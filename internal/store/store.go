// Package store discovers ncdu snapshots in a directory, indexes them in the
// background and caches the results next to them (in .space-cache/).
//
// File layout written by scripts/collect.sh:
//
//	<host>__<label>__<YYYYmmdd-HHMMSS>.ncdu.json.gz        the ncdu export
//	<host>__<label>__<YYYYmmdd-HHMMSS>.ncdu.json.gz.part   while scanning
//	<host>__<label>__<YYYYmmdd-HHMMSS>.df                  `df -B1 -P <path>`
//	<host>__<label>__<YYYYmmdd-HHMMSS>.passwd / .group     `getent passwd/group`
//
// Any *.ncdu.json[.gz] / *.json[.gz] file is accepted; sidecars are optional.
package store

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"space/internal/index"
)

// Snapshot statuses.
const (
	StatusScanning = "scanning" // .part file: ncdu still running (or died)
	StatusPending  = "pending"
	StatusIndexing = "indexing"
	StatusReady    = "ready"
	StatusError    = "error"
)

// DF is the parsed `df -B1 -P` sidecar.
type DF struct {
	Filesystem string `json:"filesystem"`
	Size       int64  `json:"size"`
	Used       int64  `json:"used"`
	Avail      int64  `json:"avail"`
	Mount      string `json:"mount"`
}

// Snapshot is one export file and everything known about it.
type Snapshot struct {
	ID            string            `json:"id"`
	Host          string            `json:"host"`
	Label         string            `json:"label"`
	Source        string            `json:"source"`
	SourceSize    int64             `json:"source_size"`
	SourceMtime   time.Time         `json:"source_mtime"`
	Status        string            `json:"status"`
	Error         string            `json:"error,omitempty"`
	Progress      float64           `json:"progress"`
	IndexSeconds  float64           `json:"index_seconds,omitempty"`
	Summary       *index.Summary    `json:"-"`
	DF            *DF               `json:"df,omitempty"`
	Users         map[uint32]string `json:"-"`
	Groups        map[uint32]string `json:"-"`
	SidecarErrors []string          `json:"sidecar_errors,omitempty"`

	progress atomic.Int64 // bytes of source consumed while indexing
}

// Series groups snapshots of the same host and root path.
func (s *Snapshot) Series() string {
	root := s.Label
	if s.Summary != nil {
		root = s.Summary.Meta.Root
	}
	if s.Host != "" {
		return s.Host + ":" + root
	}
	return root
}

// Time is the scan time, falling back to the file mtime.
func (s *Snapshot) Time() time.Time {
	if s.Summary != nil && s.Summary.Meta.Timestamp > 0 {
		return time.Unix(s.Summary.Meta.Timestamp, 0)
	}
	return s.SourceMtime
}

// Store manages the snapshot directory.
type Store struct {
	dir       string
	cacheDir  string
	maxLoaded int
	log       *slog.Logger
	opts      index.Options

	mu     sync.Mutex
	snaps  map[string]*Snapshot
	queue  []string
	wake   chan struct{}
	loaded []loadedIndex // most recently used last
	loadMu sync.Mutex    // serialises index loads (they are big)
}

type loadedIndex struct {
	id  string
	idx *index.Index
}

var exportSuffixes = []string{".ncdu.json.gz", ".ncdu.json", ".json.gz", ".json"}

// Open prepares a store over dir. Call Refresh, then Run in a goroutine.
func Open(dir string, maxLoaded int, log *slog.Logger) (*Store, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("snapshot directory: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("snapshot directory %s is not a directory", dir)
	}
	cache := filepath.Join(dir, ".space-cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	if maxLoaded < 1 {
		maxLoaded = 1
	}
	return &Store{
		dir: dir, cacheDir: cache, maxLoaded: maxLoaded, log: log,
		opts:  index.DefaultOptions,
		snaps: map[string]*Snapshot{}, wake: make(chan struct{}, 1),
	}, nil
}

// Dir returns the snapshot directory.
func (s *Store) Dir() string { return s.dir }

func splitExport(name string) (base string, ok bool) {
	for _, suf := range exportSuffixes {
		if strings.HasSuffix(name, suf) && len(name) > len(suf) {
			return strings.TrimSuffix(name, suf), true
		}
	}
	return "", false
}

func parseName(base string) (host, label string) {
	parts := strings.Split(base, "__")
	if len(parts) >= 3 {
		return parts[0], parts[1]
	}
	return "", base
}

