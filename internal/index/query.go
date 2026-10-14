package index

import (
	"sort"
	"sync"

	"space/internal/classify"
)

type rollupCache struct {
	mu sync.Mutex
	m  map[string][]int64
}

// CachedRollup is Rollup memoized under name (per index, in memory only).
func (x *Index) CachedRollup(name string, recs []Rec, match func(key uint32) bool) []int64 {
	x.rolls.mu.Lock()
	if r, ok := x.rolls.m[name]; ok {
		x.rolls.mu.Unlock()
		return r
	}
	x.rolls.mu.Unlock()
	r := x.Rollup(recs, match)
	x.rolls.mu.Lock()
	if x.rolls.m == nil || len(x.rolls.m) > 64 {
		x.rolls.m = map[string][]int64{}
	}
	x.rolls.m[name] = r
	x.rolls.mu.Unlock()
	return r
}

// Rollup returns, for every directory, the subtree sum of the records whose
// key satisfies match. It relies on pre-order layout (parent index < child).
func (x *Index) Rollup(recs []Rec, match func(key uint32) bool) []int64 {
	sub := make([]int64, len(x.Dirs))
	for _, r := range recs {
		if match(r.Key) {
			sub[r.Dir] += r.DSize
		}
	}
	for i := len(x.Dirs) - 1; i > 0; i-- {
		sub[x.Dirs[i].Parent] += sub[i]
	}
	return sub
}

// Hit is a directory selected by a query.
type Hit struct {
	Dir   int32
	Size  int64   // bytes matching the query in the subtree
	Share float64 // Size / subtree DSize
	// Direct: only the files directly inside Dir matched (the directory as a
	// whole is mixed), Size counts just those.
	Direct bool
}

