package msidb

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/blobstore"
	"github.com/abemedia/go-msi/internal/streamname"
)

type streamSource interface {
	open() io.ReadSeeker
	release()
}

type cfbStreamSource struct{ s *cfb.Stream }

func (c *cfbStreamSource) open() io.ReadSeeker { return c.s.Open() }
func (c *cfbStreamSource) release()            {}

type blobStreamSource struct {
	store  *blobstore.Store
	handle blobstore.Handle
	refs   int
}

func (b *blobStreamSource) open() io.ReadSeeker {
	rs, err := b.store.Open(b.handle)
	if err != nil {
		return errReader{err: err}
	}
	return rs
}

func (b *blobStreamSource) release() {
	if b.refs--; b.refs == 0 {
		b.store.Delete(b.handle)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error)       { return 0, r.err }
func (r errReader) Seek(int64, int) (int64, error) { return 0, r.err }

func (db *Database) addStream(name string, src streamSource) {
	if id, ok := db.pool.LookupID(name); ok {
		if old, ok := db.sources[id]; ok {
			old.release()
			db.sources[id] = src
			return
		}
	}
	id := db.pool.Intern(name, false)
	db.sources[id] = src
	st := db.tables[systemTableStreams]
	rec := []uint32{id, 1}
	if i, found := st.find(rec); !found {
		st.records = slices.Insert(st.records, i, rec)
	}
}

func (db *Database) dropStream(name string) {
	id, ok := db.pool.LookupID(name)
	if !ok {
		return
	}
	src, ok := db.sources[id]
	if !ok {
		return
	}
	src.release()
	delete(db.sources, id)
	st := db.tables[systemTableStreams]
	if i, found := st.find([]uint32{id, 0}); found {
		clear(st.records[i])
		st.records = slices.Delete(st.records, i, i+1)
	}
	db.pool.Release(id, false)
}

// openStream returns a reader over the named stream, or nil if there is none.
func (db *Database) openStream(name string) io.ReadSeeker {
	id, ok := db.pool.LookupID(name)
	if !ok {
		return nil
	}
	src, ok := db.sources[id]
	if !ok {
		return nil
	}
	return src.open()
}

// streamKey returns the name of rec's stream, or "" if none can be derived.
func (db *Database) streamKey(t *table, rec []uint32) string {
	if t.name == systemTableStreams {
		s, _ := db.pool.Lookup(rec[0])
		return s
	}
	if len(t.keys) == 0 {
		return ""
	}
	const maxStreamName = 62
	b := make([]byte, 0, maxStreamName)
	b = append(b, t.name...)
	for _, i := range t.keys {
		c := t.cols[i]
		b = append(b, '.')
		fv := rec[i]
		if fv == 0 {
			continue
		}
		switch c.Type {
		case ColumnString:
			s, ok := db.pool.Lookup(fv)
			if !ok {
				return ""
			}
			b = append(b, s...)
		case ColumnInteger:
			b = strconv.AppendInt(b, int64(decodeInt(fv, c.Size)), 10)
		default:
			return ""
		}
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func (db *Database) stage(r io.Reader) (*blobStreamSource, error) {
	h, w, err := db.blob.Create()
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		db.blob.Delete(h)
		return nil, err
	}
	if err := w.Close(); err != nil {
		db.blob.Delete(h)
		return nil, err
	}
	return &blobStreamSource{store: &db.blob, handle: h, refs: 1}, nil
}

// checkStreamName returns an error if name cannot be stored as a stream and read back unchanged.
func checkStreamName(name string) error {
	if strings.HasPrefix(name, tableMarker) {
		return fmt.Errorf("stream name %q starts with the table marker", name)
	}
	if i := strings.IndexAny(name, cfbForbidden); i >= 0 {
		return fmt.Errorf("stream name %q contains invalid character %q", name, name[i])
	}
	if i := strings.IndexFunc(name, streamname.Reserved); i >= 0 {
		r, _ := utf8.DecodeRuneInString(name[i:])
		return fmt.Errorf("stream name %q contains reserved character %U", name, r)
	}
	var n int
	if strings.HasPrefix(name, "\x05") {
		for _, r := range name {
			n += utf16.RuneLen(r)
		}
	} else {
		n = streamname.EncodedLen(name)
	}
	if n > 31 {
		return fmt.Errorf("stream name %q is %d wchars, max 31", name, n)
	}
	return nil
}
