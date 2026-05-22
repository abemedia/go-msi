package oleps

import (
	"errors"
	"fmt"
	"math"
	"time"
)

type (
	// I2 is a 16-bit signed integer property value.
	I2 int16

	// I4 is a 32-bit signed integer property value.
	I4 int32

	// UI4 is a 32-bit unsigned integer property value.
	UI4 uint32

	// LPSTR is a string property value, transcoded to and from the property set's code page.
	LPSTR string

	// FileTime is a timestamp property value. A zero FileTime is an unset timestamp.
	FileTime time.Time
)

const (
	vtI2       = 0x02
	vtI4       = 0x03
	vtUI4      = 0x13
	vtLPSTR    = 0x1E
	vtFileTime = 0x40
)

// propertyType returns the VT type code and ensures that only typed property
// values can be assigned to a [Value].
func (I2) propertyType() uint16       { return vtI2 }
func (I4) propertyType() uint16       { return vtI4 }
func (UI4) propertyType() uint16      { return vtUI4 }
func (LPSTR) propertyType() uint16    { return vtLPSTR }
func (FileTime) propertyType() uint16 { return vtFileTime }

// valueCoder encodes and decodes a value of one property type.
type valueCoder interface {
	decode(d *decoder) (Value, error)
	encode(e *encoder, v Value) error
}

type i2Coder struct{}

func (i2Coder) decode(d *decoder) (Value, error) {
	n, err := d.uint16()
	if err != nil {
		return nil, err
	}
	return I2(n), nil
}

func (i2Coder) encode(e *encoder, v Value) error { e.uint16(uint16(v.(I2))); return nil }

type i4Coder struct{}

func (i4Coder) decode(d *decoder) (Value, error) {
	n, err := d.uint32()
	if err != nil {
		return nil, err
	}
	return I4(n), nil
}

func (i4Coder) encode(e *encoder, v Value) error { e.uint32(uint32(v.(I4))); return nil }

type ui4Coder struct{}

func (ui4Coder) decode(d *decoder) (Value, error) {
	n, err := d.uint32()
	if err != nil {
		return nil, err
	}
	return UI4(n), nil
}

func (ui4Coder) encode(e *encoder, v Value) error { e.uint32(uint32(v.(UI4))); return nil }

type lpstrCoder struct{}

func (lpstrCoder) decode(d *decoder) (Value, error) {
	s, err := d.codePageString()
	if err != nil {
		return nil, err
	}
	return LPSTR(s), nil
}

func (lpstrCoder) encode(e *encoder, v Value) error { return e.codePageString(string(v.(LPSTR))) }

// FILETIME measures 100-nanosecond ticks since 1601-01-01 UTC.
const (
	epochDeltaSecs = 11644473600 // 1601-01-01 to the Unix epoch
	ticksPerSec    = 10_000_000
	nanosPerTick   = 100
	maxUnixSec     = math.MaxUint64/ticksPerSec - epochDeltaSecs - 1 // year ~60056
)

type fileTimeCoder struct{}

func (fileTimeCoder) decode(d *decoder) (Value, error) {
	ft, err := d.uint64()
	if err != nil {
		return nil, err
	}
	if ft == 0 {
		return FileTime{}, nil
	}
	sec, rem := ft/ticksPerSec, ft%ticksPerSec
	return FileTime(time.Unix(int64(sec)-epochDeltaSecs, int64(rem)*nanosPerTick).UTC()), nil
}

func (fileTimeCoder) encode(e *encoder, v Value) error {
	t := time.Time(v.(FileTime))
	if t.IsZero() {
		e.uint64(0)
		return nil
	}
	sec, nsec := t.Unix(), int64(t.Nanosecond())
	if sec < -epochDeltaSecs {
		return errors.New("oleps: cannot encode timestamp before year 1601")
	}
	if sec > maxUnixSec {
		return errors.New("oleps: cannot encode timestamp after year 60056")
	}
	e.uint64(uint64(sec+epochDeltaSecs)*ticksPerSec + uint64(nsec/nanosPerTick))
	return nil
}

// resolveCoder maps a property type to its coder.
// Returns [ErrUnsupported] for an unimplemented property type.
func resolveCoder(t uint16) (valueCoder, error) {
	switch t {
	case vtI2:
		return i2Coder{}, nil
	case vtI4:
		return i4Coder{}, nil
	case vtUI4:
		return ui4Coder{}, nil
	case vtLPSTR:
		return lpstrCoder{}, nil
	case vtFileTime:
		return fileTimeCoder{}, nil
	}
	return nil, fmt.Errorf("%w: property type 0x%04x", ErrUnsupported, t)
}
