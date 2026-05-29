package msidb

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/abemedia/go-cfb"
)

// spillThreshold is the byte count above which a streamWriter switches from
// an in-memory buffer to a temp file.
const spillThreshold = 4 << 20 // 4 MiB

// streamSource is one named stream's storage backend.
type streamSource interface {
	open() io.ReadSeeker
	close() error
}

// cfbStreamSource serves a stream from the source CFB.
type cfbStreamSource struct{ s *cfb.Stream }

func (c *cfbStreamSource) open() io.ReadSeeker { return c.s.Open() }
func (c *cfbStreamSource) close() error        { return nil }

// memStreamSource serves a stream from bytes held in memory.
type memStreamSource struct{ b []byte }

func (m *memStreamSource) open() io.ReadSeeker { return bytes.NewReader(m.b) }
func (m *memStreamSource) close() error        { m.b = nil; return nil }

// fileStreamSource serves a stream from a temporary file.
type fileStreamSource struct {
	f    *os.File
	size int64
}

func (f *fileStreamSource) open() io.ReadSeeker { return io.NewSectionReader(f.f, 0, f.size) }
func (f *fileStreamSource) close() error        { return f.f.Close() }

// streamWriter accumulates a binary record field's bytes.
type streamWriter struct {
	db   *Database
	buf  bytes.Buffer
	file *os.File
	path string
	n    int64
	err  error
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.file == nil && w.n+int64(len(p)) > spillThreshold { //nolint:nestif
		if err := w.db.ensureStageDir(); err != nil {
			w.err = err
			return 0, err
		}
		f, err := os.CreateTemp(w.db.stageDir, "s-*")
		if err != nil {
			w.err = err
			return 0, err
		}
		if w.buf.Len() > 0 {
			if _, err := f.Write(w.buf.Bytes()); err != nil {
				f.Close()
				os.Remove(f.Name())
				w.err = err
				return 0, err
			}
			w.buf.Reset()
		}
		w.file = f
		w.path = f.Name()
	}
	if w.file != nil {
		n, err := w.file.Write(p)
		w.n += int64(n)
		if err != nil {
			w.file.Close()
			os.Remove(w.path)
			w.file = nil
			w.path = ""
			w.err = err
		}
		return n, err
	}
	n, _ := w.buf.Write(p)
	w.n += int64(n)
	return n, nil
}

// build finalises w and returns the streamSource it produced.
func (w *streamWriter) build() (streamSource, error) {
	if w.err != nil {
		return nil, w.err
	}
	if w.file != nil {
		return &fileStreamSource{f: w.file, size: w.n}, nil
	}
	return &memStreamSource{b: w.buf.Bytes()}, nil
}

// ensureStageDir lazily creates the database's staging directory.
func (db *Database) ensureStageDir() error {
	if db.stageDir != "" {
		return nil
	}
	dir, err := os.MkdirTemp("", "msidb-stream-*")
	if err != nil {
		return fmt.Errorf("stage dir: %w", err)
	}
	db.stageDir = dir
	db.cleanup = runtime.AddCleanup(db, func(d string) { _ = os.RemoveAll(d) }, dir)
	return nil
}
