// Package stringpool decodes and encodes the MSI _StringPool and
// _StringData streams: a shared, refcounted, code-page-encoded string table
// referenced by every other table by integer ID.
package stringpool

import (
	"errors"
	"fmt"

	"github.com/abemedia/go-msi/internal/codepage"
)

// ErrUnsupportedCodePage is returned when a pool uses a code page this package does
// not implement.
var ErrUnsupportedCodePage = errors.New("stringpool: unsupported code page")

// longStrFlag is the top bit of the pool's 4-byte header word; set when
// table-stream string references are 3 bytes instead of 2.
const longStrFlag = 0x80000000

// maxRefcount is the refcount ceiling; further references are silently dropped.
const maxRefcount = 0xFFFF

type entry struct {
	s        string
	refcount uint16
}

// Pool is an MSI string pool.
type Pool struct {
	codepage uint16
	entries  []entry
	index    map[string]uint32
	free     []uint32
	longRefs bool
}

// New returns an empty pool that will encode strings in the given Windows
// code page. Returns [ErrUnsupportedCodePage] if cp is not implemented.
func New(cp uint16) (*Pool, error) { return newPool(cp, 0, false) }

func newPool(cp uint16, capHint int, longRefs bool) (*Pool, error) {
	if codepage.Encoding(cp) == nil {
		return nil, fmt.Errorf("%w %d", ErrUnsupportedCodePage, cp)
	}
	return &Pool{
		codepage: cp,
		entries:  make([]entry, 0, capHint),
		index:    make(map[string]uint32, capHint),
		longRefs: longRefs,
	}, nil
}

// Codepage returns the Windows code page strings are stored in.
func (p *Pool) Codepage() uint16 { return p.codepage }

// LongRefs reports whether table-stream string references are 3 bytes
// instead of 2. Sticky once set (either by reading a file with the flag
// already set, or by the pool growing past 65535 entries).
func (p *Pool) LongRefs() bool { return p.longRefs || len(p.entries) > 0xFFFF }

// Lookup returns the string at id. ID 0 returns ("", true) for the empty
// string; an out-of-range or released ID returns ("", false).
func (p *Pool) Lookup(id uint32) (string, bool) {
	if id == 0 {
		return "", true
	}
	if id > uint32(len(p.entries)) {
		return "", false
	}
	e := p.entries[id-1]
	if e.refcount == 0 {
		return "", false
	}
	return e.s, true
}

// Intern returns the ID for s, creating an entry if absent. Each call
// increments the refcount. The empty string always returns 0 without
// storing.
func (p *Pool) Intern(s string) uint32 {
	if s == "" {
		return 0
	}
	if id, ok := p.index[s]; ok {
		if p.entries[id-1].refcount < maxRefcount {
			p.entries[id-1].refcount++
		}
		return id
	}
	var id uint32
	if n := len(p.free); n > 0 {
		id = p.free[n-1]
		p.free = p.free[:n-1]
		p.entries[id-1] = entry{s: s, refcount: 1}
	} else {
		p.entries = append(p.entries, entry{s: s, refcount: 1})
		id = uint32(len(p.entries))
	}
	p.index[s] = id
	return id
}

// Release decrements id's refcount. When it hits 0 the slot becomes
// reusable but its ID is preserved on disk as an empty record.
func (p *Pool) Release(id uint32) {
	if id == 0 || id > uint32(len(p.entries)) {
		return
	}
	e := &p.entries[id-1]
	if e.refcount == 0 {
		return
	}
	e.refcount--
	if e.refcount == 0 {
		delete(p.index, e.s)
		e.s = ""
		p.free = append(p.free, id)
	}
}
