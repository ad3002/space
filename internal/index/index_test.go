package index

import (
	"bytes"
	"strings"
	"testing"

	"space/internal/classify"
)

func build(t *testing.T, n node) *Index {
	t.Helper()
	x, err := Build(strings.NewReader(export(n)), DefaultOptions)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return x
}

func keyTotal(ts []KeyTotal, key uint32) int64 {
	for _, t := range ts {
		if t.Key == key {
			return t.DSize
		}
	}
	return 0
}

func TestBuildTotals(t *testing.T) {
	x := build(t, fixtureA())
	const dirBytes = 9 * 4096
	const fileBytes int64 = 1e6 + 2e6 + 3e6 + 5e6 + 7e6 + 2e6 + 2e6 // hard link counted once
	if got := x.Total().DSize; got != dirBytes+fileBytes {
		t.Errorf("root DSize = %d, want %d", got, dirBytes+fileBytes)
	}
	if x.Meta.HardlinkDup != 5_000_000 {
		t.Errorf("HardlinkDup = %d", x.Meta.HardlinkDup)
	}
	if x.Meta.Root != "/data" || x.Meta.Timestamp != fixtureTS || !x.Meta.Extended {
		t.Errorf("meta = %+v", x.Meta)
	}
	if x.Meta.ReadErrors != 1 || x.Total().Errors != 1 {
		t.Errorf("read errors = %d / root.Errors = %d", x.Meta.ReadErrors, x.Total().Errors)
	}
	if x.Meta.NumExcluded != 1 || len(x.Excluded) != 1 || x.Excluded[0].Reason != "otherfs" {
		t.Errorf("excluded = %+v", x.Excluded)
	}
	if x.Total().Files != 8 || x.TotalFiles != 8 {
		t.Errorf("files = %d / %d, want 8", x.Total().Files, x.TotalFiles)
	}
	if x.Total().Dirs != 8 {
		t.Errorf("subdirs = %d, want 8", x.Total().Dirs)
	}

	owners := Totals(x.OwnerRecs)
	want := map[uint32]int64{1001: 2*4096 + 6e6, 1002: 4*4096 + 12e6, 1003: 4096 + 4e6, 0: 2 * 4096}
	var sum int64
	for uid, w := range want {
		if got := keyTotal(owners, uid); got != w {
			t.Errorf("owner %d = %d, want %d", uid, got, w)
		}
		sum += w
	}
	if sum != x.Total().DSize {
		t.Errorf("owner totals %d != root %d", sum, x.Total().DSize)
	}
	if owners[0].Key != 1002 {
		t.Errorf("owners not sorted by size: first = %d", owners[0].Key)
	}

	cats := Totals(x.CatRecs)
	if got := keyTotal(cats, uint32(classify.CatBam)); got != 13e6 {
		t.Errorf("bam = %d, want 13e6", got)
	}
	if got := keyTotal(cats, uint32(classify.CatFastq)); got != 1e6 {
		t.Errorf("fastq = %d", got)
	}
	if got := keyTotal(cats, uint32(classify.CatTarRaw)); got != 5e6 {
		t.Errorf("tar = %d", got)
	}
	classes := Totals(x.ClassRecs)
	if got := keyTotal(classes, uint32(classify.ClassTemp)); got != 4096+7e6 {
		t.Errorf("temp = %d", got)
	}
	if got := keyTotal(classes, uint32(classify.ClassPublic)); got != 4096+3e6 {
		t.Errorf("public = %d", got)
	}
	// Age: a_R1.fastq (400d) is in the 1-2y bucket; x.bam/old.tar (1000d) and
	// SRR1234567_1.fastq.gz (800d) in 2-3y; the repeated hard link adds nothing.
	if x.AgeHist[4] != 1e6 {
		t.Errorf("age 1-2y = %d", x.AgeHist[4])
	}
	if x.AgeHist[5] != 15e6 {
		t.Errorf("age 2-3y = %d", x.AgeHist[5])
	}
	if x.OwnerAge[1002][5] != 12e6 {
		t.Errorf("bob age 2-3y = %d", x.OwnerAge[1002][5])
	}
}

func TestPathLookup(t *testing.T) {
	x := build(t, fixtureA())
	id, ok := x.Lookup("/data/bob/work/ab")
	if !ok {
		t.Fatal("lookup failed")
	}
	if x.Path(id) != "/data/bob/work/ab" {
		t.Errorf("path = %q", x.Path(id))
	}
	if _, ok := x.Lookup("/data/nope"); ok {
		t.Error("lookup of missing dir succeeded")
	}
	if _, ok := x.Lookup("/other/bob"); ok {
		t.Error("lookup outside root succeeded")
	}
	if id, ok := x.Lookup("/data"); !ok || id != 0 {
		t.Error("root lookup")
	}
	var names []string
	for _, c := range x.Children(0) {
		names = append(names, x.Dirs[c].Name)
	}
	if strings.Join(names, ",") != "alice,bob,shared,locked" {
		t.Errorf("children = %v", names)
	}
}

func TestFrontierOwnerAndTemp(t *testing.T) {
	x := build(t, fixtureA())
	sub := x.Rollup(x.OwnerRecs, func(k uint32) bool { return k == 1001 })
	hits, covered := x.Frontier(sub, 0.8, 1, 10)
	if len(hits) != 1 || x.Path(hits[0].Dir) != "/data/alice" || hits[0].Share != 1 {
		t.Fatalf("alice frontier = %+v", hits)
	}
	if covered != 2*4096+6e6 {
		t.Errorf("covered = %d", covered)
	}
	tsub := x.Rollup(x.ClassRecs, func(k uint32) bool { return k == uint32(classify.ClassTemp) })
	hits, _ = x.Frontier(tsub, 0.8, 1, 10)
	if len(hits) != 1 || x.Path(hits[0].Dir) != "/data/bob/work" {
		t.Fatalf("temp frontier = %+v", hits)
	}
}

