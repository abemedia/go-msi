// Package stringpool decodes and encodes the MSI _StringPool and
// _StringData streams: a shared, refcounted, code-page-encoded string table
// referenced by every other table by integer ID.
package stringpool

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"golang.org/x/text/encoding"
)

// ErrUnsupportedCodePage is returned when a pool uses a code page this package does
// not support.
var ErrUnsupportedCodePage = errors.New("unsupported code page")

// longStrFlag is the top bit of the pool's 4-byte header word; set when
// table-stream string references are 3 bytes instead of 2.
const longStrFlag = 0x80000000

// maxRefcount is the refcount ceiling; further references are silently dropped.
const maxRefcount = 0xFFFF

type entry struct {
	s                     string
	persistentRefcount    uint16
	nonpersistentRefcount uint16
}

// Pool is an MSI string pool.
type Pool struct {
	index    map[string]uint32
	entries  []entry
	free     []uint32
	encoder  *encoding.Encoder
	codepage uint16
	src, dst [4]byte
	longRefs bool
}

// New returns an empty pool that will encode strings in the given Windows
// code page. Returns an error if cp is not supported.
func New(cp uint16) (*Pool, error) { return newPool(cp, 0, false) }

func newPool(cp uint16, capHint int, longRefs bool) (*Pool, error) {
	enc := getEncoding(cp)
	if enc == nil {
		return nil, fmt.Errorf("%w %d", ErrUnsupportedCodePage, cp)
	}
	return &Pool{
		codepage: cp,
		encoder:  enc.NewEncoder(),
		entries:  make([]entry, 0, capHint),
		index:    make(map[string]uint32, capHint),
		longRefs: longRefs,
	}, nil
}

// Codepage returns the Windows code page strings are stored in.
func (p *Pool) Codepage() uint16 { return p.codepage }

// SetCodepage sets the code page used by [Encode].
// Returns an error if cp is not supported or any persisted string contains a
// rune not representable in cp.
func (p *Pool) SetCodepage(cp uint16) error {
	enc := getEncoding(cp)
	if enc == nil {
		return fmt.Errorf("%w %d", ErrUnsupportedCodePage, cp)
	}
	encoder := enc.NewEncoder()
	var src, dst [4]byte
	for _, e := range p.entries {
		if e.persistentRefcount == 0 {
			continue
		}
		for _, r := range e.s {
			if r < 0x80 {
				continue
			}
			n := utf8.EncodeRune(src[:], r)
			if _, _, err := encoder.Transform(dst[:], src[:n], true); err != nil {
				return fmt.Errorf("rune %q not encodable in code page %d", r, cp)
			}
		}
	}
	p.codepage = cp
	p.encoder = encoder
	return nil
}

// LongRefs reports whether table-stream string references are 3 bytes
// instead of 2. Once set the result stays true for the pool's lifetime.
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
	if e.persistentRefcount == 0 && e.nonpersistentRefcount == 0 {
		return "", false
	}
	return e.s, true
}

// LookupID returns the ID of s. The empty string returns (0, true) for
// the canonical NULL ID; an absent string returns (0, false).
func (p *Pool) LookupID(s string) (id uint32, ok bool) {
	if s == "" {
		return 0, true
	}
	id, ok = p.index[s]
	return id, ok
}

// Intern returns the ID for s, incrementing its persistent or
// non-persistent refcount. The empty string returns 0.
func (p *Pool) Intern(s string, persistent bool) uint32 {
	if s == "" {
		return 0
	}
	if id, ok := p.index[s]; ok {
		e := &p.entries[id-1]
		switch {
		case !persistent && e.nonpersistentRefcount < maxRefcount:
			e.nonpersistentRefcount++
		case persistent && e.persistentRefcount < maxRefcount:
			e.persistentRefcount++
		}
		return id
	}
	e := entry{s: s}
	if persistent {
		e.persistentRefcount = 1
	} else {
		e.nonpersistentRefcount = 1
	}
	var id uint32
	if n := len(p.free); n > 0 {
		id = p.free[n-1]
		p.free = p.free[:n-1]
		p.entries[id-1] = e
	} else {
		p.entries = append(p.entries, e)
		id = uint32(len(p.entries))
	}
	p.index[s] = id
	return id
}

// Validate reports whether s is encodable in the pool's current code page.
func (p *Pool) Validate(s string) error {
	p.encoder.Reset()
	hasRuneError := false
	for _, r := range s {
		if r < 0x80 {
			continue
		}
		n := utf8.EncodeRune(p.src[:], r)
		if _, _, err := p.encoder.Transform(p.dst[:], p.src[:n], true); err != nil {
			return fmt.Errorf("rune %q not encodable in code page %d", r, p.codepage)
		}
		if r == utf8.RuneError {
			hasRuneError = true
		}
	}
	if hasRuneError && !utf8.ValidString(s) {
		return fmt.Errorf("string %q is not valid UTF-8", s)
	}
	return nil
}

// Release decrements id's persistent or non-persistent refcount. The
// slot becomes reusable when both reach 0.
func (p *Pool) Release(id uint32, persistent bool) {
	if id == 0 || id > uint32(len(p.entries)) {
		return
	}
	e := &p.entries[id-1]
	switch {
	case persistent && e.persistentRefcount > 0:
		e.persistentRefcount--
	case !persistent && e.nonpersistentRefcount > 0:
		e.nonpersistentRefcount--
	default:
		return
	}
	if e.persistentRefcount == 0 && e.nonpersistentRefcount == 0 {
		delete(p.index, e.s)
		e.s = ""
		p.free = append(p.free, id)
	}
}
