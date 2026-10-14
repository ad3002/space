// Package ncdu implements a streaming parser for the ncdu JSON export format
// (https://dev.yorhel.nl/ncdu/jsonfmt). The parser never holds the tree in
// memory: it reports directories and files to a Handler as it reads, so exports
// with tens of millions of entries can be processed in bounded memory.
package ncdu

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

// Meta is the header object of an export.
type Meta struct {
	ProgName  string
	ProgVer   string
	Timestamp int64
	Major     int
	Minor     int
}

// Entry is one file or directory record. Name is only valid until the handler
// call returns (the underlying buffer is reused); copy it if you keep it.
type Entry struct {
	Name      []byte
	ASize     int64
	DSize     int64
	Dev       uint64
	Ino       uint64
	Nlink     int64
	UID       uint32
	GID       uint32
	Mode      uint32
	Mtime     int64
	HasUID    bool
	HasMtime  bool
	HardLink  bool // "hlnkc": file has more than one hard link
	NotReg    bool // not a regular file (symlink, socket, ...)
	ReadError bool
	Excluded  string // "pattern", "otherfs", "kernfs", "frmlnk" or ""
}

// Handler receives parse events in depth-first order.
type Handler interface {
	Meta(m Meta)
	// EnterDir is called for a directory; its children follow, then LeaveDir.
	EnterDir(e *Entry)
	LeaveDir()
	File(e *Entry)
}

// ParseError carries the byte offset where the input stopped making sense.
type ParseError struct {
	Offset int64
	Msg    string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("ncdu export: offset %d: %s", e.Offset, e.Msg)
}

type parser struct {
	r   *bufio.Reader
	off int64
	h   Handler
	buf []byte // scratch for strings
	e   Entry
}

// Parse reads a complete ncdu export from r and reports it to h.
// A truncated or malformed export returns a *ParseError; events already
// delivered to h stay delivered, so callers must treat the result as invalid.
func Parse(r io.Reader, h Handler) error {
	p := &parser{r: bufio.NewReaderSize(r, 1<<20), h: h, buf: make([]byte, 0, 256)}
	return p.parseTop()
}

func (p *parser) fail(format string, args ...any) error {
	return &ParseError{Offset: p.off, Msg: fmt.Sprintf(format, args...)}
}

func (p *parser) readByte() (byte, error) {
	c, err := p.r.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0, p.fail("unexpected end of input (export truncated? was the scan interrupted?)")
		}
		return 0, &ParseError{Offset: p.off, Msg: "read: " + err.Error()}
	}
	p.off++
	return c, nil
}

func (p *parser) unread() {
	_ = p.r.UnreadByte()
	p.off--
}

// next returns the next non-whitespace byte.
func (p *parser) next() (byte, error) {
	for {
		c, err := p.readByte()
		if err != nil {
			return 0, err
		}
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c, nil
	}
}

func (p *parser) expect(want byte) error {
	c, err := p.next()
	if err != nil {
		return err
	}
	if c != want {
		return p.fail("expected %q, got %q", want, c)
	}
	return nil
}

func (p *parser) parseTop() error {
	if err := p.expect('['); err != nil {
		return err
	}
	major, err := p.parseInt()
	if err != nil {
		return err
	}
	if major != 1 {
		return p.fail("unsupported export major version %d (only 1 is supported)", major)
	}
	if err := p.expect(','); err != nil {
		return err
	}
	minor, err := p.parseInt()
	if err != nil {
		return err
	}
	if err := p.expect(','); err != nil {
		return err
	}
	meta := Meta{Major: int(major), Minor: int(minor)}
	if err := p.parseMeta(&meta); err != nil {
		return err
	}
	p.h.Meta(meta)
	if err := p.expect(','); err != nil {
		return err
	}
	if err := p.expect('['); err != nil {
		return err
	}
	if err := p.parseDir(); err != nil {
		return err
	}
	// Anything after the root directory up to the closing bracket is ignored
	// by ncdu itself; we only require the final ']'.
	c, err := p.next()
	if err != nil {
		return err
	}
	if c == ',' {
		return p.fail("unexpected second root item")
	}
	if c != ']' {
		return p.fail("expected ']' after root directory, got %q", c)
	}
	return nil
}

func (p *parser) parseMeta(m *Meta) error {
	if err := p.expect('{'); err != nil {
		return err
	}
	c, err := p.next()
	if err != nil {
		return err
	}
	if c == '}' {
		return nil
	}
	p.unread()
	for {
		key, err := p.parseKey()
		if err != nil {
			return err
		}
		switch key {
		case "progname", "progver":
			s, err := p.parseString()
			if err != nil {
				return err
			}
			if key == "progname" {
				m.ProgName = string(s)
			} else {
				m.ProgVer = string(s)
			}
		case "timestamp":
			v, err := p.parseInt()
			if err != nil {
				return err
			}
			m.Timestamp = v
		default:
			if err := p.skipValue(); err != nil {
				return err
			}
		}
		c, err := p.next()
		if err != nil {
			return err
		}
		if c == '}' {
			return nil
		}
		if c != ',' {
			return p.fail("expected ',' or '}' in metadata, got %q", c)
		}
	}
}

