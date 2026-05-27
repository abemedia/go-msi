package stringpool

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/text/transform"
)

// Encode emits the _StringPool and _StringData streams.
func Encode(p *Pool) (pool, data []byte, err error) {
	encoder := getEncoding(p.codepage).NewEncoder()

	// Worst case: every entry needs an 8-byte long-string record.
	pool = make([]byte, 4+8*len(p.entries))
	header := uint32(p.codepage)
	if p.LongRefs() {
		header |= longStrFlag
	}
	binary.LittleEndian.PutUint32(pool, header)

	// Estimate data cap from raw UTF-8 lengths. Exact for 1-byte codepages,
	// an underestimate for UTF-16; transform.Append's internal grow handles
	// overflow geometrically.
	dataCap := 0
	for _, e := range p.entries {
		if e.refcount != 0 {
			dataCap += len(e.s)
		}
	}
	data = make([]byte, 0, dataCap)

	pos := 4
	for i, e := range p.entries {
		if e.refcount == 0 {
			pos += 4 // zero from make
			continue
		}
		before := len(data)
		data, _, err = transform.Append(encoder, data, unsafe.Slice(unsafe.StringData(e.s), len(e.s)))
		if err != nil {
			return nil, nil, fmt.Errorf("stringpool: encode ID %d in code page %d: %w", i+1, p.codepage, err)
		}
		sz := len(data) - before
		if sz < 0x10000 {
			binary.LittleEndian.PutUint16(pool[pos:], uint16(sz))
			binary.LittleEndian.PutUint16(pool[pos+2:], e.refcount)
			pos += 4
		} else { // long string
			binary.LittleEndian.PutUint16(pool[pos+2:], e.refcount)
			binary.LittleEndian.PutUint32(pool[pos+4:], uint32(sz))
			pos += 8
		}
	}
	return pool[:pos], data, nil
}
