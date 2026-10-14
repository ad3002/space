package server

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"space/internal/store"
)

// Snapshot A: /data with alice (a.fastq 2 MB, SRR1234567/ 3 MB) and bob
// (uid 4242, not in passwd; old.tar 5 MB, 1000 days old).
const exportA = `[1,2,{"progname":"ncdu","progver":"1.15","timestamp":1700000000},
[{"name":"/data","asize":4096,"dsize":4096,"uid":0,"mtime":1700000000},
[{"name":"alice","asize":4096,"dsize":4096,"uid":1001,"mtime":1699000000},
{"name":"a.fastq","asize":2000000,"dsize":2000000,"uid":1001,"mtime":1665440000},
[{"name":"SRR1234567","asize":4096,"dsize":4096,"uid":1001,"mtime":1630880000},
{"name":"SRR1234567_1.fastq.gz","asize":3000000,"dsize":3000000,"uid":1001,"mtime":1630880000}]],
[{"name":"bob","asize":4096,"dsize":4096,"uid":4242,"mtime":1613600000},
{"name":"old.tar","asize":5000000,"dsize":5000000,"uid":4242,"mtime":1613600000}]]]`

// Snapshot B: ten days later alice has a new 10 MB BAM.
const exportB = `[1,2,{"progname":"ncdu","progver":"1.15","timestamp":1700864000},
[{"name":"/data","asize":4096,"dsize":4096,"uid":0,"mtime":1700000000},
[{"name":"alice","asize":4096,"dsize":4096,"uid":1001,"mtime":1700800000},
{"name":"a.fastq","asize":2000000,"dsize":2000000,"uid":1001,"mtime":1665440000},
{"name":"new.bam","asize":10000000,"dsize":10000000,"uid":1001,"mtime":1700800000},
[{"name":"SRR1234567","asize":4096,"dsize":4096,"uid":1001,"mtime":1630880000},
{"name":"SRR1234567_1.fastq.gz","asize":3000000,"dsize":3000000,"uid":1001,"mtime":1630880000}]],
[{"name":"bob","asize":4096,"dsize":4096,"uid":4242,"mtime":1613600000},
{"name":"old.tar","asize":5000000,"dsize":5000000,"uid":4242,"mtime":1613600000}]]]`

const (
	idA    = "gen1__data__20231114-221320"
	idB    = "gen1__data__20231124-221320"
	totalA = 4*4096 + 10_000_000
	totalB = totalA + 10_000_000
)

func df(used int64) string {
	return "Filesystem 1-blocks Used Available Capacity Mounted on\n/dev/sdb 1000000000000 " +
		itoa(used) + " " + itoa(1000000000000-used) + " 10% /data\n"
}

func itoa(v int64) string { b, _ := json.Marshal(v); return string(b) }

func writeGz(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	zw.Write([]byte(content))
	zw.Close()
	f.Close()
}

func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	passwd := "root:x:0:0::/root:/bin/bash\nalice:x:1001:1001::/home/alice:/bin/bash\n"
	for _, s := range []struct {
		id, export string
		used       int64
	}{{idA, exportA, 100e9}, {idB, exportB, 110e9}} {
		base := filepath.Join(dir, s.id)
		writeGz(t, base+".ncdu.json.gz", s.export)
		os.WriteFile(base+".df", []byte(df(s.used)), 0o644)
		os.WriteFile(base+".passwd", []byte(passwd), 0o644)
	}
	// A scan still running.
	os.WriteFile(filepath.Join(dir, "gen1__data__20231201-000000.ncdu.json.gz.part"), []byte("xx"), 0o644)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(dir, 2, log)
	if err != nil {
		t.Fatal(err)
	}
	st.Refresh()
	st.IngestAll()
	static := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>space</title>")}}
	ts := httptest.NewServer(New(st, static, log))
	t.Cleanup(ts.Close)
	return ts, dir
}

func get(t *testing.T, ts *httptest.Server, path string, wantCode int, out any) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantCode {
		t.Fatalf("GET %s: status %d, want %d; body %s", path, resp.StatusCode, wantCode, body)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("GET %s: bad JSON %v: %s", path, err, body)
		}
	}
}

