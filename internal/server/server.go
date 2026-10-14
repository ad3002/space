// Package server exposes snapshots over a JSON API and serves the UI.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"space/internal/classify"
	"space/internal/index"
	"space/internal/store"
)

// Server is the HTTP handler.
type Server struct {
	st  *store.Store
	mux *http.ServeMux
	log *slog.Logger

	diffMu  sync.Mutex
	diffKey string
	diff    *index.Diff
}

// New builds the handler. static holds index.html and assets.
func New(st *store.Store, static fs.FS, log *slog.Logger) *Server {
	s := &Server{st: st, mux: http.NewServeMux(), log: log}
	s.mux.HandleFunc("GET /api/snapshots", s.handleSnapshots)
	s.mux.HandleFunc("GET /api/s/{id}/overview", s.withIndex(s.handleOverview))
	s.mux.HandleFunc("GET /api/s/{id}/dir", s.withIndex(s.handleDir))
	s.mux.HandleFunc("GET /api/s/{id}/owners", s.withIndex(s.handleOwners))
	s.mux.HandleFunc("GET /api/s/{id}/owner/{uid}", s.withIndex(s.handleOwner))
	s.mux.HandleFunc("GET /api/s/{id}/cats", s.withIndex(s.handleCats))
	s.mux.HandleFunc("GET /api/s/{id}/cat/{key}", s.withIndex(s.handleCat))
	s.mux.HandleFunc("GET /api/s/{id}/class/{key}", s.withIndex(s.handleClass))
	s.mux.HandleFunc("GET /api/s/{id}/stale", s.withIndex(s.handleStale))
	s.mux.HandleFunc("GET /api/s/{id}/dups", s.withIndex(s.handleDups))
	s.mux.HandleFunc("GET /api/trend", s.handleTrend)
	s.mux.HandleFunc("GET /api/diff", s.handleDiff)
	s.mux.Handle("GET /", http.FileServerFS(static))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // client went away; nothing useful to do
}

type apiError struct {
	Error  string `json:"error"`
	Status string `json:"status,omitempty"`
}

func fail(w http.ResponseWriter, code int, format string, args ...any) {
	writeJSON(w, code, apiError{Error: fmt.Sprintf(format, args...)})
}

type req struct {
	w  http.ResponseWriter
	r  *http.Request
	sn *store.Snapshot
	x  *index.Index
}

func (q *req) intParam(name string, def int64) (int64, bool) {
	v := q.r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		fail(q.w, http.StatusBadRequest, "parameter %s must be a non-negative integer, got %q", name, v)
		return 0, false
	}
	return n, true
}

func (s *Server) withIndex(h func(q *req)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		sn, x, ok := s.load(w, id)
		if !ok {
			return
		}
		h(&req{w: w, r: r, sn: sn, x: x})
	}
}

func (s *Server) load(w http.ResponseWriter, id string) (*store.Snapshot, *index.Index, bool) {
	sn, ok := s.st.Get(id)
	if !ok {
		fail(w, http.StatusNotFound, "snapshot %q not found", id)
		return nil, nil, false
	}
	x, err := s.st.Index(id)
	if errors.Is(err, store.ErrNotReady) {
		writeJSON(w, http.StatusConflict, apiError{Error: "snapshot is not indexed yet", Status: sn.Status})
		return nil, nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return nil, nil, false
	}
	return sn, x, true
}

