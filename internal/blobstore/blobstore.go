// Package blobstore stages binary blobs to a single temp file. Blobs are
// addressed by opaque handles; deleted handles return their extents to a
// free list for reuse. The backing file only grows.
package blobstore

import (
	"errors"
	"io"
	"os"
	"runtime"
	"sync"
)

// Handle identifies a staged blob.
type Handle uint32

// Store is a session-only flat blob store backed by one temp file. The
// zero value is ready to use; the backing file is created lazily on the
// first [Store.Create] call.
type Store struct {
	once sync.Once

	mu      sync.Mutex
	err     error // nil until init fails or Close runs; then the store is unusable
	file    *os.File
	chains  map[Handle][]extent
	free    []extent
	fileEnd int64
	nextH   Handle
	cleanup runtime.Cleanup
}

type extent struct {
	offset, length int64
}

var (
	errClosed   = errors.New("closed")
	errNotFound = errors.New("not found")
)

// init creates the backing file and registers the GC cleanup hook on the
// first call.
func (s *Store) init() {
	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.err != nil {
			return // closed before first use
		}
		s.file, s.err = os.CreateTemp("", "msidb-blob-*")
		if s.err != nil {
			return
		}
		s.chains = make(map[Handle][]extent)
		s.cleanup = runtime.AddCleanup(s, func(f *os.File) { f.Close(); os.Remove(f.Name()) }, s.file)
	})
}

// Close closes and removes the backing file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil // already closed, or init failed with nothing to clean up
	}
	s.err = errClosed
	if s.file == nil {
		return nil
	}
	s.cleanup.Stop()
	err := s.file.Close()
	if rmErr := os.Remove(s.file.Name()); err == nil {
		err = rmErr
	}
	return err
}

// Create reserves a handle and returns a writer for staging a new blob.
// The returned handle becomes readable only after the writer is closed.
func (s *Store) Create() (Handle, io.WriteCloser, error) {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, nil, s.err
	}
	h := s.nextH
	s.nextH++
	return h, &writer{s: s, h: h}, nil
}

// Open returns a reader over the blob identified by h. The returned
// reader stays valid until h is deleted or the store is closed.
func (s *Store) Open(h Handle) (io.ReadSeeker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	chain, ok := s.chains[h]
	if !ok {
		return nil, errNotFound
	}
	return newBlobReader(s.file, chain), nil
}

// Delete drops the blob identified by h and returns its extents to the
// free list for reuse. Unknown handles are silently ignored.
func (s *Store) Delete(h Handle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chain, ok := s.chains[h]
	if !ok {
		return
	}
	delete(s.chains, h)
	s.free = append(s.free, chain...)
}

type writer struct {
	s       *Store
	h       Handle
	chain   []extent
	cur     extent
	curLen  int64
	growing bool
	closed  bool
	err     error // last Write outcome; if non-nil at Close the blob is discarded
}

func (w *writer) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	if w.closed || w.s.err != nil {
		return 0, errClosed // init succeeded, so w.s.err can only be errClosed
	}
	written := 0
	for len(p) > 0 {
		// Need a fresh extent if cur is full (reused) or no longer at the tail (growing).
		full := w.curLen >= w.cur.length
		if w.growing {
			full = w.cur.offset+w.cur.length != w.s.fileEnd
		}
		if full {
			// Commit the filled portion of the open extent; return any unused tail.
			if w.curLen > 0 {
				w.chain = append(w.chain, extent{w.cur.offset, w.curLen})
			}
			if tail := w.cur.length - w.curLen; tail > 0 {
				w.s.free = append(w.s.free, extent{w.cur.offset + w.curLen, tail})
			}
			// Open the next extent: pop a freed one or append at the file tail.
			if n := len(w.s.free); n > 0 {
				w.cur = w.s.free[n-1]
				w.s.free = w.s.free[:n-1]
				w.growing = false
			} else {
				w.cur = extent{offset: w.s.fileEnd}
				w.growing = true
			}
			w.curLen = 0
		}
		chunk := len(p)
		if !w.growing {
			if space := int(w.cur.length - w.curLen); chunk > space {
				chunk = space
			}
		}
		var n int
		n, w.err = w.s.file.WriteAt(p[:chunk], w.cur.offset+w.curLen)
		w.curLen += int64(n)
		if w.growing {
			w.cur.length = w.curLen
			w.s.fileEnd = w.cur.offset + w.cur.length
		}
		written += n
		p = p[n:]
		if w.err != nil {
			return written, w.err
		}
	}
	return written, w.err
}

func (w *writer) Close() error {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.s.err != nil {
		return w.s.err
	}
	if w.err != nil {
		// Last write failed: publish nothing and free every reserved extent.
		w.s.free = append(w.s.free, w.chain...)
		if w.cur.length > 0 {
			w.s.free = append(w.s.free, w.cur)
		}
		return w.err
	}
	if w.curLen > 0 {
		w.chain = append(w.chain, extent{w.cur.offset, w.curLen})
	}
	if tail := w.cur.length - w.curLen; tail > 0 {
		w.s.free = append(w.s.free, extent{w.cur.offset + w.curLen, tail})
	}
	w.s.chains[w.h] = w.chain
	return nil
}

// blobReader is an io.ReadSeeker over a blob's chain of extents in one file.
type blobReader struct {
	file    *os.File
	extents []extent
	pos     int64
	size    int64
}

func newBlobReader(file *os.File, extents []extent) *blobReader {
	var size int64
	for _, e := range extents {
		size += e.length
	}
	return &blobReader{file: file, extents: extents, size: size}
}

func (r *blobReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	pos := r.pos
	for _, e := range r.extents {
		if pos < e.length {
			avail := e.length - pos
			chunk := min(int64(len(p)), avail)
			n, err := r.file.ReadAt(p[:chunk], e.offset+pos)
			r.pos += int64(n)
			return n, err
		}
		pos -= e.length
	}
	return 0, io.EOF
}

func (r *blobReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative position")
	}
	r.pos = abs
	return abs, nil
}
