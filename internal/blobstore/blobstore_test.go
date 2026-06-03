package blobstore_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/abemedia/go-msi/internal/blobstore"
)

func stage(t *testing.T, s *blobstore.Store, data []byte) blobstore.Handle {
	t.Helper()
	h, w, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return h
}

func read(t *testing.T, s *blobstore.Store, h blobstore.Handle) []byte {
	t.Helper()
	got, err := io.ReadAll(open(t, s, h))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func open(t *testing.T, s *blobstore.Store, h blobstore.Handle) io.ReadSeeker {
	t.Helper()
	rs, err := s.Open(h)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"small", []byte("hello, blob")},
		{"longer", []byte("second blob, somewhat longer")},
		{"page", bytes.Repeat([]byte("x"), 4096)},
	}
	s := &blobstore.Store{}
	defer s.Close()
	hs := make([]blobstore.Handle, len(tests))
	for i, test := range tests {
		hs[i] = stage(t, s, test.data)
	}
	for i, test := range tests {
		if got := read(t, s, hs[i]); !bytes.Equal(got, test.data) {
			t.Errorf("%s: got %d bytes, want %d", test.name, len(got), len(test.data))
		}
	}
}

func TestSeek(t *testing.T) {
	s := &blobstore.Store{}
	defer s.Close()
	input := []byte("abcdefghij")
	h := stage(t, s, input)
	r := open(t, s, h)
	if _, err := r.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, input[3:7]) {
		t.Errorf("got %q, want %q", buf, input[3:7])
	}
}

func TestReuse(t *testing.T) {
	s := &blobstore.Store{}
	defer s.Close()
	size := func() int64 {
		t.Helper()
		fi, err := s.File().Stat()
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}

	hA := stage(t, s, bytes.Repeat([]byte("A"), 100))
	hB := stage(t, s, bytes.Repeat([]byte("B"), 200))
	stage(t, s, bytes.Repeat([]byte("C"), 50)) // anchor so freed extents stay interior

	// Exact fit: free 100 and restage 100 — the freed slot is reused, no growth.
	s.Delete(hA)
	before := size()
	hExact := stage(t, s, bytes.Repeat([]byte("x"), 100))
	if got := size(); got != before {
		t.Errorf("exact-fit reuse grew file %d -> %d, want no growth", before, got)
	}
	if got := read(t, s, hExact); len(got) != 100 {
		t.Errorf("exact-fit blob: got %d bytes, want 100", len(got))
	}

	// Spanning: free 100+200 and stage 400 — it reuses both freed slots and
	// appends the remaining 100, so the file grows by exactly len-freed and the
	// blob's read chain spans multiple non-contiguous extents.
	const freed = 300
	s.Delete(hExact)
	s.Delete(hB)
	before = size()
	want := make([]byte, 400)
	for i := range want {
		want[i] = byte('a' + i%26)
	}
	h := stage(t, s, want)
	if grew := size() - before; grew != int64(len(want)-freed) {
		t.Errorf("spanning reuse grew file by %d, want %d", grew, len(want)-freed)
	}
	if got := read(t, s, h); !bytes.Equal(got, want) {
		t.Errorf("multi-extent round-trip mismatch:\ngot  %x\nwant %x", got, want)
	}
}

func TestInterleavedWriters(t *testing.T) {
	s := &blobstore.Store{}
	defer s.Close()
	hA, wA, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	hB, wB, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	wantA := []byte("AAAAAAAAAA")
	wantB := []byte("BBBBBBBBBB")
	for i := range wantA {
		if _, err := wA.Write(wantA[i : i+1]); err != nil {
			t.Fatal(err)
		}
		if _, err := wB.Write(wantB[i : i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := wA.Close(); err != nil {
		t.Fatal(err)
	}
	if err := wB.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, s, hA); !bytes.Equal(got, wantA) {
		t.Errorf("writer A: got %q, want %q", got, wantA)
	}
	if got := read(t, s, hB); !bytes.Equal(got, wantB) {
		t.Errorf("writer B: got %q, want %q", got, wantB)
	}
}

func TestLargeBlob(t *testing.T) {
	s := &blobstore.Store{}
	defer s.Close()
	want := make([]byte, 5<<20) // 5 MiB
	if _, err := rand.Read(want); err != nil {
		t.Fatal(err)
	}
	h, w, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, bytes.NewReader(want)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := read(t, s, h)
	if !bytes.Equal(got, want) {
		t.Errorf("large blob mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestConcurrentReads(t *testing.T) {
	s := &blobstore.Store{}
	defer s.Close()
	const n = 8
	hs := make([]blobstore.Handle, n)
	wants := make([][]byte, n)
	for i := range hs {
		wants[i] = bytes.Repeat([]byte{byte('a' + i)}, 4096+i*100)
		hs[i] = stage(t, s, wants[i])
	}

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			for j := range 50 {
				rs, err := s.Open(hs[i])
				if err != nil {
					t.Error(err)
					return
				}
				got, err := io.ReadAll(rs)
				if err != nil {
					t.Error(err)
					return
				}
				if !bytes.Equal(got, wants[i]) {
					t.Errorf("blob %d round %d mismatch", i, j)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestUnknownHandle(t *testing.T) {
	s := &blobstore.Store{}
	defer s.Close()
	t.Run("delete", func(_ *testing.T) {
		s.Delete(9999) // unknown handles are a silent no-op
	})
	t.Run("open", func(t *testing.T) {
		if _, err := s.Open(9999); !errors.Is(err, blobstore.ErrNotFound) {
			t.Errorf("Open unknown handle = %v, want ErrNotFound", err)
		}
	})
}

func TestClose(t *testing.T) {
	t.Run("removes file", func(t *testing.T) {
		s := &blobstore.Store{}
		stage(t, s, []byte("data"))
		path := s.File().Name()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("file still present after Close: %v", err)
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		s := &blobstore.Store{}
		stage(t, s, []byte("data")) // force init so Close has a file to remove
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Errorf("second Close returned %v, want nil", err)
		}
	})
	t.Run("without init", func(t *testing.T) {
		s := &blobstore.Store{}
		if err := s.Close(); err != nil {
			t.Errorf("Close on uninited store returned %v, want nil", err)
		}
	})
}

func TestGCCleanup(t *testing.T) {
	path := func() string {
		s := &blobstore.Store{}
		stage(t, s, []byte("data")) // force init so a file exists
		return s.File().Name()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
	}
	t.Errorf("backing file %s still present after GC", path)
}

func TestUseAfterClose(t *testing.T) {
	s := &blobstore.Store{}
	defer s.Close()

	_, wClosed, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := wClosed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := wClosed.Write([]byte("x")); !errors.Is(err, blobstore.ErrClosed) {
		t.Errorf("Write on a closed writer = %v, want ErrClosed", err)
	}

	h := stage(t, s, []byte("data"))
	_, wOpen, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Create(); !errors.Is(err, blobstore.ErrClosed) {
		t.Errorf("Create after store Close = %v, want ErrClosed", err)
	}
	if _, err := s.Open(h); !errors.Is(err, blobstore.ErrClosed) {
		t.Errorf("Open after store Close = %v, want ErrClosed", err)
	}
	if _, err := wOpen.Write([]byte("x")); !errors.Is(err, blobstore.ErrClosed) {
		t.Errorf("Write after store Close = %v, want ErrClosed", err)
	}
	if err := wOpen.Close(); !errors.Is(err, blobstore.ErrClosed) {
		t.Errorf("writer Close after store Close = %v, want ErrClosed", err)
	}
	s.Delete(h)
}
