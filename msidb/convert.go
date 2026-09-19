package msidb

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/abemedia/go-msi/internal/stringpool"
)

type integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

// convertValue returns v as the value column c stores: an int, a string, an
// [io.Reader] for a binary payload, or nil for NULL. It returns an error if
// column c cannot store v.
func convertValue(pool *stringpool.Pool, c Column, v any) (any, error) {
	v, err := normalize(v)
	if err != nil {
		return nil, err
	}
	if v == nil {
		if !c.Nullable {
			return nil, errors.New("NULL not allowed")
		}
		return nil, nil //nolint:nilnil
	}
	switch c.Type {
	case ColumnInteger:
		limit := math.MaxInt32
		if c.Size == 2 {
			limit = math.MaxInt16
		}
		return intValue(v, limit)
	case ColumnString:
		s, err := stringValue(v)
		if err != nil {
			return nil, err
		}
		if s == "" { // The empty string is NULL.
			if !c.Nullable {
				return nil, errors.New("empty string not allowed in a NOT NULL column")
			}
			return nil, nil //nolint:nilnil
		}
		// Stream names live in the CFB directory as UTF-16, not in the string pool.
		if c.Table != systemTableStreams {
			if err := pool.Validate(s); err != nil {
				return nil, err
			}
		}
		return s, nil
	case ColumnBinary:
		return binaryReader(v)
	}
	return nil, fmt.Errorf("unsupported column type %d", c.Type)
}

var valuerType = reflect.TypeFor[driver.Valuer]()

// normalize reduces v to a plain Go value, taking a [driver.Valuer]'s result,
// a pointer's target or a named type's underlying value.
func normalize(v any) (any, error) {
	if vr, ok := v.(driver.Valuer); ok {
		rv := reflect.ValueOf(vr)
		if rv.Kind() == reflect.Pointer && rv.IsNil() && rv.Type().Elem().Implements(valuerType) {
			return nil, nil //nolint:nilnil
		}
		var err error
		if v, err = vr.Value(); err != nil {
			return nil, err
		}
	}
	switch p := v.(type) {
	case nil, string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return v, nil
	case []byte:
		if p == nil {
			return nil, nil //nolint:nilnil
		}
		return v, nil
	case *string:
		return indirect(p), nil
	case *int:
		return indirect(p), nil
	case *int8:
		return indirect(p), nil
	case *int16:
		return indirect(p), nil
	case *int32:
		return indirect(p), nil
	case *int64:
		return indirect(p), nil
	case *uint:
		return indirect(p), nil
	case *uint8:
		return indirect(p), nil
	case *uint16:
		return indirect(p), nil
	case *uint32:
		return indirect(p), nil
	case *uint64:
		return indirect(p), nil
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil //nolint:nilnil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint(), nil
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			if rv.IsNil() {
				return nil, nil //nolint:nilnil
			}
			return rv.Bytes(), nil
		}
	}
	return v, nil
}

func indirect[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// intValue returns v as an int if v is an integer in the range +/-limit.
func intValue(v any, limit int) (int, error) {
	switch x := v.(type) {
	case int:
		return inRange(x, limit)
	case int8:
		return inRange(x, limit)
	case int16:
		return inRange(x, limit)
	case int32:
		return inRange(x, limit)
	case int64:
		return inRange(x, limit)
	case uint:
		return inRange(x, limit)
	case uint8:
		return inRange(x, limit)
	case uint16:
		return inRange(x, limit)
	case uint32:
		return inRange(x, limit)
	case uint64:
		return inRange(x, limit)
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			if errors.Is(err, strconv.ErrRange) {
				return 0, fmt.Errorf("value %s out of range", x)
			}
			return 0, fmt.Errorf("expected integer, got %q", x)
		}
		return inRange(n, limit)
	}
	return 0, fmt.Errorf("expected integer, got %T", v)
}

func inRange[T integer](v T, limit int) (int, error) {
	if (v >= 0 && uint64(v) > uint64(limit)) || int64(v) < -int64(limit) {
		return 0, fmt.Errorf("value %d out of range", v)
	}
	return int(v), nil
}

func stringValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	case fmt.Stringer:
		return x.String(), nil
	case int:
		return strconv.Itoa(x), nil
	case int8:
		return strconv.FormatInt(int64(x), 10), nil
	case int16:
		return strconv.FormatInt(int64(x), 10), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(x), 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	}
	return "", fmt.Errorf("expected string, got %T", v)
}

func binaryReader(v any) (io.Reader, error) {
	switch x := v.(type) {
	case io.Reader:
		return x, nil
	case []byte:
		return bytes.NewReader(x), nil
	case string:
		return strings.NewReader(x), nil
	}
	return nil, fmt.Errorf("expected io.Reader, []byte or string, got %T", v)
}

// encodeValue returns v encoded as a cell of column c. v must be a value
// returned by convertValue. A string is interned in pool with a persistent or
// a temporary reference.
func encodeValue(pool *stringpool.Pool, c Column, persistent bool, v any) uint32 {
	switch v := v.(type) {
	case int:
		return encodeInt(v, c.Size)
	case string:
		return pool.Intern(v, persistent)
	case io.Reader:
		return 1
	}
	return 0
}

