package store

import (
	"compress/gzip"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tinyExport = `[1,2,{"progname":"ncdu","progver":"test","timestamp":1700000000},
[{"name":"/data","asize":4096,"dsize":4096,"uid":0,"mtime":1700000000},
[{"name":"alice","asize":4096,"dsize":4096,"uid":1001,"mtime":1700000000},
{"name":"r.fastq","asize":1000,"dsize":4096,"uid":1001,"mtime":1690000000}],
{"name":"x.bam","asize":8000,"dsize":8192,"uid":4242,"mtime":1600000000}]]`

const dfOut = `Filesystem       1-blocks        Used   Available Capacity Mounted on
/dev/sdb    3936818806784 1181645000000 2555000000000      32% /mnt/beta
`

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func writeGz(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIngestWithSidecars(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "gen1__data__20231114-221320")
	writeGz(t, base+".ncdu.json.gz", tinyExport)
	os.WriteFile(base+".df", []byte(dfOut), 0o644)
	os.WriteFile(base+".passwd", []byte("root:x:0:0::/root:/bin/bash\nalice:x:1001:1001::/home/alice:/bin/bash\n"), 0o644)

	st, err := Open(dir, 2, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Refresh(); err != nil {
		t.Fatal(err)
	}
	st.IngestAll()
	list := st.List()
	if len(list) != 1 {
		t.Fatalf("snapshots = %d", len(list))
	}
	sn := list[0]
	if sn.Status != StatusReady {
		t.Fatalf("status = %s (%s)", sn.Status, sn.Error)
	}
	if sn.Host != "gen1" || sn.Series() != "gen1:/data" {
		t.Errorf("host/series = %q / %q", sn.Host, sn.Series())
	}
	if sn.DF == nil || sn.DF.Size != 3936818806784 || sn.DF.Mount != "/mnt/beta" {
		t.Errorf("df = %+v", sn.DF)
	}
	if sn.Users[1001] != "alice" {
		t.Errorf("users = %v", sn.Users)
	}
	if sn.Summary.DSize != 4096*2+4096+8192 {
		t.Errorf("summary dsize = %d", sn.Summary.DSize)
	}
	x, err := st.Index(sn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if x.Total().Files != 2 {
		t.Errorf("files = %d", x.Total().Files)
	}

	// A second store over the same dir must reuse the cache (no queue).
	st2, _ := Open(dir, 2, quietLog())
	st2.Refresh()
	if len(st2.queue) != 0 {
		t.Errorf("cache not reused, queue = %v", st2.queue)
	}
	if s2, _ := st2.Get(sn.ID); s2.Status != StatusReady || s2.Summary == nil {
		t.Errorf("cached snapshot = %+v", s2)
	}
	if _, err := st2.Index(sn.ID); err != nil {
		t.Errorf("load cached index: %v", err)
	}
}

func TestTruncatedExportIsVisibleError(t *testing.T) {
	dir := t.TempDir()
	writeGz(t, filepath.Join(dir, "h__data__20231114-000000.ncdu.json.gz"), tinyExport[:len(tinyExport)/2])
	st, _ := Open(dir, 1, quietLog())
	st.Refresh()
	st.IngestAll()
	sn := st.List()[0]
	if sn.Status != StatusError || !strings.Contains(sn.Error, "truncated") {
		t.Fatalf("status = %s, error = %q", sn.Status, sn.Error)
	}
	if _, err := st.Index(sn.ID); err != ErrNotReady {
		t.Errorf("Index on failed snapshot: %v", err)
	}
}

func TestCorruptGzipIsVisibleError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "h__data__20231114-000000.ncdu.json.gz"), []byte("not gzip at all"), 0o644)
	st, _ := Open(dir, 1, quietLog())
	st.Refresh()
	st.IngestAll()
	sn := st.List()[0]
	if sn.Status != StatusError || !strings.Contains(sn.Error, "gzip") {
		t.Fatalf("status = %s, error = %q", sn.Status, sn.Error)
	}
}

func TestNonExtendedExportRejected(t *testing.T) {
	dir := t.TempDir()
	plain := `[1,0,{"timestamp":1},[{"name":"/d","asize":1,"dsize":4096},{"name":"f","asize":1,"dsize":4096}]]`
	os.WriteFile(filepath.Join(dir, "plain.ncdu.json"), []byte(plain), 0o644)
	st, _ := Open(dir, 1, quietLog())
	st.Refresh()
	st.IngestAll()
	sn := st.List()[0]
	if sn.Status != StatusError || !strings.Contains(sn.Error, "ncdu -e") {
		t.Fatalf("status = %s, error = %q", sn.Status, sn.Error)
	}
}

func TestPartFileShowsScanning(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "h__data__20231114-000000.ncdu.json.gz.part"), []byte("xx"), 0o644)
	st, _ := Open(dir, 1, quietLog())
	st.Refresh()
	list := st.List()
	if len(list) != 1 || list[0].Status != StatusScanning {
		t.Fatalf("list = %+v", list)
	}
	if len(st.queue) != 0 {
		t.Error("part file must not be indexed")
	}
}

func TestMalformedSidecarsSurface(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "h__data__20231114-000000")
	writeGz(t, base+".ncdu.json.gz", tinyExport)
	os.WriteFile(base+".df", []byte("Filesystem Size Used\n/dev/x 10G 5G 5G 50% /d\n"), 0o644)
	os.WriteFile(base+".passwd", []byte("alice:x:notanumber:1::/:/bin/sh\nbob:x:1002:1002::/:/bin/sh\n"), 0o644)
	st, _ := Open(dir, 1, quietLog())
	st.Refresh()
	sn := st.List()[0]
	joined := strings.Join(sn.SidecarErrors, "|")
	if !strings.Contains(joined, ".df:") || !strings.Contains(joined, "-B1") {
		t.Errorf("df error not surfaced: %v", sn.SidecarErrors)
	}
	if !strings.Contains(joined, ".passwd: line 1") {
		t.Errorf("passwd error not surfaced: %v", sn.SidecarErrors)
	}
	if sn.Users[1002] != "bob" {
		t.Errorf("valid passwd lines should still load: %v", sn.Users)
	}
}

func TestDeletedSnapshotForgotten(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h__data__20231114-000000.ncdu.json.gz")
	writeGz(t, p, tinyExport)
	st, _ := Open(dir, 1, quietLog())
	st.Refresh()
	st.IngestAll()
	os.Remove(p)
	st.Refresh()
	if n := len(st.List()); n != 0 {
		t.Errorf("snapshots after delete = %d", n)
	}
}
