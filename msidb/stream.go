package msidb

import (
	"io"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/blobstore"
)

// streamSource is one named stream's storage backend.
type streamSource interface {
	open() io.ReadSeeker
	delete()
}

// cfbStreamSource serves a stream from the source CFB.
type cfbStreamSource struct{ s *cfb.Stream }

func (c *cfbStreamSource) open() io.ReadSeeker { return c.s.Open() }
func (c *cfbStreamSource) delete()             {}

// blobStreamSource serves a stream from a blob in the staging store.
type blobStreamSource struct {
	store  *blobstore.Store
	handle blobstore.Handle
}

func (b *blobStreamSource) open() io.ReadSeeker {
	rs, err := b.store.Open(b.handle)
	if err != nil {
		return errReader{err: err}
	}
	return rs
}

func (b *blobStreamSource) delete() { b.store.Delete(b.handle) }

// errReader is a [io.ReadSeeker] whose every call returns err.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error)       { return 0, r.err }
func (r errReader) Seek(int64, int) (int64, error) { return 0, r.err }
