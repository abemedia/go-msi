package msidb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"unicode/utf8"
)

// toFieldValue converts v to the raw field value for the i'th column of r's
// table. For a binary column it returns the payload as a reader for the caller
// to stage once the record is committed; the value is not stored here.
func toFieldValue(r *Record, i int, v any) (uint32, io.Reader, error) {
	t := r.table
	c := t.columns[i]
	if v == nil {
		if !c.Nullable {
			return 0, nil, errors.New("NULL not allowed")
		}
		return 0, nil, nil
	}
	switch c.Type {
	case ColumnInteger:
		fv, err := intValue(v, c.Size)
		return fv, nil, err
	case ColumnString:
		s, ok := v.(string)
		if !ok {
			return 0, nil, fmt.Errorf("expected string, got %T", v)
		}
		if c.Size > 0 && utf8.RuneCountInString(s) > c.Size {
			return 0, nil, fmt.Errorf("length %d exceeds column max %d", utf8.RuneCountInString(s), c.Size)
		}
		if err := t.db.pool.Validate(s); err != nil {
			return 0, nil, err
		}
		return t.db.pool.Intern(s, t.name != systemTableStreams), nil, nil
	case ColumnBinary:
		switch x := v.(type) {
		case []byte:
			return 1, bytes.NewReader(x), nil
		case io.Reader:
			return 1, x, nil
		default:
			return 0, nil, fmt.Errorf("expected io.Reader or []byte, got %T", v)
		}
	}
	return 0, nil, fmt.Errorf("unsupported column type %d", c.Type)
}

// intValue converts v to the raw field value for a size-byte integer column.
func intValue(v any, size int) (uint32, error) {
	var n int
	var err error
	switch x := v.(type) {
	case int:
		n, err = toInt(x, size)
	case int8:
		n, err = toInt(x, size)
	case int16:
		n, err = toInt(x, size)
	case int32:
		n, err = toInt(x, size)
	case int64:
		n, err = toInt(x, size)
	case uint:
		n, err = toInt(x, size)
	case uint8:
		n, err = toInt(x, size)
	case uint16:
		n, err = toInt(x, size)
	case uint32:
		n, err = toInt(x, size)
	case uint64:
		n, err = toInt(x, size)
	case uintptr:
		n, err = toInt(x, size)
	default:
		return 0, fmt.Errorf("expected integer, got %T", v)
	}
	if err != nil {
		return 0, err
	}
	return encodeInt(n, size), nil
}

// errOutOfRange marks an integer value that does not fit its column.
var errOutOfRange = errors.New("out of range")

// toInt returns v as an int if it fits an MSI integer column of the
// given byte size (2 or 4).
func toInt[T interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}](v T, size int) (int, error) {
	var limit int64
	switch size {
	case 2:
		limit = math.MaxInt16
	case 4:
		limit = math.MaxInt32
	default:
		panic("invalid int column size") // unreachable
	}

	if (v >= 0 && uint64(v) > uint64(limit)) || int64(v) < -limit {
		return 0, fmt.Errorf("value %d %w for int(%d) column", v, errOutOfRange, size)
	}

	return int(v), nil
}