func joinPath(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

type ownerRef struct {
	UID    uint32  `json:"uid"`
	Name   string  `json:"name"`
	Orphan bool    `json:"orphan"`
	Share  float64 `json:"share,omitempty"`
}

func owner(sn *store.Snapshot, uid uint32) ownerRef {
	if sn.Users == nil {
		return ownerRef{UID: uid, Name: fmt.Sprintf("uid %d", uid)}
	}
	if n, ok := sn.Users[uid]; ok {
		return ownerRef{UID: uid, Name: n}
	}
	return ownerRef{UID: uid, Name: fmt.Sprintf("uid %d", uid), Orphan: true}
}

// ---------- snapshots ----------

type snapJSON struct {
	ID            string    `json:"id"`
	Host          string    `json:"host"`
	Label         string    `json:"label"`
	Series        string    `json:"series"`
	Status        string    `json:"status"`
	Error         string    `json:"error,omitempty"`
	Progress      float64   `json:"progress"`
	Time          int64     `json:"time"`
	SourceSize    int64     `json:"source_size"`
	SourceMtime   int64     `json:"source_mtime"`
	IndexSeconds  float64   `json:"index_seconds,omitempty"`
	DF            *store.DF `json:"df"`
	SidecarErrors []string  `json:"sidecar_errors"`
	HasUsers      bool      `json:"has_users"`
	Root          string    `json:"root,omitempty"`
	DSize         int64     `json:"dsize"`
	Files         int64     `json:"files"`
	Dirs          int64     `json:"dirs"`
	ReadErrors    int64     `json:"read_errors"`
	Excluded      int64     `json:"excluded"`
	NcduVersion   string    `json:"ncdu_version,omitempty"`
}

func snapInfo(sn *store.Snapshot) snapJSON {
	j := snapJSON{
		ID: sn.ID, Host: sn.Host, Label: sn.Label, Series: sn.Series(), Status: sn.Status,
		Error: sn.Error, Progress: sn.Progress, Time: sn.Time().Unix(), SourceSize: sn.SourceSize,
		SourceMtime: sn.SourceMtime.Unix(), IndexSeconds: sn.IndexSeconds, DF: sn.DF,
		SidecarErrors: sn.SidecarErrors, HasUsers: sn.Users != nil,
	}
	if j.SidecarErrors == nil {
		j.SidecarErrors = []string{}
	}
	if s := sn.Summary; s != nil {
		j.Root = s.Meta.Root
		j.DSize, j.Files, j.Dirs = s.DSize, s.Files, s.Dirs
		j.ReadErrors, j.Excluded = s.Meta.ReadErrors, s.Meta.NumExcluded
		j.NcduVersion = s.Meta.ProgName + " " + s.Meta.ProgVer
	}
	return j
}

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	refreshErr := ""
	if err := s.st.Refresh(); err != nil {
		refreshErr = err.Error()
	}
	list := s.st.List()
	out := make([]snapJSON, 0, len(list))
	for _, sn := range list {
		out = append(out, snapInfo(sn))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dir":           s.st.Dir(),
		"snapshots":     out,
		"refresh_error": refreshErr,
	})
}

// ---------- overview ----------

type ownerJSON struct {
	ownerRef
	DSize int64                `json:"dsize"`
	Count int64                `json:"count"`
	Age   *[index.NumAge]int64 `json:"age,omitempty"`
	Old   int64                `json:"old"` // bytes not modified for > 1 year
}

type catJSON struct {
	classify.CatInfo
	DSize   int64 `json:"dsize"`
	Count   int64 `json:"count"`
	EstSave int64 `json:"est_save"`
}

type classJSON struct {
	classify.ClassInfo
	DSize int64 `json:"dsize"`
	Count int64 `json:"count"`
}

type warning struct {
	Level string `json:"level"` // error | warn | info
	Text  string `json:"text"`
}

func ageLabels() []string {
	out := make([]string, len(index.AgeBuckets))
	for i, b := range index.AgeBuckets {
		out[i] = b.Label
	}
	return out
}

func oldBytes(a *[index.NumAge]int64) int64 {
	var n int64
	for i := 4; i < index.NumAge; i++ { // buckets from "1–2 года" on
		n += a[i]
	}
	return n
}

func (s *Server) owners(sn *store.Snapshot, x *index.Index, recs []index.KeyTotal, total int64) []ownerJSON {
	out := make([]ownerJSON, 0, len(recs))
	for _, t := range recs {
		o := ownerJSON{ownerRef: owner(sn, t.Key), DSize: t.DSize, Count: t.Count}
		if total > 0 {
			o.Share = float64(t.DSize) / float64(total)
		}
		if a := x.OwnerAge[t.Key]; a != nil {
			o.Age = a
			o.Old = oldBytes(a)
		}
		out = append(out, o)
	}
	return out
}