func TestStale(t *testing.T) {
	x := build(t, fixtureA())
	cutoff := fixtureTS - 365*86400
	hits, total := x.Stale(cutoff, 1, 10)
	var paths []string
	for _, h := range hits {
		paths = append(paths, x.Path(h.Dir))
	}
	if strings.Join(paths, ",") != "/data/bob,/data/alice/SRR1234567" {
		t.Fatalf("stale = %v", paths)
	}
	if total != (4*4096+12e6)+(4096+3e6) {
		t.Errorf("stale total = %d", total)
	}
	// minSize filters small dirs out.
	hits, _ = x.Stale(cutoff, 5e6, 10)
	if len(hits) != 1 {
		t.Errorf("stale with minSize = %d hits", len(hits))
	}
}

func TestDuplicates(t *testing.T) {
	x := build(t, fixtureA())
	groups, waste := x.Duplicates(1, 10)
	if len(groups) != 1 || groups[0].Name != "a.bam" || len(groups[0].Files) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	if waste != 2e6 {
		t.Errorf("waste = %d", waste)
	}
}

func TestTopFilesSortedAndBounded(t *testing.T) {
	opts := DefaultOptions
	opts.TopFiles = 3
	x, err := Build(strings.NewReader(export(fixtureA())), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Files) != 3 {
		t.Fatalf("files kept = %d", len(x.Files))
	}
	if x.Files[0].Name != "x.bam" || x.Files[0].DSize != 7e6 || x.Files[1].DSize != 5e6 || x.Files[2].DSize != 3e6 {
		t.Errorf("top files = %+v", x.Files)
	}
	if x.FilesMin != 3e6 {
		t.Errorf("FilesMin = %d", x.FilesMin)
	}
}

func TestDiffOrigins(t *testing.T) {
	a := build(t, fixtureA())
	b := build(t, fixtureB())
	df := NewDiff(a, b)
	got := map[string]int64{}
	var sum int64
	for _, c := range df.Origins(1, 0) {
		sum += c.Delta
	}
	// Origins are an exact decomposition of the total change.
	if total := b.Total().DSize - a.Total().DSize; sum != total {
		t.Errorf("origins sum to %d, total change is %d", sum, total)
	}
	for _, c := range df.Origins(500_000, 0) {
		got[c.Path] = c.Delta
		switch c.Path {
		case "/data/alice":
			if !c.Direct || c.BID < 0 || c.AID < 0 {
				t.Errorf("alice's growth is in its own files: %+v", c)
			}
		case "/data/bob":
			if c.BID != -1 || c.Direct {
				t.Errorf("bob should be reported as removed: %+v", c)
			}
		case "/data/carol":
			if c.AID != -1 || c.Direct {
				t.Errorf("carol should be reported as new: %+v", c)
			}
		}
	}
	want := map[string]int64{
		"/data/alice": 10e6,
		"/data/bob":   -(4*4096 + 12e6),
		"/data/carol": 4096 + 1e6,
	}
	if len(got) != len(want) {
		t.Fatalf("origins = %v", got)
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("origin %s = %d, want %d", p, got[p], w)
		}
	}
	kids := df.Children(0, 0)
	if len(kids) != 5 { // alice, bob(removed), shared, locked, carol
		t.Fatalf("diff children = %+v", kids)
	}
	if kids[0].Path != "/data/bob" || kids[0].BID != -1 {
		t.Errorf("largest change should be removed bob, got %+v", kids[0])
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	x := build(t, fixtureA())
	var buf bytes.Buffer
	if err := Save(&buf, x); err != nil {
		t.Fatal(err)
	}
	y, err := Load(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if y.Total().DSize != x.Total().DSize || len(y.Dirs) != len(x.Dirs) || len(y.Files) != len(x.Files) {
		t.Errorf("round trip mismatch")
	}
	if y.OwnerAge[1002][5] != 12e6 {
		t.Errorf("owner age lost: %v", y.OwnerAge[1002])
	}
	if y.Path(3) != x.Path(3) {
		t.Errorf("path mismatch %q vs %q", y.Path(3), x.Path(3))
	}
}

func TestLoadRejectsOtherVersion(t *testing.T) {
	x := build(t, fixtureA())
	x.Version = FormatVersion + 100
	var buf bytes.Buffer
	if err := Save(&buf, x); err != nil {
		t.Fatal(err)
	}
	_, err := Load(&buf)
	if _, ok := err.(ErrStaleFormat); !ok {
		t.Fatalf("expected ErrStaleFormat, got %v", err)
	}
}

func TestBuildTruncatedFails(t *testing.T) {
	s := export(fixtureA())
	_, err := Build(strings.NewReader(s[:len(s)-40]), DefaultOptions)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected truncation error, got %v", err)
	}
}

func TestSummary(t *testing.T) {
	x := build(t, fixtureA())
	s := x.Summarize()
	if s.DSize != x.Total().DSize || s.Files != 8 || s.Dirs != 9 {
		t.Errorf("summary totals = %+v", s)
	}
	if len(s.Top) != 4 || s.Top[0].Name != "bob" {
		t.Errorf("top = %+v", s.Top)
	}
	// fastq 1e6*0.75 + bam 13e6*0.45 + tar 5e6*0.5
	want := int64(1e6*0.75 + 13e6*0.45 + 5e6*0.5)
	if s.Compress != want {
		t.Errorf("compress estimate = %d, want %d", s.Compress, want)
	}
}
