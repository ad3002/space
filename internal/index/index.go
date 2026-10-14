// Package index turns an ncdu export into a compact, queryable snapshot:
// a directory tree with subtree totals plus sparse per-directory breakdowns by
// owner, category and reclaim class. Individual files are not kept, except the
// N largest (for "biggest files", duplicates and accession listings).
package index

import (
	"container/heap"
	"fmt"
	"io"
	"sort"
	"strings"

	"space/internal/classify"
	"space/internal/ncdu"
)

// FormatVersion changes whenever the persisted layout or aggregation rules
// change; cached indexes with another version are rebuilt.
const FormatVersion = 3

// Dir flags.
const (
	FlagReadError uint8 = 1 << iota // ncdu could not read this directory
)

// Dir is one directory. Dirs are stored in depth-first pre-order, so a
// parent always has a smaller index than its children.
type Dir struct {
	Name        string
	Parent      int32 // -1 for the root
	FirstChild  int32 // -1 if none
	NextSibling int32 // -1 if none
	Depth       uint16
	Class       classify.Class
	Flags       uint8
	UID         uint32 // owner of the directory entry itself
	Mtime       int64  // mtime of the directory entry itself
	DSize       int64  // disk usage of the whole subtree (incl. the dir entry)
	ASize       int64  // apparent size of the subtree
	Files       int64  // non-directory entries in the subtree
	Dirs        int64  // directories in the subtree, excluding itself
	OwnDSize    int64  // disk usage of direct non-directory children
	OwnFiles    int64  // number of direct non-directory children
	Newest      int64  // newest mtime anywhere in the subtree
	Errors      int32  // unreadable directories in the subtree (incl. itself)
}

// Rec is one sparse breakdown cell: for directory Dir, the direct (non
// recursive) entries with key Key sum to DSize bytes over Count entries.
type Rec struct {
	Dir   int32
	Key   uint32
	DSize int64
	Count int64
}

// File is a large file kept individually.
type File struct {
	Dir      int32
	Name     string
	DSize    int64
	ASize    int64
	Mtime    int64
	UID      uint32
	Dev      uint64
	Ino      uint64
	Cat      classify.Cat
	Class    classify.Class
	HardLink bool
}

// Excluded is an entry ncdu did not scan (other filesystem, pattern, kernfs).
type Excluded struct {
	Dir    int32
	Name   string
	Reason string
}

// Age buckets, by time between file mtime and scan time.
var AgeBuckets = []struct {
	Label   string
	MaxDays int64 // upper bound (exclusive); last bucket is open-ended
}{
	{"< 1 мес", 30}, {"1–3 мес", 91}, {"3–6 мес", 182}, {"6–12 мес", 365},
	{"1–2 года", 730}, {"2–3 года", 1095}, {"3–5 лет", 1826}, {"> 5 лет", 1 << 40},
}

// NumAge is the number of age buckets.
const NumAge = 8

// AgeBucket returns the bucket index for a file of the given mtime.
func AgeBucket(scanTime, mtime int64) int {
	days := (scanTime - mtime) / 86400
	if days < 0 {
		days = 0 // files from the future (clock skew) count as fresh
	}
	for i, b := range AgeBuckets {
		if days < b.MaxDays {
			return i
		}
	}
	return NumAge - 1
}

// Meta describes a snapshot.
type Meta struct {
	ID          string // file base name without extensions
	Root        string // scanned path
	Timestamp   int64  // scan time (from ncdu header)
	ProgName    string
	ProgVer     string
	Extended    bool  // export has uid/mtime (ncdu -e)
	HardlinkDup int64 // bytes of repeated hard links, counted once
	ReadErrors  int64
	NumExcluded int64
}

// Index is a fully built snapshot.
type Index struct {
	Version    int
	Meta       Meta
	Dirs       []Dir
	OwnerRecs  []Rec // Key = uid
	CatRecs    []Rec // Key = classify.Cat
	ClassRecs  []Rec // Key = classify.Class
	Files      []File
	FilesMin   int64 // smallest DSize among kept files (0 if all files kept)
	Excluded   []Excluded
	ExclTrunc  bool // more excluded entries existed than were kept
	AgeHist    [NumAge]int64
	OwnerAge   map[uint32]*[NumAge]int64
	CatAge     [classify.NumCats][NumAge]int64
	TotalFiles int64

	rolls rollupCache // not persisted (unexported)
}

// Options tune index building.
type Options struct {
	TopFiles    int // how many largest files to keep individually
	MaxExcluded int
}

// DefaultOptions are used by the server and CLI.
var DefaultOptions = Options{TopFiles: 200000, MaxExcluded: 5000}

type kv struct {
	key   uint32
	dsize int64
	count int64
}

func addKV(s []kv, key uint32, dsize int64) []kv {
	for i := range s {
		if s[i].key == key {
			s[i].dsize += dsize
			s[i].count++
			return s
		}
	}
	return append(s, kv{key, dsize, 1})
}

type frame struct {
	dir       int32
	dev       uint64
	lastChild int32
	owners    []kv
	cats      []kv
	classes   []kv
}