var errNilPtr = errors.New("destination pointer is nil")

// convertAssign assigns the raw cell value of column c to dest, reading a
// binary column from rs. A nil rs means the stream is NULL or unresolvable.
func convertAssign(pool *stringpool.Pool, c Column, raw uint32, rs io.ReadSeeker, dest any) error {
	switch c.Type {
	case ColumnInteger:
		return assignInt(dest, c, raw)
	case ColumnString:
		s, _ := pool.Lookup(raw)
		return assignString(dest, c, s, raw == 0)
	case ColumnBinary:
		return assignBinary(dest, c, rs)
	}
	return fmt.Errorf("unsupported column type %d", c.Type)
}

func assignInt(dest any, c Column, raw uint32) error { //nolint:funlen
	size := c.Size
	switch d := dest.(type) {
	case *int:
		return setInt(d, raw, size)
	case *int8:
		return setInt(d, raw, size)
	case *int16:
		return setInt(d, raw, size)
	case *int32:
		return setInt(d, raw, size)
	case *int64:
		return setInt(d, raw, size)
	case *uint:
		return setInt(d, raw, size)
	case *uint8:
		return setInt(d, raw, size)
	case *uint16:
		return setInt(d, raw, size)
	case *uint32:
		return setInt(d, raw, size)
	case *uint64:
		return setInt(d, raw, size)
	case **int:
		return setNullInt(d, raw, size)
	case **int8:
		return setNullInt(d, raw, size)
	case **int16:
		return setNullInt(d, raw, size)
	case **int32:
		return setNullInt(d, raw, size)
	case **int64:
		return setNullInt(d, raw, size)
	case **uint:
		return setNullInt(d, raw, size)
	case **uint8:
		return setNullInt(d, raw, size)
	case **uint16:
		return setNullInt(d, raw, size)
	case **uint32:
		return setNullInt(d, raw, size)
	case **uint64:
		return setNullInt(d, raw, size)
	case *string:
		if d == nil {
			return errNilPtr
		}
		*d = ""
		if raw != 0 {
			*d = strconv.Itoa(decodeInt(raw, size))
		}
		return nil
	case **string:
		if d == nil {
			return errNilPtr
		}
		*d = nil
		if raw != 0 {
			s := strconv.Itoa(decodeInt(raw, size))
			*d = &s
		}
		return nil
	}
	return assignFallback(dest, c, int64(decodeInt(raw, size)), raw == 0)
}

func setInt[T integer](d *T, raw uint32, size int) error {
	if d == nil {
		return errNilPtr
	}
	if raw == 0 {
		return fmt.Errorf("cannot scan NULL into *%T", *d)
	}
	return set(d, int64(decodeInt(raw, size)))
}

func setNullInt[T integer](d **T, raw uint32, size int) error {
	if d == nil {
		return errNilPtr
	}
	*d = nil
	if raw == 0 {
		return nil
	}
	var v T
	if err := setInt(&v, raw, size); err != nil {
		return err
	}
	*d = &v
	return nil
}

func assignString(dest any, c Column, s string, null bool) error { //nolint:funlen
	switch d := dest.(type) {
	case *string:
		if d == nil {
			return errNilPtr
		}
		*d = s
		return nil
	case **string:
		if d == nil {
			return errNilPtr
		}
		*d = nil
		if !null {
			*d = &s
		}
		return nil
	case *[]byte:
		if d == nil {
			return errNilPtr
		}
		*d = nil
		if !null {
			*d = []byte(s)
		}
		return nil
	case *int:
		return parseInt(d, s, null)
	case *int8:
		return parseInt(d, s, null)
	case *int16:
		return parseInt(d, s, null)
	case *int32:
		return parseInt(d, s, null)
	case *int64:
		return parseInt(d, s, null)
	case *uint:
		return parseInt(d, s, null)
	case *uint8:
		return parseInt(d, s, null)
	case *uint16:
		return parseInt(d, s, null)
	case *uint32:
		return parseInt(d, s, null)
	case *uint64:
		return parseInt(d, s, null)
	case **int:
		return parseNullInt(d, s, null)
	case **int8:
		return parseNullInt(d, s, null)
	case **int16:
		return parseNullInt(d, s, null)
	case **int32:
		return parseNullInt(d, s, null)
	case **int64:
		return parseNullInt(d, s, null)
	case **uint:
		return parseNullInt(d, s, null)
	case **uint8:
		return parseNullInt(d, s, null)
	case **uint16:
		return parseNullInt(d, s, null)
	case **uint32:
		return parseNullInt(d, s, null)
	case **uint64:
		return parseNullInt(d, s, null)
	}
	return assignFallback(dest, c, s, null)
}

func parseInt[T integer](d *T, s string, null bool) error {
	if d == nil {
		return errNilPtr
	}
	if null {
		return fmt.Errorf("cannot scan NULL into *%T", *d)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("cannot scan %q into *%T", s, *d)
	}
	return set(d, n)
}