// Refresh rescans the directory: new or changed exports are queued, deleted
// ones forgotten, sidecars re-read.
func (s *Store) Refresh() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("read snapshot directory: %w", err)
	}
	seen := map[string]bool{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".part") {
			base, ok := splitExport(strings.TrimSuffix(name, ".part"))
			if !ok {
				continue
			}
			id := base
			seen[id] = true
			info, err := e.Info()
			if err != nil {
				continue // vanished between ReadDir and Info (scan finished)
			}
			sn := s.snaps[id]
			if sn == nil || sn.Status == StatusScanning {
				host, label := parseName(base)
				s.snaps[id] = &Snapshot{ID: id, Host: host, Label: label, Source: filepath.Join(s.dir, name),
					SourceSize: info.Size(), SourceMtime: info.ModTime(), Status: StatusScanning}
			}
			continue
		}
		base, ok := splitExport(name)
		if !ok {
			continue
		}
		id := base
		info, err := e.Info()
		if err != nil {
			continue
		}
		seen[id] = true
		sn := s.snaps[id]
		if sn != nil && sn.Status != StatusScanning && sn.SourceSize == info.Size() && sn.SourceMtime.Equal(info.ModTime()) {
			continue // unchanged
		}
		host, label := parseName(base)
		sn = &Snapshot{ID: id, Host: host, Label: label, Source: filepath.Join(s.dir, name),
			SourceSize: info.Size(), SourceMtime: info.ModTime(), Status: StatusPending}
		s.readSidecars(sn)
		if sum, err := s.readCachedSummary(sn); err == nil {
			sn.Summary = sum
			sn.Status = StatusReady
			sn.Progress = 1
		} else {
			if !errors.Is(err, fs.ErrNotExist) {
				s.log.Info("re-indexing snapshot", "id", id, "reason", err)
			}
			s.queue = append(s.queue, id)
		}
		s.snaps[id] = sn
		s.dropLoaded(id)
	}
	for id := range s.snaps {
		if !seen[id] {
			delete(s.snaps, id)
			s.dropLoaded(id)
		}
	}
	if len(s.queue) > 0 {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// cachedSummary is persisted as JSON next to the index.
type cachedSummary struct {
	Version     int
	SourceSize  int64
	SourceMtime time.Time
	Seconds     float64
	Summary     *index.Summary
}

func (s *Store) summaryPath(id string) string { return filepath.Join(s.cacheDir, id+".summary.json") }
func (s *Store) indexPath(id string) string   { return filepath.Join(s.cacheDir, id+".idx.gz") }

func (s *Store) readCachedSummary(sn *Snapshot) (*index.Summary, error) {
	b, err := os.ReadFile(s.summaryPath(sn.ID))
	if err != nil {
		return nil, err
	}
	var c cachedSummary
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("corrupt cached summary: %w", err)
	}
	if c.Version != index.FormatVersion {
		return nil, fmt.Errorf("cached summary has format %d, want %d", c.Version, index.FormatVersion)
	}
	if c.SourceSize != sn.SourceSize || !c.SourceMtime.Equal(sn.SourceMtime) {
		return nil, fmt.Errorf("export changed since it was indexed")
	}
	if _, err := os.Stat(s.indexPath(sn.ID)); err != nil {
		return nil, fmt.Errorf("cached index missing: %w", err)
	}
	sn.IndexSeconds = c.Seconds
	return c.Summary, nil
}

// Run processes the indexing queue until stop is closed.
func (s *Store) Run(stop <-chan struct{}) {
	for {
		s.mu.Lock()
		var id string
		if len(s.queue) > 0 {
			id, s.queue = s.queue[0], s.queue[1:]
		}
		s.mu.Unlock()
		if id == "" {
			select {
			case <-stop:
				return
			case <-s.wake:
				continue
			case <-time.After(30 * time.Second):
				if err := s.Refresh(); err != nil {
					s.log.Error("refresh failed", "err", err)
				}
				continue
			}
		}
		s.ingest(id)
	}
}

// IngestAll synchronously indexes everything queued (used by the CLI and tests).
func (s *Store) IngestAll() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		id := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		s.ingest(id)
	}
}

