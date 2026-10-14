package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// readSidecars loads the optional .df/.passwd/.group files. A missing sidecar
// is fine; an unreadable or malformed one is recorded in SidecarErrors so the
// UI can show it.
func (s *Store) readSidecars(sn *Snapshot) {
	base := strings.TrimSuffix(sn.Source, ".part")
	for _, suf := range exportSuffixes {
		if strings.HasSuffix(base, suf) {
			base = strings.TrimSuffix(base, suf)
			break
		}
	}
	read := func(ext string) (string, bool) {
		b, err := os.ReadFile(base + ext)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				sn.SidecarErrors = append(sn.SidecarErrors, fmt.Sprintf("%s: %v", ext, err))
			}
			return "", false
		}
		return string(b), true
	}
	if txt, ok := read(".df"); ok {
		df, err := ParseDF(txt)
		if err != nil {
			sn.SidecarErrors = append(sn.SidecarErrors, ".df: "+err.Error())
		} else {
			sn.DF = df
		}
	}
	if txt, ok := read(".passwd"); ok {
		m, errs := ParseIDFile(txt, 2)
		sn.Users = m
		for _, e := range errs {
			sn.SidecarErrors = append(sn.SidecarErrors, ".passwd: "+e)
		}
	}
	if txt, ok := read(".group"); ok {
		m, errs := ParseIDFile(txt, 2)
		sn.Groups = m
		for _, e := range errs {
			sn.SidecarErrors = append(sn.SidecarErrors, ".group: "+e)
		}
	}
}

// ParseDF parses POSIX `df -B1 -P <path>` output (header + one line).
func ParseDF(txt string) (*DF, error) {
	lines := strings.Split(strings.TrimSpace(txt), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("expected header and one data line, got %d lines", len(lines))
	}
	// A long device name can wrap onto its own line in non-POSIX output;
	// join everything after the header.
	fields := strings.Fields(strings.Join(lines[1:], " "))
	if len(fields) < 6 {
		return nil, fmt.Errorf("expected 6 columns, got %d: %q", len(fields), lines[1])
	}
	nums := make([]int64, 3)
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseInt(fields[1+i], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("column %d is not a byte count (%q) — was df run with -B1?", i+2, fields[1+i])
		}
		nums[i] = v
	}
	return &DF{
		Filesystem: fields[0], Size: nums[0], Used: nums[1], Avail: nums[2],
		Mount: strings.Join(fields[5:], " "),
	}, nil
}

// ParseIDFile parses passwd/group style files: name:x:id:... -> id -> name.
// idField is the 0-based column of the numeric id.
func ParseIDFile(txt string, idField int) (map[uint32]string, []string) {
	m := map[uint32]string{}
	var errs []string
	for n, line := range strings.Split(txt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) <= idField {
			errs = append(errs, fmt.Sprintf("line %d: malformed %q", n+1, line))
			continue
		}
		id, err := strconv.ParseUint(f[idField], 10, 32)
		if err != nil {
			errs = append(errs, fmt.Sprintf("line %d: bad id %q", n+1, f[idField]))
			continue
		}
		if _, dup := m[uint32(id)]; !dup {
			m[uint32(id)] = f[0]
		}
	}
	if len(errs) > 5 {
		errs = append(errs[:5], fmt.Sprintf("… and %d more", len(errs)-5))
	}
	return m, errs
}