func parseNullInt[T integer](d **T, s string, null bool) error {
	if d == nil {
		return errNilPtr
	}
	*d = nil
	if null {
		return nil
	}
	var v T
	if err := parseInt(&v, s, false); err != nil {
		return err
	}
	*d = &v
	return nil
}

func set[T integer](d *T, n int64) error {
	v := T(n)
	if int64(v) != n || (n < 0 && v > 0) {
		return fmt.Errorf("value %d out of range for *%T", n, *d)
	}
	*d = v
	return nil
}

func assignBinary(dest any, c Column, rs io.ReadSeeker) error {
	switch d := dest.(type) {
	case *io.ReadSeeker:
		if d == nil {
			return errNilPtr
		}
		*d = rs
	case *[]byte:
		if d == nil {
			return errNilPtr
		}
		*d = nil
		if rs != nil {
			b, err := streamBytes(rs)
			if err != nil {
				return err
			}
			*d = b
		}
	case *string:
		if d == nil {
			return errNilPtr
		}
		*d = ""
		if rs != nil {
			b, err := streamBytes(rs)
			if err != nil {
				return err
			}
			*d = string(b)
		}
	default:
		return assignFallback(dest, c, rs, rs == nil)
	}
	return nil
}

// assignFallback assigns src to a destination the type switches did not cover,
// allocating each level of a pointer chain only when the cell is not NULL.
func assignFallback(dest any, c Column, src any, null bool) error { //nolint:funlen,gocognit
	if sc, ok := dest.(sql.Scanner); ok {
		if null {
			return sc.Scan(nil)
		}
		if r, ok := src.(io.ReadSeeker); ok {
			b, err := streamBytes(r)
			if err != nil {
				return err
			}
			src = b
		}
		return sc.Scan(src)
	}
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Pointer {
		return errors.New("destination not a pointer")
	}
	if rv.IsNil() {
		return errNilPtr
	}
	rv = rv.Elem()
	for rv.Kind() == reflect.Pointer {
		if null {
			rv.SetZero()
			return nil
		}
		rv.Set(reflect.New(rv.Type().Elem()))
		rv = rv.Elem()
	}
	if sc, ok := reflect.TypeAssert[sql.Scanner](rv.Addr()); ok {
		return assignFallback(sc, c, src, null)
	}

	switch rv.Kind() {
	case reflect.Interface:
		if null {
			rv.SetZero()
			return nil
		}
		if s := reflect.ValueOf(src); s.IsValid() && s.Type().AssignableTo(rv.Type()) {
			rv.Set(s)
			return nil
		}
	case reflect.String:
		if null {
			rv.SetString("")
			return nil
		}
		switch s := src.(type) {
		case string:
			rv.SetString(s)
			return nil
		case int64:
			rv.SetString(strconv.FormatInt(s, 10))
			return nil
		case io.ReadSeeker:
			b, err := streamBytes(s)
			if err != nil {
				return err
			}
			rv.SetString(string(b))
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n, ok, err := scanInt(src); ok {
			if null {
				return fmt.Errorf("cannot scan NULL into *%s", rv.Type())
			}
			if err != nil {
				return fmt.Errorf("cannot scan %q into *%s", src, rv.Type())
			}
			if rv.OverflowInt(n) {
				return fmt.Errorf("value %d out of range for *%s", n, rv.Type())
			}
			rv.SetInt(n)
			return nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n, ok, err := scanInt(src); ok {
			if null {
				return fmt.Errorf("cannot scan NULL into *%s", rv.Type())
			}
			if err != nil {
				return fmt.Errorf("cannot scan %q into *%s", src, rv.Type())
			}
			if n < 0 || rv.OverflowUint(uint64(n)) {
				return fmt.Errorf("value %d out of range for *%s", n, rv.Type())
			}
			rv.SetUint(uint64(n))
			return nil
		}
	case reflect.Slice:
		if rv.Type().Elem().Kind() != reflect.Uint8 {
			break
		}
		if null {
			rv.SetZero()
			return nil
		}
		switch b := src.(type) {
		case string:
			rv.SetBytes([]byte(b))
			return nil
		case io.ReadSeeker:
			v, err := streamBytes(b)
			if err != nil {
				return err
			}
			rv.SetBytes(v)
			return nil
		}
	}
	return fmt.Errorf("cannot scan %s column into %T", c.Type, dest)
}

func scanInt(src any) (int64, bool, error) {
	switch s := src.(type) {
	case int64:
		return s, true, nil
	case string:
		n, err := strconv.ParseInt(s, 10, 64)
		return n, true, err
	}
	return 0, false, nil
}

func streamBytes(rs io.ReadSeeker) ([]byte, error) {
	size, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(rs, b); err != nil {
		return nil, err
	}
	return b, nil
}

func encodeInt(v, size int) uint32 {
	if size == 2 {
		return uint32(uint16(v) ^ 0x8000)
	}
	return uint32(v) ^ 0x80000000
}

func decodeInt(raw uint32, size int) int {
	if size == 2 {
		return int(int16(raw ^ 0x8000))
	}
	return int(int32(raw ^ 0x80000000))
}