func (s *Store) ingest(id string) {
	s.mu.Lock()
	sn := s.snaps[id]
	if sn == nil || sn.Status != StatusPending {
		s.mu.Unlock()
		return
	}
	sn.Status = StatusIndexing
	src, size, mtime := sn.Source, sn.SourceSize, sn.SourceMtime
	s.mu.Unlock()

	start := time.Now()
	s.log.Info("indexing snapshot", "id", id, "bytes", size)
	x, err := s.build(sn, src)
	secs := time.Since(start).Seconds()

	var sum *index.Summary
	if err == nil {
		sum = x.Summarize()
		err = s.writeCache(id, x, cachedSummary{
			Version: index.FormatVersion, SourceSize: size, SourceMtime: mtime, Seconds: secs, Summary: sum,
		})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.snaps[id]
	if cur != sn {
		return // file replaced meanwhile; the new entry is already queued
	}
	if err != nil {
		sn.Status = StatusError
		sn.Error = err.Error()
		s.log.Error("indexing failed", "id", id, "err", err)
		return
	}
	sn.Summary = sum
	sn.Status = StatusReady
	sn.Progress = 1
	sn.IndexSeconds = secs
	s.putLoaded(id, x)
	s.log.Info("indexed snapshot", "id", id, "dirs", len(x.Dirs), "files", x.TotalFiles, "seconds", secs)
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (s *Store) build(sn *Snapshot, path string) (*index.Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open export: %w", err)
	}
	defer f.Close()
	sn.progress.Store(0)
	var r io.Reader = countingReader{f, &sn.progress}
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("export is not valid gzip: %w", err)
		}
		defer zr.Close()
		r = zr
	}
	x, err := index.Build(r, s.opts)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("gzip stream truncated — was the scan interrupted or the copy incomplete? (%v)", err)
		}
		return nil, err
	}
	if !x.Meta.Extended {
		return nil, fmt.Errorf("export has no owner/mtime data: rescan with `ncdu -e` (scripts/collect.sh does this)")
	}
	x.Meta.ID = sn.ID
	return x, nil
}

func (s *Store) writeCache(id string, x *index.Index, c cachedSummary) error {
	tmp := s.indexPath(id) + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("write index cache: %w", err)
	}
	if err := index.Save(f, x); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write index cache: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write index cache: %w", err)
	}
	if err := os.Rename(tmp, s.indexPath(id)); err != nil {
		return fmt.Errorf("write index cache: %w", err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode summary: %w", err)
	}
	stmp := s.summaryPath(id) + ".tmp"
	if err := os.WriteFile(stmp, b, 0o644); err != nil {
		return fmt.Errorf("write summary cache: %w", err)
	}
	if err := os.Rename(stmp, s.summaryPath(id)); err != nil {
		return fmt.Errorf("write summary cache: %w", err)
	}
	return nil
}

// List returns snapshots sorted by series, then time.
func (s *Store) List() []*Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Snapshot, 0, len(s.snaps))
	for _, sn := range s.snaps {
		if sn.Status == StatusIndexing && sn.SourceSize > 0 {
			sn.Progress = float64(sn.progress.Load()) / float64(sn.SourceSize)
		}
		out = append(out, sn)
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Series(), out[j].Series(); a != b {
			return a < b
		}
		return out[i].Time().Before(out[j].Time())
	})
	return out
}

// Get returns one snapshot.
func (s *Store) Get(id string) (*Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sn, ok := s.snaps[id]
	return sn, ok
}

// ErrNotReady is returned for snapshots that are not indexed (yet).
var ErrNotReady = errors.New("snapshot is not indexed")

// Index returns the full index of a ready snapshot, loading it if needed.
func (s *Store) Index(id string) (*index.Index, error) {
	s.mu.Lock()
	sn, ok := s.snaps[id]
	if !ok {
		s.mu.Unlock()
		return nil, fs.ErrNotExist
	}
	if sn.Status != StatusReady {
		s.mu.Unlock()
		return nil, ErrNotReady
	}
	if x := s.getLoaded(id); x != nil {
		s.mu.Unlock()
		return x, nil
	}
	s.mu.Unlock()

	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	s.mu.Lock()
	if x := s.getLoaded(id); x != nil {
		s.mu.Unlock()
		return x, nil
	}
	s.mu.Unlock()
	f, err := os.Open(s.indexPath(id))
	if err != nil {
		return nil, fmt.Errorf("open cached index: %w", err)
	}
	defer f.Close()
	x, err := index.Load(f)
	if err != nil {
		// Corrupt or outdated cache: surface the error and re-index.
		s.mu.Lock()
		if cur := s.snaps[id]; cur == sn {
			sn.Status = StatusPending
			sn.Error = ""
			s.queue = append(s.queue, id)
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}
		s.mu.Unlock()
		return nil, fmt.Errorf("cached index unreadable, re-indexing: %w", err)
	}
	s.mu.Lock()
	s.putLoaded(id, x)
	s.mu.Unlock()
	return x, nil
}

// getLoaded/putLoaded/dropLoaded require s.mu.
func (s *Store) getLoaded(id string) *index.Index {
	for i, l := range s.loaded {
		if l.id == id {
			s.loaded = append(append(s.loaded[:i:i], s.loaded[i+1:]...), l)
			return l.idx
		}
	}
	return nil
}

func (s *Store) putLoaded(id string, x *index.Index) {
	s.dropLoaded(id)
	s.loaded = append(s.loaded, loadedIndex{id, x})
	for len(s.loaded) > s.maxLoaded {
		s.loaded = s.loaded[1:]
	}
}

func (s *Store) dropLoaded(id string) {
	for i, l := range s.loaded {
		if l.id == id {
			s.loaded = append(s.loaded[:i], s.loaded[i+1:]...)
			return
		}
	}
}