func cats(totals []index.KeyTotal) []catJSON {
	out := make([]catJSON, 0, len(totals))
	for _, t := range totals {
		info := classify.Info(classify.Cat(t.Key))
		out = append(out, catJSON{CatInfo: info, DSize: t.DSize, Count: t.Count,
			EstSave: int64(float64(t.DSize) * info.SaveFrac)})
	}
	return out
}

func classes(totals []index.KeyTotal) []classJSON {
	out := make([]classJSON, 0, len(totals))
	for _, t := range totals {
		if classify.Class(t.Key) == classify.ClassNone {
			continue
		}
		out = append(out, classJSON{ClassInfo: classify.ClassMeta(classify.Class(t.Key)), DSize: t.DSize, Count: t.Count})
	}
	return out
}

func (s *Server) handleOverview(q *req) {
	x, sn := q.x, q.sn
	root := x.Total()
	sum := sn.Summary
	ownersAll := s.owners(sn, x, sum.Owners, root.DSize)
	var orphanBytes int64
	var orphanCount int
	for _, o := range ownersAll {
		if o.Orphan {
			orphanBytes += o.DSize
			orphanCount++
		}
	}
	clsTotals := map[string]int64{}
	for _, c := range sum.Classes {
		clsTotals[classify.ClassMeta(classify.Class(c.Key)).Key] = c.DSize
	}
	age := make([]map[string]any, index.NumAge)
	for i, l := range ageLabels() {
		age[i] = map[string]any{"label": l, "dsize": x.AgeHist[i]}
	}
	var top []dirJSON
	for _, c := range sum.Top {
		if len(top) >= 20 {
			break
		}
		id, ok := x.Lookup(joinPath(root.Name, c.Name))
		if !ok {
			continue
		}
		top = append(top, s.dirInfo(sn, x, id, true))
	}
	var dupWaste int64
	_, dupWaste = x.Duplicates(100<<20, 0)

	warns := []warning{}
	if sn.DF == nil {
		warns = append(warns, warning{"warn", "Нет файла .df: объём диска и свободное место неизвестны. scripts/collect.sh записывает его автоматически."})
	} else if gap := sn.DF.Used - root.DSize; sn.DF.Used > 0 && gap > sn.DF.Used/10 {
		if strings.TrimSuffix(sn.DF.Mount, "/") == strings.TrimSuffix(x.Meta.Root, "/") {
			warns = append(warns, warning{"info", fmt.Sprintf(
				"ncdu насчитал %s, а df показывает занятыми %s на %s. Разница %s: каталоги, пропущенные при сканировании (другие ФС, ошибки чтения), удалённые, но ещё открытые процессами файлы, метаданные ФС.",
				human(root.DSize), human(sn.DF.Used), sn.DF.Mount, human(gap))})
		} else {
			warns = append(warns, warning{"info", fmt.Sprintf(
				"Снимок %s охватывает %s из %s, занятых на файловой системе %s (по df). Остальное лежит вне просканированного каталога.",
				x.Meta.Root, human(root.DSize), human(sn.DF.Used), sn.DF.Mount)})
		}
	}
	if sn.Users == nil {
		warns = append(warns, warning{"warn", "Нет файла .passwd: владельцы показаны числовыми uid, удалённых пользователей определить нельзя."})
	}
	for _, e := range sn.SidecarErrors {
		warns = append(warns, warning{"error", "Вспомогательный файл снимка повреждён: " + e})
	}
	if x.Meta.ReadErrors > 0 {
		warns = append(warns, warning{"error", fmt.Sprintf("ncdu не смог прочитать %d каталог(ов): их содержимое в размерах не учтено. См. «Проводник», метка ⚠.", x.Meta.ReadErrors)})
	}
	if x.Meta.NumExcluded > 0 {
		warns = append(warns, warning{"info", fmt.Sprintf("Пропущено %d элементов (другие файловые системы или исключения): их размер не учтён.", x.Meta.NumExcluded)})
	}

	writeJSON(q.w, http.StatusOK, map[string]any{
		"snapshot": snapInfo(sn),
		"totals": map[string]any{
			"dsize": root.DSize, "asize": root.ASize, "files": root.Files, "dirs": root.Dirs + 1,
			"hardlink_dup": x.Meta.HardlinkDup,
		},
		"owners":  ownersAll,
		"cats":    cats(sum.Cats),
		"classes": classes(sum.Classes),
		"age":     age,
		"reclaim": map[string]any{
			"compress":     sum.Compress,
			"public":       clsTotals["public"],
			"refgenome":    clsTotals["refgenome"],
			"temp":         clsTotals["temp"],
			"software":     clsTotals["software"],
			"trash":        clsTotals["trash"],
			"old":          oldBytes(&x.AgeHist),
			"orphan":       orphanBytes,
			"orphan_users": orphanCount,
			"dups":         dupWaste,
		},
		"top":      top,
		"warnings": warns,
	})
}

