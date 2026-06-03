package blobstore

import "os"

// Sentinel errors exposed for matching in tests.
var (
	ErrClosed   = errClosed
	ErrNotFound = errNotFound
)

// File returns the store's backing temp file, or nil if it has not been
// created yet. Exposed for tests only.
func (s *Store) File() *os.File {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file
}
