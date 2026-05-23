package oleps

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unsafe"

	"github.com/abemedia/go-msi/internal/codepage"
	"golang.org/x/text/encoding"
)

// ErrFormat is returned when the data is not a valid property set stream.
var ErrFormat = errors.New("oleps: not a valid property set stream")

// Decode parses a property set stream from r.
func Decode(r io.Reader) (PropertySetStream, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return PropertySetStream{}, err
	}
	return Unmarshal(data)
}

// Unmarshal parses data as a property set stream.
func Unmarshal(data []byte) (PropertySetStream, error) {
	d := decoder{buf: data}
	return d.propertySetStream()
}

// A decoder reads the encoded form of a property set stream.
type decoder struct {
	buf      []byte
	setBuf   []byte
	numProps uint32
	cp       uint16
	dec      *encoding.Decoder
}

// propertySetStream decodes the property set stream.
func (d *decoder) propertySetStream() (PropertySetStream, error) {
	data := d.buf

	if bo, err := d.uint16(); err != nil {
		return PropertySetStream{}, err
	} else if bo != byteOrder {
		return PropertySetStream{}, fmt.Errorf("%w: invalid byte order", ErrFormat)
	}

	const headerSize = 28 // ByteOrder + Version + SystemIdentifier + CLSID + NumPropertySets
	if len(data) < headerSize {
		return PropertySetStream{}, io.ErrUnexpectedEOF
	}

	var s PropertySetStream
	if s.Version, _ = d.uint16(); s.Version > 1 {
		return PropertySetStream{}, fmt.Errorf("%w: invalid property set version %d", ErrFormat, s.Version)
	}
	s.SystemIdentifier, _ = d.uint32()
	clsid, _ := d.bytes(16)
	s.CLSID = [16]byte(clsid)
	numPropertySets, _ := d.uint32()
	switch numPropertySets {
	case 1:
	case 2:
		return PropertySetStream{}, fmt.Errorf("%w: more than one property set", ErrUnsupported)
	default:
		return PropertySetStream{}, fmt.Errorf("%w: invalid property set count", ErrFormat)
	}

	propertySetsOffset := headerSize + numPropertySets*20
	if int(propertySetsOffset) > len(data) {
		return PropertySetStream{}, io.ErrUnexpectedEOF
	}

	s.PropertySets = make([]PropertySet, numPropertySets)
	for i := range s.PropertySets {
		fmtid, _ := d.bytes(16)
		off, _ := d.uint32()
		if off < propertySetsOffset {
			return PropertySetStream{}, fmt.Errorf("%w: invalid property set offset", ErrFormat)
		}
		if int64(off) > int64(len(data)) {
			return PropertySetStream{}, io.ErrUnexpectedEOF
		}
		dir := d.buf
		d.buf = data[off:]
		props, err := d.properties()
		if err != nil {
			return PropertySetStream{}, err
		}
		s.PropertySets[i] = PropertySet{FMTID: [16]byte(fmtid), Properties: props}
		d.buf = dir
	}
	return s, nil
}

// properties decodes the properties of one property set.
func (d *decoder) properties() ([]Property, error) {
	d.setBuf = d.buf
	size, err := d.uint32()
	if err != nil {
		return nil, err
	}
	d.numProps, err = d.uint32()
	if err != nil {
		return nil, err
	}
	if size < 8 || int64(size) > int64(len(d.setBuf)) {
		return nil, io.ErrUnexpectedEOF
	}
	if 8+8*int64(d.numProps) > int64(size) {
		return nil, fmt.Errorf("%w: invalid property set size", ErrFormat)
	}
	d.setBuf = d.setBuf[:size]

	minOff := 8 + d.numProps*8
	for i := range d.numProps {
		_, off, end := d.propertyPacket(i)
		if off < minOff || end < off || end-off < 4 || end > size {
			return nil, fmt.Errorf("%w: invalid property offset", ErrFormat)
		}
	}

	cp, err := d.codePage()
	if err != nil {
		return nil, err
	}
	enc := codepage.Encoding(cp)
	if enc == nil {
		return nil, fmt.Errorf("%w: code page %d", ErrUnsupported, cp)
	}
	d.cp, d.dec = cp, enc.NewDecoder()

	props := make([]Property, d.numProps)
	for i := range d.numProps {
		p, err := d.property(i)
		if err != nil {
			return nil, err
		}
		props[i] = p
	}
	return props, nil
}

