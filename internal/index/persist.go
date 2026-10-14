package index

import (
	"bufio"
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"io"
)

// Save writes the index as gzip-compressed gob.
func Save(w io.Writer, x *Index) error {
	bw := bufio.NewWriterSize(w, 1<<20)
	zw, err := gzip.NewWriterLevel(bw, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(zw).Encode(x); err != nil {
		return fmt.Errorf("encode index: %w", err)
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return bw.Flush()
}

// ErrStaleFormat means the cached index was written by another FormatVersion.
type ErrStaleFormat struct{ Got int }

func (e ErrStaleFormat) Error() string {
	return fmt.Sprintf("index format version %d, want %d", e.Got, FormatVersion)
}

// Load reads an index written by Save.
func Load(r io.Reader) (*Index, error) {
	zr, err := gzip.NewReader(bufio.NewReaderSize(r, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	defer zr.Close()
	var x Index
	if err := gob.NewDecoder(zr).Decode(&x); err != nil {
		return nil, fmt.Errorf("decode index: %w", err)
	}
	if x.Version != FormatVersion {
		return nil, ErrStaleFormat{Got: x.Version}
	}
	if len(x.Dirs) == 0 {
		return nil, fmt.Errorf("decode index: no directories")
	}
	if x.OwnerAge == nil {
		x.OwnerAge = map[uint32]*[NumAge]int64{}
	}
	return &x, nil
}