// ---------- directories ----------

type dirJSON struct {
	ID        int32     `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	DSize     int64     `json:"dsize"`
	ASize     int64     `json:"asize"`
	Files     int64     `json:"files"`
	Dirs      int64     `json:"dirs"`
	Newest    int64     `json:"newest"`
	Mtime     int64     `json:"mtime"`
	Owner     ownerRef  `json:"owner"`
	TopOwner  *ownerRef `json:"top_owner,omitempty"`
	Class     string    `json:"class"`
	Errors    int32     `json:"errors"`
	ReadError bool      `json:"read_error"`
	OwnDSize  int64     `json:"own_dsize"`
	OwnFiles  int64     `json:"own_files"`
	HasKids   bool      `json:"has_kids"`
}

func (s *Server) dirInfo(sn *store.Snapshot, x *index.Index, id int32, withTopOwner bool) dirJSON {
	d := &x.Dirs[id]
	j := dirJSON{
		ID: id, Name: d.Name, Path: x.Path(id), DSize: d.DSize, ASize: d.ASize, Files: d.Files,
		Dirs: d.Dirs, Newest: d.Newest, Mtime: d.Mtime, Owner: owner(sn, d.UID),
		Class: classify.ClassMeta(d.Class).Key, Errors: d.Errors,
		ReadError: d.Flags&index.FlagReadError != 0, OwnDSize: d.OwnDSize, OwnFiles: d.OwnFiles,
		HasKids: d.FirstChild >= 0,
	}
	if withTopOwner && d.DSize > 0 {
		if b := x.Breakdown(x.OwnerRecs, id); len(b) > 0 {
			o := owner(sn, b[0].Key)
			o.Share = float64(b[0].DSize) / float64(d.DSize)
			j.TopOwner = &o
		}
	}
	return j
}

type fileJSON struct {
	Path      string   `json:"path"`
	Name      string   `json:"name"`
	DSize     int64    `json:"dsize"`
	ASize     int64    `json:"asize"`
	Mtime     int64    `json:"mtime"`
	Owner     ownerRef `json:"owner"`
	Cat       string   `json:"cat"`
	CatLabel  string   `json:"cat_label"`
	Class     string   `json:"class"`
	HardLink  bool     `json:"hardlink"`
	Accession string   `json:"accession,omitempty"`
}

func fileInfo(sn *store.Snapshot, x *index.Index, f *index.File) fileJSON {
	dir := x.Path(f.Dir)
	p := joinPath(dir, f.Name)
	j := fileJSON{Path: p, Name: f.Name, DSize: f.DSize, ASize: f.ASize, Mtime: f.Mtime,
		Owner: owner(sn, f.UID), Cat: classify.Info(f.Cat).Key, CatLabel: classify.Info(f.Cat).Label,
		Class: classify.ClassMeta(f.Class).Key, HardLink: f.HardLink}
	if f.Class == classify.ClassPublic {
		j.Accession = classify.ExtractAccession(p)
	}
	return j
}

func (s *Server) files(sn *store.Snapshot, x *index.Index, keep func(*index.File) bool, limit int) []fileJSON {
	out := []fileJSON{}
	for i := range x.Files {
		f := &x.Files[i]
		if keep(f) {
			out = append(out, fileInfo(sn, x, f))
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

type crumb struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Server) handleDir(q *req) {
	x, sn := q.x, q.sn
	path := q.r.URL.Query().Get("path")
	id, ok := x.Lookup(path)
	if !ok {
		fail(q.w, http.StatusNotFound, "directory %q is not in this snapshot", path)
		return
	}
	var crumbs []crumb
	for d := id; d >= 0; d = x.Dirs[d].Parent {
		crumbs = append(crumbs, crumb{Name: x.Dirs[d].Name, Path: x.Path(d)})
	}
	for i, j := 0, len(crumbs)-1; i < j; i, j = i+1, j-1 {
		crumbs[i], crumbs[j] = crumbs[j], crumbs[i]
	}
	kids := x.Children(id)
	sort.Slice(kids, func(i, j int) bool { return x.Dirs[kids[i]].DSize > x.Dirs[kids[j]].DSize })
	const maxKids = 500
	more := 0
	if len(kids) > maxKids {
		more = len(kids) - maxKids
		kids = kids[:maxKids]
	}
	children := make([]dirJSON, 0, len(kids))
	for _, c := range kids {
		children = append(children, s.dirInfo(sn, x, c, true))
	}
	ownersB := s.owners(sn, x, x.Breakdown(x.OwnerRecs, id), x.Dirs[id].DSize)
	if len(ownersB) > 10 {
		ownersB = ownersB[:10]
	}
	for i := range ownersB {
		ownersB[i].Age = nil
	}
	catB := cats(x.Breakdown(x.CatRecs, id))
	if len(catB) > 10 {
		catB = catB[:10]
	}
	writeJSON(q.w, http.StatusOK, map[string]any{
		"dir":       s.dirInfo(sn, x, id, false),
		"crumbs":    crumbs,
		"children":  children,
		"more_kids": more,
		"files":     s.files(sn, x, func(f *index.File) bool { return f.Dir == id }, 100),
		"files_min": x.FilesMin,
		"owners":    ownersB,
		"cats":      catB,
		"classes":   classes(x.Breakdown(x.ClassRecs, id)),
	})
}

// ---------- owners / categories / classes ----------

func (s *Server) handleOwners(q *req) {
	root := q.x.Total()
	writeJSON(q.w, http.StatusOK, map[string]any{
		"owners":     s.owners(q.sn, q.x, q.sn.Summary.Owners, root.DSize),
		"age_labels": ageLabels(),
		"has_users":  q.sn.Users != nil,
	})
}

type hitJSON struct {
	dirJSON
	Match     int64   `json:"match"`
	Share     float64 `json:"share"`
	Accession string  `json:"accession,omitempty"`
}

func (s *Server) hits(sn *store.Snapshot, x *index.Index, hits []index.Hit, topOwner bool) []hitJSON {
	out := make([]hitJSON, 0, len(hits))
	for _, h := range hits {
		out = append(out, hitJSON{dirJSON: s.dirInfo(sn, x, h.Dir, topOwner), Match: h.Size, Share: h.Share})
	}
	return out
}

// minHit picks a size floor for frontier listings: small directories are
// noise at this scale.
func minHit(total int64) int64 {
	return max(1<<20, total/1000)
}

func (s *Server) frontier(q *req, name string, recs []index.Rec, key uint32, share float64, total int64) ([]hitJSON, int64) {
	sub := q.x.CachedRollup(name, recs, func(k uint32) bool { return k == key })
	hits, covered := q.x.Frontier(sub, share, minHit(total), 200)
	return s.hits(q.sn, q.x, hits, true), covered
}

func (s *Server) handleOwner(q *req) {
	uid64, err := strconv.ParseUint(q.r.PathValue("uid"), 10, 32)
	if err != nil {
		fail(q.w, http.StatusBadRequest, "uid must be a number")
		return
	}
	uid := uint32(uid64)
	var total, count int64
	for _, t := range q.sn.Summary.Owners {
		if t.Key == uid {
			total, count = t.DSize, t.Count
		}
	}
	if count == 0 {
		fail(q.w, http.StatusNotFound, "uid %d owns nothing in this snapshot", uid)
		return
	}
	hits, covered := s.frontier(q, fmt.Sprintf("owner:%d", uid), q.x.OwnerRecs, uid, 0.8, total)
	var age *[index.NumAge]int64
	if a := q.x.OwnerAge[uid]; a != nil {
		age = a
	}
	writeJSON(q.w, http.StatusOK, map[string]any{
		"owner":      owner(q.sn, uid),
		"dsize":      total,
		"count":      count,
		"share":      float64(total) / float64(q.x.Total().DSize),
		"dirs":       hits,
		"covered":    covered,
		"age":        age,
		"age_labels": ageLabels(),
		"files":      s.files(q.sn, q.x, func(f *index.File) bool { return f.UID == uid }, 50),
	})
}

func (s *Server) handleCats(q *req) {
	out := cats(q.sn.Summary.Cats)
	writeJSON(q.w, http.StatusOK, map[string]any{"cats": out, "total": q.x.Total().DSize})
}

func catByKey(key string) (classify.Cat, bool) {
	for _, c := range classify.All() {
		if c.Key == key {
			return c.ID, true
		}
	}
	return 0, false
}

func (s *Server) handleCat(q *req) {
	cat, ok := catByKey(q.r.PathValue("key"))
	if !ok {
		fail(q.w, http.StatusNotFound, "unknown category %q", q.r.PathValue("key"))
		return
	}
	var total, count int64
	for _, t := range q.sn.Summary.Cats {
		if classify.Cat(t.Key) == cat {
			total, count = t.DSize, t.Count
		}
	}
	info := classify.Info(cat)
	hits, covered := s.frontier(q, fmt.Sprintf("cat:%d", cat), q.x.CatRecs, uint32(cat), 0.5, total)
	age := q.x.CatAge[cat]
	writeJSON(q.w, http.StatusOK, map[string]any{
		"cat":        info,
		"dsize":      total,
		"count":      count,
		"est_save":   int64(float64(total) * info.SaveFrac),
		"dirs":       hits,
		"covered":    covered,
		"age":        age,
		"age_labels": ageLabels(),
		"files":      s.files(q.sn, q.x, func(f *index.File) bool { return f.Cat == cat }, 50),
	})
}

func classByKey(key string) (classify.Class, bool) {
	for _, c := range classify.AllClasses() {
		if c.Key == key && c.ID != classify.ClassNone {
			return c.ID, true
		}
	}
	return 0, false
}

func (s *Server) handleClass(q *req) {
	cls, ok := classByKey(q.r.PathValue("key"))
	if !ok {
		fail(q.w, http.StatusNotFound, "unknown class %q", q.r.PathValue("key"))
		return
	}
	var total, count int64
	for _, t := range q.sn.Summary.Classes {
		if classify.Class(t.Key) == cls {
			total, count = t.DSize, t.Count
		}
	}
	hits, covered := s.frontier(q, fmt.Sprintf("class:%d", cls), q.x.ClassRecs, uint32(cls), 0.8, total)
	if cls == classify.ClassPublic {
		for i := range hits {
			hits[i].Accession = classify.ExtractAccession(hits[i].Path)
		}
	}
	writeJSON(q.w, http.StatusOK, map[string]any{
		"class":   classify.ClassMeta(cls),
		"dsize":   total,
		"count":   count,
		"dirs":    hits,
		"covered": covered,
		"files":   s.files(q.sn, q.x, func(f *index.File) bool { return f.Class == cls }, 100),
	})
}

// ---------- stale / duplicates ----------

func (s *Server) handleStale(q *req) {
	days, ok := q.intParam("days", 365)
	if !ok {
		return
	}
	root := q.x.Total()
	minSize, ok := q.intParam("min", minHit(root.DSize))
	if !ok {
		return
	}
	cutoff := q.x.Meta.Timestamp - days*86400
	hits, total := q.x.Stale(cutoff, minSize, 300)
	writeJSON(q.w, http.StatusOK, map[string]any{
		"days":   days,
		"min":    minSize,
		"cutoff": cutoff,
		"total":  total,
		"share":  float64(total) / float64(max(root.DSize, 1)),
		"dirs":   s.hits(q.sn, q.x, hits, true),
	})
}

func (s *Server) handleDups(q *req) {
	minSize, ok := q.intParam("min", 100<<20)
	if !ok {
		return
	}
	groups, waste := q.x.Duplicates(minSize, 300)
	type groupJSON struct {
		Name  string     `json:"name"`
		ASize int64      `json:"asize"`
		Waste int64      `json:"waste"`
		Files []fileJSON `json:"files"`
	}
	out := make([]groupJSON, 0, len(groups))
	for _, g := range groups {
		gj := groupJSON{Name: g.Name, ASize: g.ASize, Waste: g.Waste}
		for _, i := range g.Files {
			gj.Files = append(gj.Files, fileInfo(q.sn, q.x, &q.x.Files[i]))
		}
		out = append(out, gj)
	}
	writeJSON(q.w, http.StatusOK, map[string]any{
		"min":       minSize,
		"waste":     waste,
		"groups":    out,
		"files_min": q.x.FilesMin,
	})
}

// ---------- trend ----------

type trendPoint struct {
	ID      string           `json:"id"`
	Time    int64            `json:"time"`
	DSize   int64            `json:"dsize"`
	Files   int64            `json:"files"`
	DF      *store.DF        `json:"df"`
	Owners  map[string]int64 `json:"owners"`
	Classes map[string]int64 `json:"classes"`
}

func (s *Server) handleTrend(w http.ResponseWriter, r *http.Request) {
	series := r.URL.Query().Get("series")
	var pts []trendPoint
	for _, sn := range s.st.List() {
		if sn.Status != store.StatusReady || sn.Series() != series {
			continue
		}
		p := trendPoint{ID: sn.ID, Time: sn.Time().Unix(), DSize: sn.Summary.DSize, Files: sn.Summary.Files,
			DF: sn.DF, Owners: map[string]int64{}, Classes: map[string]int64{}}
		for _, o := range sn.Summary.Owners {
			p.Owners[owner(sn, o.Key).Name] = o.DSize
		}
		for _, c := range sn.Summary.Classes {
			if classify.Class(c.Key) != classify.ClassNone {
				p.Classes[classify.ClassMeta(classify.Class(c.Key)).Key] = c.DSize
			}
		}
		pts = append(pts, p)
	}
	if len(pts) == 0 {
		fail(w, http.StatusNotFound, "no indexed snapshots in series %q", series)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"series":   series,
		"points":   pts,
		"forecast": forecast(pts),
	})
}

type forecastJSON struct {
	Basis       string  `json:"basis"` // "df" or "scan"
	PerDay      float64 `json:"per_day"`
	DaysToFull  float64 `json:"days_to_full"` // -1 if not growing or capacity unknown
	Capacity    int64   `json:"capacity"`
	Points      int     `json:"points"`
	SpanDays    float64 `json:"span_days"`
	Explanation string  `json:"explanation"`
}

// forecast fits a least-squares line to used space over time.
func forecast(pts []trendPoint) *forecastJSON {
	type xy struct{ x, y float64 }
	var data []xy
	basis := "df"
	var capacity, lastUsed int64
	for _, p := range pts {
		if p.DF != nil {
			data = append(data, xy{float64(p.Time), float64(p.DF.Used)})
			capacity, lastUsed = p.DF.Size, p.DF.Used
		}
	}
	if len(data) < 2 {
		basis = "scan"
		data = data[:0]
		for _, p := range pts {
			data = append(data, xy{float64(p.Time), float64(p.DSize)})
			lastUsed = p.DSize
		}
	}
	f := &forecastJSON{Basis: basis, DaysToFull: -1, Capacity: capacity, Points: len(data)}
	if len(data) < 2 {
		f.Explanation = "Для прогноза нужно минимум два снимка."
		return f
	}
	var sx, sy, sxx, sxy float64
	n := float64(len(data))
	for _, d := range data {
		sx += d.x
		sy += d.y
		sxx += d.x * d.x
		sxy += d.x * d.y
	}
	den := n*sxx - sx*sx
	f.SpanDays = (data[len(data)-1].x - data[0].x) / 86400
	if den == 0 || f.SpanDays < 0.01 {
		f.Explanation = "Снимки сделаны почти одновременно, скорость роста не определить."
		return f
	}
	slope := (n*sxy - sx*sy) / den // bytes per second
	f.PerDay = slope * 86400
	switch {
	case capacity == 0:
		f.Explanation = "Ёмкость диска неизвестна (нет .df), считаем только скорость роста просканированного каталога."
	case f.PerDay <= 0:
		f.Explanation = "Занятое место не растёт."
	default:
		f.DaysToFull = float64(capacity-lastUsed) / f.PerDay
		if math.IsInf(f.DaysToFull, 0) || math.IsNaN(f.DaysToFull) {
			f.DaysToFull = -1
		}
		f.Explanation = "Линейная экстраполяция по всем снимкам серии."
	}
	return f
}

// ---------- diff ----------

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	qa, qb := r.URL.Query().Get("a"), r.URL.Query().Get("b")
	sa, xa, ok := s.load(w, qa)
	if !ok {
		return
	}
	sb, xb, ok := s.load(w, qb)
	if !ok {
		return
	}
	if sa.Time().After(sb.Time()) {
		sa, sb, xa, xb = sb, sa, xb, xa
	}
	warns := []warning{}
	if xa.Meta.Root != xb.Meta.Root {
		warns = append(warns, warning{"warn", fmt.Sprintf("Снимки сделаны для разных корней (%s и %s): сопоставление по именам может вводить в заблуждение.", xa.Meta.Root, xb.Meta.Root)})
	}
	key := sa.ID + "\x00" + sb.ID
	s.diffMu.Lock()
	if s.diffKey != key || s.diff == nil || s.diff.A != xa || s.diff.B != xb {
		s.diff = index.NewDiff(xa, xb)
		s.diffKey = key
	}
	d := s.diff
	s.diffMu.Unlock()

	path := r.URL.Query().Get("path")
	var ai, bi int32 = -1, -1
	if id, ok := xb.Lookup(path); ok {
		bi, ai = id, d.BtoA[id]
	} else if id, ok := xa.Lookup(path); ok {
		ai = id
	} else {
		fail(w, http.StatusNotFound, "directory %q is in neither snapshot", path)
		return
	}
	total := xb.Total().DSize - xa.Total().DSize
	minDelta := max(1<<20, abs64(total)/200, xb.Total().DSize/5000)
	origins := d.Origins(minDelta, 200)
	head := change(d, ai, bi)
	writeJSON(w, http.StatusOK, map[string]any{
		"a":         snapInfo(sa),
		"b":         snapInfo(sb),
		"total":     total,
		"min_delta": minDelta,
		"origins":   origins,
		"dir":       head,
		"children":  d.Children(ai, bi),
		"warnings":  warns,
	})
}

func change(d *index.Diff, ai, bi int32) index.Change {
	c := index.Change{AID: ai, BID: bi}
	if ai >= 0 {
		c.ASize, c.AFiles, c.Path = d.A.Dirs[ai].DSize, d.A.Dirs[ai].Files, d.A.Path(ai)
	}
	if bi >= 0 {
		c.BSize, c.BFiles, c.Path = d.B.Dirs[bi].DSize, d.B.Dirs[bi].Files, d.B.Path(bi)
	}
	c.Delta = c.BSize - c.ASize
	return c
}

func human(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d Б", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	s := strings.Replace(fmt.Sprintf("%.1f", float64(b)/float64(div)), ".", ",", 1) // Russian decimal comma, as in the UI
	return s + " " + []string{"КБ", "МБ", "ГБ", "ТБ", "ПБ", "ЭБ"}[exp]
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
