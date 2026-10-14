package index

import "sort"

// Diff aligns two snapshots of the same root by directory path.
type Diff struct {
	A, B *Index
	// BtoA maps each directory of B to the directory with the same path in A (or -1).
	BtoA []int32
	// AtoB maps each directory of A to its counterpart in B (or -1).
	AtoB []int32
}

// NewDiff matches directories of a (older) and b (newer) by name, top-down.
// The roots are matched regardless of their names.
func NewDiff(a, b *Index) *Diff {
	d := &Diff{A: a, B: b, BtoA: make([]int32, len(b.Dirs)), AtoB: make([]int32, len(a.Dirs))}
	for i := range d.BtoA {
		d.BtoA[i] = -1
	}
	for i := range d.AtoB {
		d.AtoB[i] = -1
	}
	d.BtoA[0], d.AtoB[0] = 0, 0
	// Pre-order guarantees a parent is matched before its children are visited.
	names := map[string]int32{}
	for bi := 0; bi < len(b.Dirs); bi++ {
		ai := d.BtoA[bi]
		if ai < 0 || b.Dirs[bi].FirstChild < 0 {
			continue
		}
		clear(names)
		for c := a.Dirs[ai].FirstChild; c >= 0; c = a.Dirs[c].NextSibling {
			names[a.Dirs[c].Name] = c
		}
		for c := b.Dirs[bi].FirstChild; c >= 0; c = b.Dirs[c].NextSibling {
			if m, ok := names[b.Dirs[c].Name]; ok {
				d.BtoA[c] = m
				d.AtoB[m] = c
			}
		}
	}
	return d
}

// Change is a size change of one directory between A and B.
type Change struct {
	Path   string `json:"path"`
	AID    int32  `json:"a_id"` // -1 if the directory is new
	BID    int32  `json:"b_id"` // -1 if the directory was removed
	ASize  int64  `json:"a_size"`
	BSize  int64  `json:"b_size"`
	Delta  int64  `json:"delta"`
	AFiles int64  `json:"a_files"`
	BFiles int64  `json:"b_files"`
	// Direct: sizes and delta cover only the files directly inside the
	// directory (not its subdirectories).
	Direct bool `json:"direct"`
}

func (d *Diff) change(ai, bi int32) Change {
	c := Change{AID: ai, BID: bi}
	if ai >= 0 {
		c.ASize = d.A.Dirs[ai].DSize
		c.AFiles = d.A.Dirs[ai].Files
	}
	if bi >= 0 {
		c.BSize = d.B.Dirs[bi].DSize
		c.BFiles = d.B.Dirs[bi].Files
		c.Path = d.B.Path(bi)
	} else {
		c.Path = d.A.Path(ai)
	}
	c.Delta = c.BSize - c.ASize
	return c
}

// Children lists the children of a directory present in A, B or both,
// identified by its B id (or, for a directory removed in B, by its A id with
// bi = -1).
func (d *Diff) Children(ai, bi int32) []Change {
	var out []Change
	if bi >= 0 {
		for c := d.B.Dirs[bi].FirstChild; c >= 0; c = d.B.Dirs[c].NextSibling {
			out = append(out, d.change(d.BtoA[c], c))
		}
	}
	if ai >= 0 {
		for c := d.A.Dirs[ai].FirstChild; c >= 0; c = d.A.Dirs[c].NextSibling {
			if d.AtoB[c] < 0 {
				out = append(out, d.change(c, -1))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return abs(out[i].Delta) > abs(out[j].Delta) })
	return out
}

// ownSize is the size of a directory minus its subdirectories: the files
// directly inside it plus the directory entry itself.
func ownSize(x *Index, id int32) int64 {
	s := x.Dirs[id].DSize
	for c := x.Dirs[id].FirstChild; c >= 0; c = x.Dirs[c].NextSibling {
		s -= x.Dirs[c].DSize
	}
	return s
}

// Origins decomposes the total change into the places where it happened:
// directories that appeared or disappeared (reported at their outermost
// level) and files directly inside directories present in both snapshots
// (Direct). Every byte of change belongs to exactly one origin, so the
// origins add up to the total; those smaller than minDelta are dropped.
func (d *Diff) Origins(minDelta int64, limit int) []Change {
	var out []Change
	for bi := int32(0); int(bi) < len(d.B.Dirs); bi++ {
		ai := d.BtoA[bi]
		if ai < 0 {
			if p := d.B.Dirs[bi].Parent; p >= 0 && d.BtoA[p] < 0 {
				continue // inside a new directory that is reported as a whole
			}
			if c := d.change(-1, bi); abs(c.Delta) >= minDelta {
				out = append(out, c)
			}
			continue
		}
		oa, ob := ownSize(d.A, ai), ownSize(d.B, bi)
		if abs(ob-oa) >= minDelta {
			out = append(out, Change{
				Path: d.B.Path(bi), AID: ai, BID: bi, ASize: oa, BSize: ob, Delta: ob - oa,
				AFiles: d.A.Dirs[ai].OwnFiles, BFiles: d.B.Dirs[bi].OwnFiles, Direct: true,
			})
		}
	}
	for ai := int32(1); int(ai) < len(d.A.Dirs); ai++ {
		if d.AtoB[ai] >= 0 || d.AtoB[d.A.Dirs[ai].Parent] < 0 {
			continue // still present, or inside a removed directory
		}
		if c := d.change(ai, -1); abs(c.Delta) >= minDelta {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if abs(out[i].Delta) != abs(out[j].Delta) {
			return abs(out[i].Delta) > abs(out[j].Delta)
		}
		return out[i].Path < out[j].Path
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