func TestSnapshotsList(t *testing.T) {
	ts, _ := setup(t)
	var r struct {
		Snapshots []snapJSON `json:"snapshots"`
	}
	get(t, ts, "/api/snapshots", 200, &r)
	if len(r.Snapshots) != 3 {
		t.Fatalf("snapshots = %+v", r.Snapshots)
	}
	byID := map[string]snapJSON{}
	for _, s := range r.Snapshots {
		byID[s.ID] = s
	}
	a := byID[idA]
	if a.Status != "ready" || a.DSize != totalA || a.Root != "/data" || a.Series != "gen1:/data" || a.DF == nil || !a.HasUsers {
		t.Errorf("snapshot A = %+v", a)
	}
	if s := byID["gen1__data__20231201-000000"]; s.Status != "scanning" {
		t.Errorf("part snapshot = %+v", s)
	}
}

func TestOverview(t *testing.T) {
	ts, _ := setup(t)
	var r struct {
		Totals struct {
			DSize int64 `json:"dsize"`
			Files int64 `json:"files"`
		} `json:"totals"`
		Owners   []ownerJSON      `json:"owners"`
		Cats     []catJSON        `json:"cats"`
		Classes  []classJSON      `json:"classes"`
		Reclaim  map[string]int64 `json:"reclaim"`
		Top      []dirJSON        `json:"top"`
		Warnings []warning        `json:"warnings"`
	}
	get(t, ts, "/api/s/"+idA+"/overview", 200, &r)
	if r.Totals.DSize != totalA || r.Totals.Files != 3 {
		t.Errorf("totals = %+v", r.Totals)
	}
	// alice (5 008 192) is 4 KiB bigger than bob's uid 4242 (5 004 096).
	if len(r.Owners) != 3 || r.Owners[0].Name != "alice" || r.Owners[0].Orphan || r.Owners[0].DSize != 2*4096+5_000_000 {
		t.Errorf("first owner should be alice: %+v", r.Owners)
	}
	if r.Owners[1].Name != "uid 4242" || !r.Owners[1].Orphan || r.Owners[1].DSize != 4096+5_000_000 {
		t.Errorf("second owner should be orphan uid 4242: %+v", r.Owners[1])
	}
	if r.Reclaim["orphan"] != 4096+5_000_000 || r.Reclaim["orphan_users"] != 1 {
		t.Errorf("reclaim orphan = %v", r.Reclaim)
	}
	if r.Reclaim["public"] != 4096+3_000_000 {
		t.Errorf("reclaim public = %d", r.Reclaim["public"])
	}
	// fastq 2e6*0.75 + tar 5e6*0.5
	if r.Reclaim["compress"] != 1_500_000+2_500_000 {
		t.Errorf("reclaim compress = %d", r.Reclaim["compress"])
	}
	// > 1 year old: a.fastq (400d), SRR fastq.gz (800d), old.tar (1000d)
	if r.Reclaim["old"] != 10_000_000 {
		t.Errorf("reclaim old = %d", r.Reclaim["old"])
	}
	if len(r.Top) != 2 || r.Top[1].Name != "bob" || r.Top[1].TopOwner == nil || r.Top[1].TopOwner.UID != 4242 || !r.Top[1].TopOwner.Orphan {
		t.Errorf("top = %+v", r.Top)
	}
	for _, c := range r.Cats {
		if c.Key == "fastq" && (c.DSize != 2_000_000 || c.EstSave != 1_500_000 || c.Compress == "") {
			t.Errorf("fastq cat = %+v", c)
		}
	}
	// df says 100 GB used on /data while ncdu counted 10 MB: that gap must be
	// explained, not hidden.
	if len(r.Warnings) != 1 || r.Warnings[0].Level != "info" || !strings.Contains(r.Warnings[0].Text, "df") {
		t.Errorf("warnings = %+v", r.Warnings)
	}
}

func TestOverviewWarnsOnBrokenSidecar(t *testing.T) {
	ts, dir := setup(t)
	os.WriteFile(filepath.Join(dir, idA+".df"), []byte("garbage"), 0o644)
	// Touch the export so Refresh re-reads sidecars.
	p := filepath.Join(dir, idA+".ncdu.json.gz")
	writeGz(t, p, exportA)
	var list struct {
		Snapshots []snapJSON `json:"snapshots"`
	}
	get(t, ts, "/api/snapshots", 200, &list)
	for _, s := range list.Snapshots {
		if s.ID == idA && (len(s.SidecarErrors) == 0 || !strings.Contains(s.SidecarErrors[0], ".df")) {
			t.Errorf("sidecar error not surfaced in list: %+v", s)
		}
	}
}