// parseDir parses a directory array whose '[' was already consumed.
func (p *parser) parseDir() error {
	if err := p.parseEntry(); err != nil {
		return err
	}
	p.h.EnterDir(&p.e)
	for {
		c, err := p.next()
		if err != nil {
			return err
		}
		if c == ']' {
			p.h.LeaveDir()
			return nil
		}
		if c != ',' {
			return p.fail("expected ',' or ']' in directory, got %q", c)
		}
		c, err = p.next()
		if err != nil {
			return err
		}
		switch c {
		case '[':
			if err := p.parseDir(); err != nil {
				return err
			}
		case '{':
			p.unread()
			if err := p.parseEntry(); err != nil {
				return err
			}
			p.h.File(&p.e)
		default:
			return p.fail("expected '{' or '[' for directory item, got %q", c)
		}
	}
}

func (p *parser) parseEntry() error {
	if err := p.expect('{'); err != nil {
		return err
	}
	p.e = Entry{}
	p.buf = p.buf[:0]
	nameSet := false
	c, err := p.next()
	if err != nil {
		return err
	}
	if c != '}' {
		p.unread()
		for {
			key, err := p.parseKey()
			if err != nil {
				return err
			}
			switch key {
			case "name":
				s, err := p.parseString()
				if err != nil {
					return err
				}
				// parseString returns p.buf; keep it as the entry name.
				p.e.Name = s
				nameSet = true
			case "asize":
				if p.e.ASize, err = p.parseInt(); err != nil {
					return err
				}
			case "dsize":
				if p.e.DSize, err = p.parseInt(); err != nil {
					return err
				}
			case "dev":
				v, err := p.parseUint()
				if err != nil {
					return err
				}
				p.e.Dev = v
			case "ino":
				v, err := p.parseUint()
				if err != nil {
					return err
				}
				p.e.Ino = v
			case "nlink":
				if p.e.Nlink, err = p.parseInt(); err != nil {
					return err
				}
			case "uid":
				v, err := p.parseUint()
				if err != nil {
					return err
				}
				p.e.UID = uint32(v)
				p.e.HasUID = true
			case "gid":
				v, err := p.parseUint()
				if err != nil {
					return err
				}
				p.e.GID = uint32(v)
			case "mode":
				v, err := p.parseUint()
				if err != nil {
					return err
				}
				p.e.Mode = uint32(v)
			case "mtime":
				if p.e.Mtime, err = p.parseInt(); err != nil {
					return err
				}
				p.e.HasMtime = true
			case "hlnkc":
				if p.e.HardLink, err = p.parseBool(); err != nil {
					return err
				}
			case "notreg":
				if p.e.NotReg, err = p.parseBool(); err != nil {
					return err
				}
			case "read_error":
				if p.e.ReadError, err = p.parseBool(); err != nil {
					return err
				}
			case "excluded":
				s, err := p.parseStringScratch()
				if err != nil {
					return err
				}
				p.e.Excluded = string(s)
			default:
				if err := p.skipValue(); err != nil {
					return err
				}
			}
			c, err := p.next()
			if err != nil {
				return err
			}
			if c == '}' {
				break
			}
			if c != ',' {
				return p.fail("expected ',' or '}' in entry, got %q", c)
			}
		}
	}
	if !nameSet {
		return p.fail("entry without \"name\"")
	}
	return nil
}

// parseKey reads `"key":` and returns the key. Keys are short ASCII.
func (p *parser) parseKey() (string, error) {
	if err := p.expect('"'); err != nil {
		return "", err
	}
	var kb [16]byte
	k := kb[:0]
	for {
		c, err := p.readByte()
		if err != nil {
			return "", err
		}
		if c == '"' {
			break
		}
		if c == '\\' {
			return "", p.fail("escape sequences in keys are not supported")
		}
		k = append(k, c)
	}
	if err := p.expect(':'); err != nil {
		return "", err
	}
	return string(k), nil
}

// parseString parses a JSON string into p.buf (appending after any bytes
// already there is not needed: entries have one name). Returned slice aliases p.buf.
func (p *parser) parseString() ([]byte, error) {
	if err := p.expect('"'); err != nil {
		return nil, err
	}
	p.buf = p.buf[:0]
	b, err := p.readStringBody(p.buf)
	p.buf = b
	return b, err
}

// parseStringScratch parses a string into a temporary buffer that does not
// clobber the entry name.
func (p *parser) parseStringScratch() ([]byte, error) {
	if err := p.expect('"'); err != nil {
		return nil, err
	}
	return p.readStringBody(make([]byte, 0, 16))
}