type inoKey struct{ dev, ino uint64 }

type builder struct {
	idx      *Index
	opts     Options
	stack    []frame
	seen     map[inoKey]struct{}
	top      fileHeap
	gotMeta  bool
	extended bool
}

// Build parses an ncdu export and builds the index.
func Build(r io.Reader, opts Options) (*Index, error) {
	b := &builder{
		idx: &Index{
			Version:  FormatVersion,
			OwnerAge: map[uint32]*[NumAge]int64{},
		},
		opts: opts,
		seen: map[inoKey]struct{}{},
	}
	if err := ncdu.Parse(r, b); err != nil {
		return nil, err
	}
	if len(b.idx.Dirs) == 0 {
		return nil, fmt.Errorf("export contains no root directory")
	}
	if len(b.stack) != 0 {
		return nil, fmt.Errorf("internal error: %d directories left open", len(b.stack))
	}
	b.idx.Meta.Extended = b.extended
	// Records are emitted in post-order; sort by directory so that a subtree
	// (a contiguous id range in pre-order) maps to a contiguous record range.
	for _, recs := range [][]Rec{b.idx.OwnerRecs, b.idx.CatRecs, b.idx.ClassRecs} {
		sort.Slice(recs, func(i, j int) bool {
			if recs[i].Dir != recs[j].Dir {
				return recs[i].Dir < recs[j].Dir
			}
			return recs[i].Key < recs[j].Key
		})
	}
	b.idx.Files = b.top.sorted()
	if len(b.idx.Files) >= opts.TopFiles && len(b.idx.Files) > 0 {
		b.idx.FilesMin = b.idx.Files[len(b.idx.Files)-1].DSize
	}
	return b.idx, nil
}

func (b *builder) Meta(m ncdu.Meta) {
	b.gotMeta = true
	b.idx.Meta.Timestamp = m.Timestamp
	b.idx.Meta.ProgName = m.ProgName
	b.idx.Meta.ProgVer = m.ProgVer
}

func (b *builder) EnterDir(e *ncdu.Entry) {
	idx := b.idx
	id := int32(len(idx.Dirs))
	parent := int32(-1)
	var depth uint16
	var dev uint64 = e.Dev
	pClass := classify.ClassNone
	var pName, gName string
	if n := len(b.stack); n > 0 {
		top := &b.stack[n-1]
		parent = top.dir
		pd := &idx.Dirs[parent]
		depth = pd.Depth + 1
		pClass = pd.Class
		pName = pd.Name
		if pd.Parent >= 0 {
			gName = idx.Dirs[pd.Parent].Name
		}
		if dev == 0 {
			dev = top.dev
		}
		if top.lastChild >= 0 {
			idx.Dirs[top.lastChild].NextSibling = id
		} else {
			pd.FirstChild = id
		}
		top.lastChild = id
	}
	name := string(e.Name)
	if parent < 0 {
		idx.Meta.Root = name
	}
	if e.HasUID || e.HasMtime {
		b.extended = true
	}
	d := Dir{
		Name:        name,
		Parent:      parent,
		FirstChild:  -1,
		NextSibling: -1,
		Depth:       depth,
		UID:         e.UID,
		Mtime:       e.Mtime,
		DSize:       e.DSize,
		ASize:       e.ASize,
		Newest:      e.Mtime,
	}
	if parent >= 0 {
		d.Class = classify.DirContext(name, pName, gName, pClass)
	}
	if e.ReadError {
		d.Flags |= FlagReadError
		d.Errors = 1
		idx.Meta.ReadErrors++
	}
	idx.Dirs = append(idx.Dirs, d)
	f := frame{dir: id, dev: dev, lastChild: -1}
	// The directory entry's own blocks belong to the directory itself.
	f.owners = addKV(f.owners, e.UID, e.DSize)
	f.cats = addKV(f.cats, uint32(classify.CatDirMeta), e.DSize)
	f.classes = addKV(f.classes, uint32(d.Class), e.DSize)
	b.stack = append(b.stack, f)
}