func TestDir(t *testing.T) {
	ts, _ := setup(t)
	var r struct {
		Dir      dirJSON     `json:"dir"`
		Crumbs   []crumb     `json:"crumbs"`
		Children []dirJSON   `json:"children"`
		Files    []fileJSON  `json:"files"`
		Owners   []ownerJSON `json:"owners"`
	}
	get(t, ts, "/api/s/"+idA+"/dir?path=/data/alice", 200, &r)
	if r.Dir.Path != "/data/alice" || r.Dir.DSize != 2*4096+5_000_000 || r.Dir.Owner.Name != "alice" {
		t.Errorf("dir = %+v", r.Dir)
	}
	if len(r.Crumbs) != 2 || r.Crumbs[0].Path != "/data" || r.Crumbs[1].Name != "alice" {
		t.Errorf("crumbs = %+v", r.Crumbs)
	}
	if len(r.Children) != 1 || r.Children[0].Name != "SRR1234567" || r.Children[0].Class != "public" {
		t.Errorf("children = %+v", r.Children)
	}
	if len(r.Files) != 1 || r.Files[0].Name != "a.fastq" || r.Files[0].Cat != "fastq" || r.Files[0].Path != "/data/alice/a.fastq" {
		t.Errorf("files = %+v", r.Files)
	}
	if len(r.Owners) != 1 || r.Owners[0].UID != 1001 {
		t.Errorf("owners = %+v", r.Owners)
	}
	var e apiError
	get(t, ts, "/api/s/"+idA+"/dir?path=/data/nope", 404, &e)
	if !strings.Contains(e.Error, "/data/nope") {
		t.Errorf("error = %q", e.Error)
	}
}

func TestOwnerCatClass(t *testing.T) {
	ts, _ := setup(t)
	var o struct {
		Owner ownerRef   `json:"owner"`
		DSize int64      `json:"dsize"`
		Dirs  []hitJSON  `json:"dirs"`
		Files []fileJSON `json:"files"`
	}
	get(t, ts, "/api/s/"+idA+"/owner/1001", 200, &o)
	if o.Owner.Name != "alice" || o.DSize != 2*4096+5_000_000 || len(o.Dirs) != 1 || o.Dirs[0].Path != "/data/alice" {
		t.Errorf("owner = %+v", o)
	}
	if len(o.Files) != 2 || o.Files[0].DSize != 3_000_000 {
		t.Errorf("owner files = %+v", o.Files)
	}

	var c struct {
		DSize   int64     `json:"dsize"`
		EstSave int64     `json:"est_save"`
		Dirs    []hitJSON `json:"dirs"`
		Covered int64     `json:"covered"`
	}
	get(t, ts, "/api/s/"+idA+"/cat/fastq", 200, &c)
	// alice is mixed (fastq is 40%), so the hit is the files directly in it.
	if c.DSize != 2_000_000 || c.Covered != 2_000_000 || len(c.Dirs) != 1 || c.Dirs[0].Path != "/data/alice" || c.Dirs[0].Match != 2_000_000 {
		t.Errorf("cat fastq = %+v", c)
	}

	var cl struct {
		DSize int64      `json:"dsize"`
		Dirs  []hitJSON  `json:"dirs"`
		Files []fileJSON `json:"files"`
	}
	get(t, ts, "/api/s/"+idA+"/class/public", 200, &cl)
	if len(cl.Dirs) != 1 || cl.Dirs[0].Accession != "SRR1234567" || cl.Dirs[0].Path != "/data/alice/SRR1234567" {
		t.Errorf("class public = %+v", cl)
	}
	if len(cl.Files) != 1 || cl.Files[0].Accession != "SRR1234567" {
		t.Errorf("public files = %+v", cl.Files)
	}
	var e apiError
	get(t, ts, "/api/s/"+idA+"/cat/nonsense", 404, &e)
	get(t, ts, "/api/s/"+idA+"/owner/abc", 400, &e)
	get(t, ts, "/api/s/"+idA+"/owner/777", 404, &e)
	if !strings.Contains(e.Error, "777") {
		t.Errorf("owner 404 error = %q", e.Error)
	}
}

