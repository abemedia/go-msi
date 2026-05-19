package oleps

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unsafe"

	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

// Marshal returns s encoded as a property set stream.
func Marshal(s PropertySetStream) ([]byte, error) {
	var e encoder
	if err := e.propertySetStream(s); err != nil {
		return nil, err
	}
	return e.buf, nil
}

// Encode writes s to w as a property set stream.
func Encode(w io.Writer, s PropertySetStream) error {
	b, err := Marshal(s)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// An encoder writes the encoded form of a property set stream.
type encoder struct {
	buf    []byte
	setOff int
	cp     uint16
	enc    *encoding.Encoder
}

// propertySetStream encodes the property set stream s.
func (e *encoder) propertySetStream(s PropertySetStream) error {
	if len(s.PropertySets) == 0 || len(s.PropertySets) > 2 {
		return errors.New("oleps: invalid property set count")
	}
	if len(s.PropertySets) == 2 {
		return fmt.Errorf("%w: more than one property set", ErrUnsupported)
	}
	if s.Version > 1 {
		return fmt.Errorf("oleps: invalid property set version %d", s.Version)
	}

	// Rough pre-size of e.buf to avoid reallocations. If the value-type set
	// grows, add a sizeHint() method to Value rather than extending the
	// LPSTR special-case below.
	size := 28 // ByteOrder + Version + SystemIdentifier + CLSID + NumPropertySets
	for _, ps := range s.PropertySets {
		size += 28 // FMTID + Offset + PropertySet header
		for _, p := range ps.Properties {
			size += 23 // ID + Offset + Type + Padding + largest value + alignment
			if str, ok := p.Value.(LPSTR); ok {
				size += len(str)
			}
		}
	}
	e.buf = make([]byte, 0, size)

	e.uint16(byteOrder)
	e.uint16(s.Version)
	e.uint32(s.SystemIdentifier)
	e.bytes(s.CLSID[:])
	e.uint32(uint32(len(s.PropertySets)))

	var offsets [2]int
	for i := range s.PropertySets {
		e.bytes(s.PropertySets[i].FMTID[:])
		offsets[i] = len(e.buf)
		e.uint32(0) // Offsetn, patched before the set is written
	}
	for i := range s.PropertySets {
		e.patch32(offsets[i], uint32(len(e.buf)))
		if err := e.properties(s.PropertySets[i].Properties); err != nil {
			return err
		}
	}
	return nil
}

// properties encodes the properties of one property set.
func (e *encoder) properties(props []Property) error {
	e.setOff = len(e.buf)
	e.uint32(0) // Size, patched after the set is written
	e.uint32(uint32(len(props)))
	for range 2 * len(props) {
		e.uint32(0) // (ID, Offset) table entry, patched per property
	}

	// First pass: resolve the code page from PID 1 (position unspecified).
	for _, p := range props {
		if p.ID != 1 {
			continue
		}
		cp, ok := p.Value.(I2)
		if !ok {
			return errors.New("oleps: invalid code page property")
		}
		enc, err := resolveEncoding(uint16(cp))
		if err != nil {
			return err
		}
		e.cp, e.enc = uint16(cp), enc.NewEncoder()
		break
	}
	if e.enc == nil {
		return errors.New("oleps: missing code page property")
	}

	for i, p := range props {
		if err := e.property(i, p); err != nil {
			return err
		}
	}

	e.align() // PropertySet packet size is a multiple of 4
	e.patch32(e.setOff, uint32(len(e.buf)-e.setOff))
	return nil
}

// property encodes the property p at table index i.
func (e *encoder) property(i int, p Property) error {
	if p.ID == 0 {
		return fmt.Errorf("%w: named properties", ErrUnsupported)
	}
	if p.Value == nil {
		return errors.New("oleps: nil property value")
	}
	vt := p.Value.propertyType()
	c, _ := resolveCoder(vt) // every Value type has a coder
	e.align()
	pos := e.setOff + 8 + i*8
	e.patch32(pos, p.ID)
	e.patch32(pos+4, uint32(len(e.buf)-e.setOff))
	e.uint16(vt)
	e.uint16(0) // padding
	return c.encode(e, p.Value)
}

// bytes appends p.
func (e *encoder) bytes(p []byte) { e.buf = append(e.buf, p...) }

// uint16 writes v as a little-endian uint16.
func (e *encoder) uint16(v uint16) { e.buf = binary.LittleEndian.AppendUint16(e.buf, v) }

// uint32 writes v as a little-endian uint32.
func (e *encoder) uint32(v uint32) { e.buf = binary.LittleEndian.AppendUint32(e.buf, v) }

// uint64 writes v as a little-endian uint64.
func (e *encoder) uint64(v uint64) { e.buf = binary.LittleEndian.AppendUint64(e.buf, v) }

// align pads with zero bytes to the next 4-byte boundary.
func (e *encoder) align() {
	for len(e.buf)%4 != 0 {
		e.buf = append(e.buf, 0)
	}
}

// patch32 overwrites the little-endian uint32 at pos.
func (e *encoder) patch32(pos int, v uint32) {
	binary.LittleEndian.PutUint32(e.buf[pos:], v)
}

// codePageString writes s as a NUL-terminated string in the property set's
// code page.
func (e *encoder) codePageString(s string) error {
	off := len(e.buf)
	e.uint32(0)
	out, _, err := transform.Append(e.enc, e.buf, unsafe.Slice(unsafe.StringData(s), len(s)))
	if err != nil {
		return errors.New("oleps: cannot encode string")
	}
	e.buf = out
	e.buf = append(e.buf, 0)
	if e.cp == cpWinUnicode {
		e.buf = append(e.buf, 0)
	}
	e.patch32(off, uint32(len(e.buf)-off-4))
	return nil
}