// Frontier selects the outermost directories where the matching bytes make up
// at least share of the subtree and at least minSize bytes. Such directories
// "belong" to the key (an owner's area, a directory of FASTQs...). A mixed
// directory whose own files (not subdirectories) hold at least minSize
// matching bytes is reported as a Direct hit, so matches sitting next to
// other data are not lost. Returns the hits sorted by size (at most limit)
// and the total bytes covered by all hits, so callers can show how much of the
// key is explained.
func (x *Index) Frontier(sub []int64, share float64, minSize int64, limit int) ([]Hit, int64) {
	var hits []Hit
	var covered int64
	for i := 0; i < len(x.Dirs); {
		d := &x.Dirs[i]
		s := sub[i]
		skip := i + int(d.Dirs) + 1 // pre-order: the subtree is contiguous
		if s < minSize || s == 0 {
			i = skip
			continue
		}
		total := d.DSize
		if total > 0 && float64(s) >= share*float64(total) {
			hits = append(hits, Hit{Dir: int32(i), Size: s, Share: float64(s) / float64(total)})
			covered += s
			i = skip
			continue
		}
		own := s
		for c := d.FirstChild; c >= 0; c = x.Dirs[c].NextSibling {
			own -= sub[c]
		}
		if own >= minSize {
			hits = append(hits, Hit{Dir: int32(i), Size: own, Share: float64(own) / float64(max(total, 1)), Direct: true})
			covered += own
		}
		i++
	}
	sortHits(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, covered
}

// Stale selects the outermost directories whose newest mtime is before cutoff
// and whose size is at least minSize. Returns hits (at most limit) and the total
// size of all hits.
func (x *Index) Stale(cutoff, minSize int64, limit int) ([]Hit, int64) {
	var hits []Hit
	var total int64
	for i := 0; i < len(x.Dirs); {
		d := &x.Dirs[i]
		skip := i + int(d.Dirs) + 1
		if d.DSize < minSize {
			i = skip
			continue
		}
		if d.Newest < cutoff {
			hits = append(hits, Hit{Dir: int32(i), Size: d.DSize, Share: 1})
			total += d.DSize
			i = skip
			continue
		}
		i++
	}
	sortHits(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, total
}

func sortHits(h []Hit) {
	sort.Slice(h, func(i, j int) bool {
		if h[i].Size != h[j].Size {
			return h[i].Size > h[j].Size
		}
		return h[i].Dir < h[j].Dir
	})
}

// KeyTotal is a total over the whole snapshot for one key.
type KeyTotal struct {
	Key   uint32 `json:"key"`
	DSize int64  `json:"dsize"`
	Count int64  `json:"count"`
}

// Totals sums records by key, sorted by size descending.
func Totals(recs []Rec) []KeyTotal {
	m := map[uint32]*KeyTotal{}
	for _, r := range recs {
		t := m[r.Key]
		if t == nil {
			t = &KeyTotal{Key: r.Key}
			m[r.Key] = t
		}
		t.DSize += r.DSize
		t.Count += r.Count
	}
	out := make([]KeyTotal, 0, len(m))
	for _, t := range m {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DSize != out[j].DSize {
			return out[i].DSize > out[j].DSize
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// SubtreeRecs returns the records of every directory in the subtree of id.
// Requires records sorted by Dir (Build does this).
func (x *Index) SubtreeRecs(recs []Rec, id int32) []Rec {
	last := id + int32(x.Dirs[id].Dirs)
	lo := sort.Search(len(recs), func(i int) bool { return recs[i].Dir >= id })
	hi := sort.Search(len(recs), func(i int) bool { return recs[i].Dir > last })
	return recs[lo:hi]
}

// Breakdown sums the subtree records of id by key, sorted by size.
func (x *Index) Breakdown(recs []Rec, id int32) []KeyTotal {
	return Totals(x.SubtreeRecs(recs, id))
}

// DupGroup is a set of large files with the same name and size in different
// places (distinct inodes) — candidates for de-duplication.
type DupGroup struct {
	Name  string
	ASize int64
	Files []int // indexes into Index.Files
	Waste int64 // bytes that would be freed by keeping one copy
}

// Duplicates groups the kept large files by (name, apparent size).
func (x *Index) Duplicates(minSize int64, limit int) ([]DupGroup, int64) {
	type key struct {
		name string
		size int64
	}
	groups := map[key][]int{}
	for i, f := range x.Files {
		if f.ASize < minSize || f.DSize == 0 {
			continue
		}
		k := key{f.Name, f.ASize}
		groups[k] = append(groups[k], i)
	}
	var out []DupGroup
	var waste int64
	for k, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		seen := map[inoKey]bool{}
		var uniq []int
		for _, i := range idxs {
			f := x.Files[i]
			ik := inoKey{f.Dev, f.Ino}
			if f.Ino != 0 && seen[ik] {
				continue
			}
			seen[ik] = true
			uniq = append(uniq, i)
		}
		if len(uniq) < 2 {
			continue
		}
		g := DupGroup{Name: k.name, ASize: k.size, Files: uniq}
		for _, i := range uniq[1:] {
			g.Waste += x.Files[i].DSize
		}
		waste += g.Waste
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Waste != out[j].Waste {
			return out[i].Waste > out[j].Waste
		}
		return out[i].Name < out[j].Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, waste
}

// Summary is the small per-snapshot digest used for lists and trends without
// loading the full index.
type Summary struct {
	Version    int
	Meta       Meta
	DSize      int64
	ASize      int64
	Files      int64
	Dirs       int64
	Owners     []KeyTotal
	Cats       []KeyTotal
	Classes    []KeyTotal
	Top        []TopDir
	AgeHist    [NumAge]int64
	Compress   int64 // estimated bytes freed by compressing compressible categories
	ReadErrors int64
}

// TopDir is a first-level directory of the scanned root.
type TopDir struct {
	Name   string
	DSize  int64
	Files  int64
	Newest int64
}

// Summarize computes the digest.
func (x *Index) Summarize() *Summary {
	r := x.Total()
	s := &Summary{
		Version:    FormatVersion,
		Meta:       x.Meta,
		DSize:      r.DSize,
		ASize:      r.ASize,
		Files:      r.Files,
		Dirs:       r.Dirs + 1,
		Owners:     Totals(x.OwnerRecs),
		Cats:       Totals(x.CatRecs),
		Classes:    Totals(x.ClassRecs),
		AgeHist:    x.AgeHist,
		ReadErrors: x.Meta.ReadErrors,
	}
	for _, c := range s.Cats {
		s.Compress += int64(float64(c.DSize) * classify.Info(classify.Cat(c.Key)).SaveFrac)
	}
	for _, c := range x.Children(0) {
		d := &x.Dirs[c]
		s.Top = append(s.Top, TopDir{Name: d.Name, DSize: d.DSize, Files: d.Files, Newest: d.Newest})
	}
	sort.Slice(s.Top, func(i, j int) bool { return s.Top[i].DSize > s.Top[j].DSize })
	return s
}