func (p *parser) readStringBody(b []byte) ([]byte, error) {
	for {
		// Fast path: copy a run of plain bytes straight from the buffer.
		chunk, _ := p.r.Peek(p.r.Buffered())
		n := 0
		for n < len(chunk) && chunk[n] != '"' && chunk[n] != '\\' {
			n++
		}
		if n > 0 {
			b = append(b, chunk[:n]...)
			_, _ = p.r.Discard(n)
			p.off += int64(n)
		}
		c, err := p.readByte()
		if err != nil {
			return b, err
		}
		switch c {
		case '"':
			return b, nil
		case '\\':
			e, err := p.readByte()
			if err != nil {
				return b, err
			}
			switch e {
			case '"', '\\', '/':
				b = append(b, e)
			case 'b':
				b = append(b, '\b')
			case 'f':
				b = append(b, '\f')
			case 'n':
				b = append(b, '\n')
			case 'r':
				b = append(b, '\r')
			case 't':
				b = append(b, '\t')
			case 'u':
				r, err := p.readHex4()
				if err != nil {
					return b, err
				}
				if r >= 0xD800 && r < 0xDC00 {
					// High surrogate: must be followed by \uDC00-\uDFFF.
					c1, err := p.readByte()
					if err != nil {
						return b, err
					}
					c2, err := p.readByte()
					if err != nil {
						return b, err
					}
					if c1 != '\\' || c2 != 'u' {
						return b, p.fail("unpaired UTF-16 surrogate in string")
					}
					lo, err := p.readHex4()
					if err != nil {
						return b, err
					}
					if lo < 0xDC00 || lo > 0xDFFF {
						return b, p.fail("invalid UTF-16 low surrogate")
					}
					r = 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00)
				}
				b = utf8.AppendRune(b, r)
			default:
				return b, p.fail("invalid escape \\%c", e)
			}
		default:
			b = append(b, c)
		}
	}
}

func (p *parser) readHex4() (rune, error) {
	var r rune
	for i := 0; i < 4; i++ {
		c, err := p.readByte()
		if err != nil {
			return 0, err
		}
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			return 0, p.fail("invalid hex digit %q in \\u escape", c)
		}
		r = r<<4 | rune(v)
	}
	return r, nil
}

func (p *parser) parseNumberBytes() ([]byte, error) {
	var nb [32]byte
	n := nb[:0]
	c, err := p.next()
	if err != nil {
		return nil, err
	}
	for {
		if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			n = append(n, c)
		} else {
			p.unread()
			break
		}
		c, err = p.readByte()
		if err != nil {
			return nil, err
		}
	}
	if len(n) == 0 {
		return nil, p.fail("expected number, got %q", c)
	}
	return n, nil
}

func (p *parser) parseInt() (int64, error) {
	n, err := p.parseNumberBytes()
	if err != nil {
		return 0, err
	}
	v, perr := strconv.ParseInt(string(n), 10, 64)
	if perr != nil {
		// ncdu writes integers; tolerate floats by truncation, never silently
		// accept garbage.
		f, ferr := strconv.ParseFloat(string(n), 64)
		if ferr != nil {
			return 0, p.fail("invalid number %q", n)
		}
		return int64(f), nil
	}
	return v, nil
}

func (p *parser) parseUint() (uint64, error) {
	n, err := p.parseNumberBytes()
	if err != nil {
		return 0, err
	}
	v, perr := strconv.ParseUint(string(n), 10, 64)
	if perr != nil {
		return 0, p.fail("invalid unsigned number %q", n)
	}
	return v, nil
}

func (p *parser) parseBool() (bool, error) {
	c, err := p.next()
	if err != nil {
		return false, err
	}
	var rest string
	var v bool
	switch c {
	case 't':
		rest, v = "rue", true
	case 'f':
		rest, v = "alse", false
	case 'n':
		rest, v = "ull", false
	default:
		return false, p.fail("expected boolean, got %q", c)
	}
	for i := 0; i < len(rest); i++ {
		c, err := p.readByte()
		if err != nil {
			return false, err
		}
		if c != rest[i] {
			return false, p.fail("invalid literal")
		}
	}
	return v, nil
}

// skipValue skips any JSON value (used for keys this parser doesn't know,
// e.g. fields added by newer ncdu versions).
func (p *parser) skipValue() error {
	c, err := p.next()
	if err != nil {
		return err
	}
	switch {
	case c == '"':
		_, err := p.readStringBody(make([]byte, 0, 16))
		return err
	case c == '{' || c == '[':
		depth := 1
		for depth > 0 {
			c, err := p.readByte()
			if err != nil {
				return err
			}
			switch c {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			case '"':
				if _, err := p.readStringBody(make([]byte, 0, 16)); err != nil {
					return err
				}
			}
		}
		return nil
	case c == 't' || c == 'f' || c == 'n':
		p.unread()
		_, err := p.parseBool()
		return err
	default:
		p.unread()
		_, err := p.parseNumberBytes()
		return err
	}
}