func TestStaleAndDups(t *testing.T) {
	ts, _ := setup(t)
	var s struct {
		Total int64     `json:"total"`
		Dirs  []hitJSON `json:"dirs"`
	}
	get(t, ts, "/api/s/"+idA+"/stale?days=365&min=1", 200, &s)
	if len(s.Dirs) != 2 || s.Dirs[0].Path != "/data/bob" || s.Dirs[1].Path != "/data/alice/SRR1234567" {
		t.Fatalf("stale = %+v", s.Dirs)
	}
	if s.Total != (4096+5_000_000)+(4096+3_000_000) {
		t.Errorf("stale total = %d", s.Total)
	}
	if s.Dirs[0].TopOwner == nil || !s.Dirs[0].TopOwner.Orphan {
		t.Errorf("stale top owner = %+v", s.Dirs[0].TopOwner)
	}
	var e apiError
	get(t, ts, "/api/s/"+idA+"/stale?days=-3", 400, &e)
	if !strings.Contains(e.Error, "days") {
		t.Errorf("error = %q", e.Error)
	}
	var d struct {
		Waste  int64 `json:"waste"`
		Groups []any `json:"groups"`
	}
	get(t, ts, "/api/s/"+idA+"/dups?min=1", 200, &d)
	if d.Waste != 0 || len(d.Groups) != 0 {
		t.Errorf("dups = %+v", d)
	}
}

func TestTrendAndDiff(t *testing.T) {
	ts, _ := setup(t)
	var tr struct {
		Points   []trendPoint `json:"points"`
		Forecast forecastJSON `json:"forecast"`
	}
	get(t, ts, "/api/trend?series=gen1:/data", 200, &tr)
	if len(tr.Points) != 2 || tr.Points[0].ID != idA || tr.Points[1].DSize != totalB {
		t.Fatalf("points = %+v", tr.Points)
	}
	if tr.Points[1].Owners["alice"] != 2*4096+15_000_000 || tr.Points[1].Owners["uid 4242"] != 4096+5_000_000 {
		t.Errorf("owners in trend = %v", tr.Points[1].Owners)
	}
	f := tr.Forecast
	if f.Basis != "df" || f.PerDay < 0.99e9 || f.PerDay > 1.01e9 || f.DaysToFull < 885 || f.DaysToFull > 895 {
		t.Errorf("forecast = %+v", f)
	}

	var d struct {
		Total    int64        `json:"total"`
		Origins  []originJSON `json:"origins"`
		Children []originJSON `json:"children"`
		A        snapJSON     `json:"a"`
	}
	// Passing them in reverse order must still diff older -> newer.
	get(t, ts, "/api/diff?a="+idB+"&b="+idA+"&path=/data", 200, &d)
	// Same root: no warnings, and the field must be an empty array, not null
	// (the UI iterates it).
	var raw map[string]json.RawMessage
	get(t, ts, "/api/diff?a="+idA+"&b="+idB+"&path=/data", 200, &raw)
	if string(raw["warnings"]) != "[]" {
		t.Errorf("diff warnings = %s, want []", raw["warnings"])
	}
	if d.A.ID != idA || d.Total != 10_000_000 {
		t.Errorf("diff header = %+v total %d", d.A.ID, d.Total)
	}
	if len(d.Origins) != 1 || d.Origins[0].Path != "/data/alice" || d.Origins[0].Delta != 10_000_000 || !d.Origins[0].Direct {
		t.Errorf("origins = %+v", d.Origins)
	}
	if len(d.Children) != 2 || d.Children[0].Path != "/data/alice" {
		t.Errorf("diff children = %+v", d.Children)
	}
}

type originJSON struct {
	Path   string `json:"path"`
	Delta  int64  `json:"delta"`
	Direct bool   `json:"direct"`
}

func TestNotReadyAndMissing(t *testing.T) {
	ts, _ := setup(t)
	var e apiError
	get(t, ts, "/api/s/gen1__data__20231201-000000/overview", 409, &e)
	if e.Status != "scanning" {
		t.Errorf("409 body = %+v", e)
	}
	get(t, ts, "/api/s/nope/overview", 404, &e)
	if !strings.Contains(e.Error, "nope") {
		t.Errorf("404 body = %+v", e)
	}
	get(t, ts, "/api/trend?series=nope", 404, &e)
}

func TestServesUI(t *testing.T) {
	ts, _ := setup(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<title>space</title>") {
		t.Errorf("index: %d %s", resp.StatusCode, body)
	}
}
