// Command synth writes a synthetic ncdu export shaped like a genomics server
// (users' home/project trees, sequencing runs, pipeline work dirs, conda),
// for load-testing the indexer and UI. Deterministic for a given -seed.
package main

import (
	"bufio"
	"compress/gzip"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"time"
)

type gen struct {
	w      *bufio.Writer
	r      *rand.Rand
	files  int
	target int
	now    int64
	ino    uint64
}

var (
	users = []uint32{1001, 1002, 1003, 1004, 1005, 1006, 1007, 1008, 2001, 2002, 4242, 4343}
	exts  = []struct {
		ext  string
		size float64 // median bytes
		w    int
	}{
		{".fastq.gz", 2e9, 10}, {".fastq", 6e9, 2}, {".bam", 5e9, 6}, {".bam.bai", 5e6, 6},
		{".cram", 2e9, 2}, {".vcf.gz", 2e8, 4}, {".vcf", 8e8, 1}, {".sam", 2e10, 1},
		{".bed", 1e6, 6}, {".tsv", 5e5, 20}, {".txt", 2e4, 20}, {".log", 5e4, 20},
		{".py", 5e3, 10}, {".png", 3e5, 8}, {".h5ad", 3e9, 2}, {".pod5", 4e9, 1},
		{".json", 2e3, 20}, {".fa", 3e9, 1},
	}
	extW int
)

func init() {
	for _, e := range exts {
		extW += e.w
	}
}

func (g *gen) pickExt() (string, float64) {
	n := g.r.Intn(extW)
	for _, e := range exts {
		if n < e.w {
			return e.ext, e.size
		}
		n -= e.w
	}
	return ".txt", 1e4
}

func (g *gen) entry(name string, uid uint32, size int64, age int64, dir bool) {
	g.ino++
	mode := 33188
	if dir {
		mode = 16877
		size = 4096
	}
	fmt.Fprintf(g.w, `{"name":%s,"asize":%d,"dsize":%d,"ino":%d,"uid":%d,"gid":%d,"mode":%d,"mtime":%d}`,
		strconv.Quote(name), size, (size+4095)/4096*4096, g.ino, uid, uid, mode, g.now-age)
}

func (g *gen) file(name string, uid uint32, age int64) {
	ext, med := g.pickExt()
	size := int64(med * g.r.ExpFloat64() * 0.5)
	if g.r.Intn(3) > 0 {
		size /= 1000 // most files are small
	}
	g.w.WriteByte(',')
	g.entry(name+ext, uid, size, age, false)
	g.files++
}

// dir writes a directory with nFiles files and recursion into sub dirs.
func (g *gen) dir(name string, uid uint32, age int64, depth int, fn func()) {
	g.w.WriteString(",[")
	g.entry(name, uid, 4096, age, true)
	fn()
	g.w.WriteByte(']')
}

func (g *gen) tree(uid uint32, depth int, baseAge int64) {
	n := 5 + g.r.Intn(40)
	for i := 0; i < n && g.files < g.target; i++ {
		age := baseAge + int64(g.r.Intn(200))*86400
		g.file(fmt.Sprintf("sample_%04d_R%d", g.r.Intn(10000), 1+g.r.Intn(2)), uid, age)
	}
	if depth <= 0 {
		return
	}
	k := 1 + g.r.Intn(6)
	used := map[string]bool{} // sibling names are unique on a real filesystem
	for i := 0; i < k && g.files < g.target; i++ {
		var name string
		switch g.r.Intn(10) {
		case 0:
			name = fmt.Sprintf("SRR%d", 1000000+g.r.Intn(9000000))
		case 1:
			name = "tmp"
		default:
			name = fmt.Sprintf("run_%03d", g.r.Intn(1000))
		}
		if used[name] {
			name = fmt.Sprintf("%s_%d", name, i)
		}
		used[name] = true
		age := baseAge + int64(g.r.Intn(100))*86400
		g.dir(name, uid, age, depth-1, func() { g.tree(uid, depth-1, age) })
	}
}

func main() {
	files := flag.Int("files", 1_000_000, "approximate number of files")
	out := flag.String("out", "", "output .ncdu.json.gz path")
	seed := flag.Int64("seed", 1, "random seed")
	ts := flag.Int64("time", time.Now().Unix(), "scan timestamp")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: synth -files N -out path.ncdu.json.gz")
		os.Exit(2)
	}
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	g := &gen{w: bufio.NewWriterSize(zw, 1<<20), r: rand.New(rand.NewSource(*seed)), target: *files, now: *ts}
	fmt.Fprintf(g.w, `[1,2,{"progname":"synth","progver":"1","timestamp":%d},`, *ts)
	g.w.WriteByte('[')
	g.entry("/data", 0, 4096, 0, true)
	g.dir("home", 0, 86400, 0, func() {
		for _, u := range users {
			u := u
			g.dir(fmt.Sprintf("user%d", u), u, 86400, 0, func() {
				g.dir("miniconda3", u, 400*86400, 0, func() {
					for i := 0; i < 2000 && g.files < g.target; i++ {
						g.file(fmt.Sprintf("pkgs_%d", i), u, 400*86400)
					}
				})
			})
		}
	})
	for g.files < g.target {
		for _, u := range users {
			if g.files >= g.target {
				break
			}
			age := int64(g.r.Intn(2000)) * 86400
			g.dir(fmt.Sprintf("project_%d_%d", u, g.r.Intn(1<<20)), u, age, 6, func() {
				g.tree(u, 6, age)
				if g.r.Intn(4) == 0 {
					g.dir("work", u, age, 0, func() {
						for i := 0; i < 20 && g.files < g.target; i++ {
							g.dir(fmt.Sprintf("%02x", g.r.Intn(256)), u, age, 0, func() {
								g.dir(fmt.Sprintf("%030x", g.r.Uint64()), u, age, 0, func() {
									g.file("tmp_sorted", u, age)
								})
							})
						}
					})
				}
			})
		}
	}
	g.w.WriteString("]]")
	if err := g.w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := zw.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %d files to %s\n", g.files, *out)
}
