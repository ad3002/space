package index

import (
	"encoding/json"
	"fmt"
	"strings"
)

// node is a tiny DSL for building ncdu exports in tests.
type node struct {
	name     string
	dir      bool
	uid      uint32
	dsize    int64
	asize    int64 // defaults to dsize
	ageDays  int64
	ino      uint64
	hardlink bool
	readErr  bool
	excluded string
	kids     []node
}

const fixtureTS = int64(1_700_000_000)

func d(name string, uid uint32, ageDays int64, kids ...node) node {
	return node{name: name, dir: true, uid: uid, dsize: 4096, ageDays: ageDays, kids: kids}
}

func f(name string, uid uint32, size int64, ageDays int64) node {
	return node{name: name, uid: uid, dsize: size, ageDays: ageDays}
}

func export(root node) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `[1,2,{"progname":"ncdu","progver":"test","timestamp":%d},`, fixtureTS)
	writeNode(&sb, root)
	sb.WriteString("]")
	return sb.String()
}

func writeNode(sb *strings.Builder, n node) {
	obj := map[string]any{"name": n.name}
	if n.excluded != "" {
		obj["excluded"] = n.excluded
	} else {
		as := n.asize
		if as == 0 {
			as = n.dsize
		}
		obj["asize"] = as
		obj["dsize"] = n.dsize
		obj["uid"] = n.uid
		obj["gid"] = n.uid
		obj["mtime"] = fixtureTS - n.ageDays*86400
		if n.ino != 0 {
			obj["ino"] = n.ino
		}
		if n.hardlink {
			obj["hlnkc"] = true
		}
		if n.readErr {
			obj["read_error"] = true
		}
	}
	b, _ := json.Marshal(obj)
	if !n.dir {
		sb.Write(b)
		return
	}
	sb.WriteString("[")
	sb.Write(b)
	for _, k := range n.kids {
		sb.WriteString(",")
		writeNode(sb, k)
	}
	sb.WriteString("]")
}

// fixtureA:
//
//	/data (root, uid 0)
//	  alice/ (1001)            a_R1.fastq 1e6 (400d), a.bam 2e6 (10d)
//	    SRR1234567/ (800d)     SRR1234567_1.fastq.gz 3e6
//	  bob/ (1002, 1000d)       old.tar 5e6, old_copy.tar hardlink of old.tar
//	    work/ab/<30 hex>/      x.bam 7e6
//	  shared/ (1003)           big.bam 2e6, a.bam 2e6 (dup of alice/a.bam)
//	  excl (excluded otherfs)
//	  locked/ (read error)
func fixtureA() node {
	oldTar := f("old.tar", 1002, 5_000_000, 1000)
	oldTar.ino, oldTar.hardlink = 77, true
	oldCopy := f("old_copy.tar", 1002, 5_000_000, 1000)
	oldCopy.ino, oldCopy.hardlink = 77, true
	aliceBam := f("a.bam", 1001, 2_000_000, 10)
	aliceBam.ino = 10
	sharedBam := f("a.bam", 1003, 2_000_000, 5)
	sharedBam.ino = 11
	locked := d("locked", 0, 5)
	locked.readErr = true
	return d("/data", 0, 1,
		d("alice", 1001, 10,
			f("a_R1.fastq", 1001, 1_000_000, 400),
			aliceBam,
			d("SRR1234567", 1001, 800,
				f("SRR1234567_1.fastq.gz", 1001, 3_000_000, 800))),
		d("bob", 1002, 1000,
			oldTar, oldCopy,
			d("work", 1002, 1000,
				d("ab", 1002, 1000,
					d("0123456789abcdef0123456789abcd", 1002, 1000,
						f("x.bam", 1002, 7_000_000, 1000))))),
		d("shared", 1003, 5,
			f("big.bam", 1003, 2_000_000, 5),
			sharedBam),
		node{name: "excl", excluded: "otherfs"},
		locked,
	)
}

// fixtureB is fixtureA a while later: alice gained 10 MB of BAM, bob's
// directory is gone, carol appeared.
func fixtureB() node {
	a := fixtureA()
	var kids []node
	for _, k := range a.kids {
		switch k.name {
		case "alice":
			k.kids = append(k.kids, f("new.bam", 1001, 10_000_000, 1))
			kids = append(kids, k)
		case "bob":
		default:
			kids = append(kids, k)
		}
	}
	kids = append(kids, d("carol", 1004, 1, f("c.fastq.gz", 1004, 1_000_000, 1)))
	a.kids = kids
	return a
}
