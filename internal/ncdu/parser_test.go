package ncdu

import (
	"errors"
	"strings"
	"testing"
)

type event struct {
	kind string // enter, leave, file
	name string
	e    Entry
}

type recorder struct {
	meta   Meta
	events []event
}

func (r *recorder) Meta(m Meta) { r.meta = m }
func (r *recorder) EnterDir(e *Entry) {
	c := *e
	r.events = append(r.events, event{"enter", string(e.Name), c})
}
func (r *recorder) LeaveDir() { r.events = append(r.events, event{kind: "leave"}) }
func (r *recorder) File(e *Entry) {
	c := *e
	r.events = append(r.events, event{"file", string(e.Name), c})
}

// Real ncdu 1.15.1 `-e -o -` output (paths shortened), including a symlink,
// a read error, hard links, a quoted name and a non-ASCII name.
const sample = `[1,1,{"progname":"ncdu","progver":"1.15.1","timestamp":1789242839},
[{"name":"/data/sample","asize":4096,"dsize":4096,"dev":66307,"ino":16785568,"uid":1013,"gid":1013,"mode":16893,"mtime":1789242839},
{"name":"deadlink","asize":9,"ino":16785583,"uid":1013,"gid":1013,"mode":41471,"mtime":1789242839,"notreg":true},
[{"name":"noread","asize":4096,"dsize":4096,"ino":16785584,"uid":1013,"gid":1013,"mode":16384,"mtime":1789242839,"read_error":true}],
[{"name":"SRR1234567","asize":4096,"dsize":4096,"ino":16785569,"uid":1013,"gid":1013,"mode":16893,"mtime":1789242839},
{"name":"SRR1234567_1.fastq","asize":5000,"dsize":8192,"ino":16785579,"uid":1013,"gid":1013,"mode":33204,"mtime":1789242839,"hlnkc":true},
[{"name":"sub","asize":4096,"dsize":4096,"ino":16785570,"uid":1013,"gid":1013,"mode":16893,"mtime":1789242839}]],
[{"name":"we ird\"q","asize":4096,"dsize":4096,"ino":16785573,"uid":1013,"gid":1013,"mode":16893,"mtime":1789242839},
{"name":"файл.txt","asize":3,"dsize":4096,"ino":16785580,"uid":1013,"gid":1013,"mode":33204,"mtime":1789242839}],
{"name":"hard.fastq","asize":5000,"dsize":8192,"ino":16785579,"uid":1013,"gid":1013,"mode":33204,"mtime":1789242839,"hlnkc":true}]]
`

func TestParseSample(t *testing.T) {
	var r recorder
	if err := Parse(strings.NewReader(sample), &r); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.meta.ProgVer != "1.15.1" || r.meta.Timestamp != 1789242839 || r.meta.Major != 1 || r.meta.Minor != 1 {
		t.Fatalf("meta = %+v", r.meta)
	}
	var shape []string
	for _, ev := range r.events {
		if ev.kind == "leave" {
			shape = append(shape, ")")
			continue
		}
		if ev.kind == "enter" {
			shape = append(shape, "("+ev.name)
		} else {
			shape = append(shape, ev.name)
		}
	}
	got := strings.Join(shape, " ")
	want := `(/data/sample deadlink (noread ) (SRR1234567 SRR1234567_1.fastq (sub ) ) (we ird"q файл.txt ) hard.fastq )`
	if got != want {
		t.Fatalf("tree shape:\n got  %s\n want %s", got, want)
	}

	byName := map[string]Entry{}
	for _, ev := range r.events {
		if ev.kind != "leave" {
			byName[ev.name] = ev.e
		}
	}
	if e := byName["deadlink"]; !e.NotReg || e.DSize != 0 || e.ASize != 9 {
		t.Errorf("deadlink = %+v", e)
	}
	if e := byName["noread"]; !e.ReadError {
		t.Errorf("noread should carry read_error: %+v", e)
	}
	if e := byName["hard.fastq"]; !e.HardLink || e.Ino != 16785579 || e.DSize != 8192 || e.UID != 1013 || !e.HasUID {
		t.Errorf("hard.fastq = %+v", e)
	}
	if e := byName["/data/sample"]; e.Dev != 66307 || e.Mode != 16893 || e.Mtime != 1789242839 {
		t.Errorf("root = %+v", e)
	}
}

func TestParseEscapesAndUnknownKeys(t *testing.T) {
	in := `[1,2,{"progname":"ncdu","progver":"2.4","timestamp":5,"extra":{"a":[1,2,"]"]}},
[{"name":"/r","asize":1,"future":[{"x":"}"}]},
{"name":"tab\thereé😀\\\/","asize":7,"dsize":4096,"nlink":2,"excluded":"pattern"}]]`
	var r recorder
	if err := Parse(strings.NewReader(in), &r); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(r.events) != 3 {
		t.Fatalf("events = %d, want 3", len(r.events))
	}
	f := r.events[1]
	if f.name != "tab\thereé\U0001F600\\/" {
		t.Errorf("name = %q", f.name)
	}
	if f.e.Excluded != "pattern" || f.e.Nlink != 2 || f.e.ASize != 7 {
		t.Errorf("entry = %+v", f.e)
	}
	if r.meta.Minor != 2 || r.meta.ProgVer != "2.4" {
		t.Errorf("meta = %+v", r.meta)
	}
}

func TestParseTruncatedIsError(t *testing.T) {
	cut := sample[:len(sample)/2]
	var r recorder
	err := Parse(strings.NewReader(cut), &r)
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParseError, got %v", err)
	}
	if !strings.Contains(pe.Msg, "truncated") {
		t.Errorf("message should explain truncation, got %q", pe.Msg)
	}
	if pe.Offset <= 0 {
		t.Errorf("offset = %d", pe.Offset)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	cases := map[string]string{
		"not json":      `hello`,
		"wrong major":   `[2,0,{},[{"name":"/"}]]`,
		"no name":       `[1,0,{},[{"asize":1}]]`,
		"bad escape":    `[1,0,{},[{"name":"\q"}]]`,
		"bad separator": `[1,0,{},[{"name":"/"};{"name":"x"}]]`,
	}
	for name, in := range cases {
		var r recorder
		if err := Parse(strings.NewReader(in), &r); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
