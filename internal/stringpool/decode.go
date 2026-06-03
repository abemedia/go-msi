package stringpool

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unsafe"

	"golang.org/x/text/transform"
)

// ErrFormat is returned when the pool or data stream is malformed.
var ErrFormat = errors.New("not a valid string pool")

// Decode parses the _StringPool and _StringData streams.
func Decode(pool, data []byte) (*Pool, error) { //nolint:funlen
	if len(pool) < 4 {
		return nil, io.ErrUnexpectedEOF
	}
	if len(pool)%4 != 0 {
		return nil, fmt.Errorf("%w: pool stream length not a multiple of 4", ErrFormat)
	}

	header := binary.LittleEndian.Uint32(pool)
	longRefs := header&longStrFlag != 0
	cp := header &^ longStrFlag
	if cp > 0xFFFF {
		return nil, fmt.Errorf("%w: code page > 65535", ErrFormat)
	}

	maxLen := len(pool)/4 - 1 // each record is at least 4 bytes
	p, err := newPool(uint16(cp), maxLen, longRefs)
	if err != nil {
		return nil, err
	}
	dec := getEncoding(uint16(cp)).NewDecoder()

	decoded := make([]byte, 0, len(data))
	ends := make([]int, 0, maxLen)
	off := int64(0)
	for pos := 4; pos+4 <= len(pool); {
		length := int64(binary.LittleEndian.Uint16(pool[pos:]))
		refs := binary.LittleEndian.Uint16(pool[pos+2:])
		pos += 4

		if length == 0 && refs != 0 { // long string: length in the next 4 bytes
			if pos+4 > len(pool) {
				return nil, io.ErrUnexpectedEOF
			}
			length = int64(binary.LittleEndian.Uint32(pool[pos:]))
			pos += 4
		}

		if off+length > int64(len(data)) {
			return nil, io.ErrUnexpectedEOF
		}

		if refs == 0 {
			p.entries = append(p.entries, entry{})
			p.free = append(p.free, uint32(len(p.entries)))
			ends = append(ends, len(decoded))
			off += length
			continue
		}

		decoded, _, err = transform.Append(dec, decoded, data[off:off+length])
		if err != nil {
			return nil, fmt.Errorf("decode ID %d in code page %d: %w", len(p.entries)+1, cp, err)
		}
		ends = append(ends, len(decoded))
		p.entries = append(p.entries, entry{persistentRefcount: refs})
		off += length
	}

	if off != int64(len(data)) {
		return nil, fmt.Errorf("%w: %d trailing bytes in data stream", ErrFormat, int64(len(data))-off)
	}

	var all string
	if len(decoded) == cap(decoded) {
		all = unsafe.String(unsafe.SliceData(decoded), len(decoded))
	} else {
		all = string(decoded)
	}

	start := 0
	for i := range p.entries {
		end := ends[i]
		e := &p.entries[i]
		e.s = all[start:end]
		if e.persistentRefcount != 0 {
			p.index[e.s] = uint32(i) + 1
		}
		start = end
	}
	return p, nil
}