func (b *builder) File(e *ncdu.Entry) {
	top := &b.stack[len(b.stack)-1]
	idx := b.idx
	d := &idx.Dirs[top.dir]
	if e.Excluded != "" {
		idx.Meta.NumExcluded++
		if len(idx.Excluded) < b.opts.MaxExcluded {
			idx.Excluded = append(idx.Excluded, Excluded{Dir: top.dir, Name: string(e.Name), Reason: e.Excluded})
		} else {
			idx.ExclTrunc = true
		}
		return
	}
	if e.HasUID || e.HasMtime {
		b.extended = true
	}
	if e.ReadError {
		d.Errors++
		idx.Meta.ReadErrors++
	}
	dsize, asize := e.DSize, e.ASize
	if e.HardLink {
		dev := e.Dev
		if dev == 0 {
			dev = top.dev
		}
		k := inoKey{dev, e.Ino}
		if _, dup := b.seen[k]; dup {
			idx.Meta.HardlinkDup += dsize
			dsize, asize = 0, 0
		} else {
			b.seen[k] = struct{}{}
		}
	}
	name := string(e.Name)
	cat := classify.CatDirMeta
	if !e.NotReg {
		cat = classify.Category(name)
	}
	class := classify.FileClass(name, cat, d.Class)

	top.owners = addKV(top.owners, e.UID, dsize)
	top.cats = addKV(top.cats, uint32(cat), dsize)
	top.classes = addKV(top.classes, uint32(class), dsize)

	d.OwnDSize += dsize
	d.OwnFiles++
	d.DSize += dsize
	d.ASize += asize
	d.Files++
	if e.Mtime > d.Newest {
		d.Newest = e.Mtime
	}
	idx.TotalFiles++

	age := AgeBucket(idx.Meta.Timestamp, e.Mtime)
	idx.AgeHist[age] += dsize
	oa := idx.OwnerAge[e.UID]
	if oa == nil {
		oa = &[NumAge]int64{}
		idx.OwnerAge[e.UID] = oa
	}
	oa[age] += dsize
	idx.CatAge[cat][age] += dsize

	if dsize > 0 && b.opts.TopFiles > 0 {
		if len(b.top) < b.opts.TopFiles {
			heap.Push(&b.top, File{Dir: top.dir, Name: name, DSize: dsize, ASize: asize, Mtime: e.Mtime,
				UID: e.UID, Dev: e.Dev, Ino: e.Ino, Cat: cat, Class: class, HardLink: e.HardLink})
		} else if dsize > b.top[0].DSize {
			b.top[0] = File{Dir: top.dir, Name: name, DSize: dsize, ASize: asize, Mtime: e.Mtime,
				UID: e.UID, Dev: e.Dev, Ino: e.Ino, Cat: cat, Class: class, HardLink: e.HardLink}
			heap.Fix(&b.top, 0)
		}
	}
}

func (b *builder) LeaveDir() {
	n := len(b.stack)
	f := b.stack[n-1]
	b.stack = b.stack[:n-1]
	idx := b.idx
	for _, r := range f.owners {
		idx.OwnerRecs = append(idx.OwnerRecs, Rec{f.dir, r.key, r.dsize, r.count})
	}
	for _, r := range f.cats {
		idx.CatRecs = append(idx.CatRecs, Rec{f.dir, r.key, r.dsize, r.count})
	}
	for _, r := range f.classes {
		idx.ClassRecs = append(idx.ClassRecs, Rec{f.dir, r.key, r.dsize, r.count})
	}
	d := &idx.Dirs[f.dir]
	if d.Parent >= 0 {
		p := &idx.Dirs[d.Parent]
		p.DSize += d.DSize
		p.ASize += d.ASize
		p.Files += d.Files
		p.Dirs += d.Dirs + 1
		p.Errors += d.Errors
		if d.Newest > p.Newest {
			p.Newest = d.Newest
		}
	}
}

// fileHeap is a min-heap by DSize.
type fileHeap []File

func (h fileHeap) Len() int           { return len(h) }
func (h fileHeap) Less(i, j int) bool { return h[i].DSize < h[j].DSize }
func (h fileHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *fileHeap) Push(x any)        { *h = append(*h, x.(File)) }
func (h *fileHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// sorted drains the heap into a slice ordered by DSize descending.
func (h *fileHeap) sorted() []File {
	out := make([]File, h.Len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = heap.Pop(h).(File)
	}
	return out
}

// Path returns the absolute path of a directory.
func (x *Index) Path(id int32) string {
	if id < 0 || int(id) >= len(x.Dirs) {
		return ""
	}
	var parts []string
	for d := id; d >= 0; d = x.Dirs[d].Parent {
		parts = append(parts, x.Dirs[d].Name)
	}
	var sb strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "/") {
			sb.WriteByte('/')
		}
		sb.WriteString(p)
	}
	return sb.String()
}

// Children lists the child directory ids of id.
func (x *Index) Children(id int32) []int32 {
	var out []int32
	for c := x.Dirs[id].FirstChild; c >= 0; c = x.Dirs[c].NextSibling {
		out = append(out, c)
	}
	return out
}

// Lookup finds a directory by absolute path.
func (x *Index) Lookup(path string) (int32, bool) {
	root := x.Dirs[0].Name
	if path == "" || path == root {
		return 0, true
	}
	rel := strings.TrimPrefix(path, strings.TrimSuffix(root, "/"))
	if rel == path || !strings.HasPrefix(rel, "/") {
		return -1, false
	}
	cur := int32(0)
	for _, part := range strings.Split(strings.Trim(rel, "/"), "/") {
		if part == "" {
			continue
		}
		found := int32(-1)
		for c := x.Dirs[cur].FirstChild; c >= 0; c = x.Dirs[c].NextSibling {
			if x.Dirs[c].Name == part {
				found = c
				break
			}
		}
		if found < 0 {
			return -1, false
		}
		cur = found
	}
	return cur, true
}

// Total returns the root directory.
func (x *Index) Total() *Dir { return &x.Dirs[0] }