// propertyPacket returns the PID and byte range of the property at index i.
func (d *decoder) propertyPacket(i uint32) (pid, off, end uint32) {
	b := d.setBuf[8+i*8:]
	pid = binary.LittleEndian.Uint32(b)
	off = binary.LittleEndian.Uint32(b[4:])
	end = uint32(len(d.setBuf))
	if i+1 < d.numProps {
		end = binary.LittleEndian.Uint32(b[12:])
	}
	return pid, off, end
}

// codePage returns the property set's code page.
func (d *decoder) codePage() (uint16, error) {
	for i := range d.numProps {
		pid, off, end := d.propertyPacket(i)
		if pid != 1 {
			continue
		}
		d.buf = d.setBuf[off:end]
		if vt, _ := d.uint16(); vt != vtI2 {
			return 0, fmt.Errorf("%w: invalid code page property", ErrFormat)
		}
		_, _ = d.bytes(2) // skip reserved padding
		cp, err := d.uint16()
		if err != nil {
			return 0, fmt.Errorf("%w: truncated property value", ErrFormat)
		}
		return cp, nil
	}
	return 0, fmt.Errorf("%w: missing code page property", ErrFormat)
}

// property decodes the property at index i.
func (d *decoder) property(i uint32) (Property, error) {
	pid, off, end := d.propertyPacket(i)
	if pid == 0 {
		return Property{}, fmt.Errorf("%w: named properties", ErrUnsupported)
	}
	d.buf = d.setBuf[off:end]
	vt, _ := d.uint16()
	if pad, _ := d.uint16(); pad != 0 {
		return Property{}, fmt.Errorf("%w: property padding is not zero", ErrFormat)
	}
	c, err := resolveCoder(vt)
	if err != nil {
		return Property{}, err
	}
	v, err := c.decode(d)
	if err != nil {
		if err == io.ErrUnexpectedEOF {
			return Property{}, fmt.Errorf("%w: truncated property value", ErrFormat)
		}
		return Property{}, err
	}
	return Property{ID: pid, Value: v}, nil
}

// bytes returns the next n bytes, or [io.ErrUnexpectedEOF] if fewer remain.
func (d *decoder) bytes(n int) ([]byte, error) {
	if n < 0 || len(d.buf) < n {
		return nil, io.ErrUnexpectedEOF
	}
	b := d.buf[:n]
	d.buf = d.buf[n:]
	return b, nil
}

// uint16 reads a little-endian uint16.
func (d *decoder) uint16() (uint16, error) {
	b, err := d.bytes(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

// uint32 reads a little-endian uint32.
func (d *decoder) uint32() (uint32, error) {
	b, err := d.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

// uint64 reads a little-endian uint64.
func (d *decoder) uint64() (uint64, error) {
	b, err := d.bytes(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

// codePageString reads a NUL-terminated string in the property set's code
// page and returns it as UTF-8.
func (d *decoder) codePageString() (string, error) {
	n, err := d.uint32()
	if err != nil || n == 0 {
		return "", err
	}
	raw, err := d.bytes(int(n))
	if err != nil {
		return "", err
	}
	term := []byte{0}
	if d.cp == cpWinUnicode {
		term = []byte{0, 0}
	}
	raw, ok := bytes.CutSuffix(raw, term)
	if !ok {
		return "", fmt.Errorf("%w: missing string terminator", ErrFormat)
	}
	out, err := d.dec.Bytes(raw)
	if err != nil {
		return "", fmt.Errorf("%w: invalid string", ErrFormat)
	}
	return unsafe.String(unsafe.SliceData(out), len(out)), nil
}
