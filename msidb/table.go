package msidb

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/abemedia/go-msi/internal/streamname"
)

// Table is one table in a [Database]. Obtain one via [Database.Table] or
// [Database.CreateTable].
type Table struct {
	db      *Database
	name    string
	columns []Column
	names   map[string]int
	primary []int
	records []*Record
}

// errUnknownString signals a non-intern string lookup that missed the pool.
var errUnknownString = errors.New("unknown string")

func newTable(db *Database, name string, cols []Column) (*Table, error) {
	if name == "" {
		return nil, errors.New("empty name")
	}
	if _, ok := db.tables[name]; ok {
		return nil, newError("create table", name, ErrExist)
	}
	if n := streamname.EncodedLen(name); n > 30 {
		return nil, fmt.Errorf("name encodes to %d wchars, max 30", n)
	}
	if len(cols) == 0 {
		return nil, errors.New("no columns")
	}
	for _, c := range cols {
		switch c.Type {
		case ColumnInteger:
			if c.Size != 2 && c.Size != 4 {
				return nil, fmt.Errorf("column %q int size %d, want 2 or 4", c.Name, c.Size)
			}
		case ColumnText:
			if c.Size < 0 || c.Size > 255 {
				return nil, fmt.Errorf("column %q string size %d, want 0-255", c.Name, c.Size)
			}
		case ColumnBinary:
		default:
			return nil, fmt.Errorf("column %q unknown type %d", c.Name, c.Type)
		}
	}
	t := &Table{db: db, name: name, columns: cols, names: make(map[string]int, len(cols))}
	for i, c := range cols {
		t.names[c.Name] = i
		if c.PrimaryKey {
			t.primary = append(t.primary, i)
		}
	}
	if len(t.primary) == 0 {
		return nil, errors.New("no primary-key column")
	}
	return t, nil
}

// Name returns the table's name.
func (t *Table) Name() string { return t.name }

// Columns returns an iterator over the table's columns, in column order.
func (t *Table) Columns() iter.Seq[Column] { return slices.Values(t.columns) }

// Len returns the number of records in the table.
func (t *Table) Len() int { return len(t.records) }

// Records returns an iterator over the table's records in primary-key
// order.
func (t *Table) Records() iter.Seq[*Record] {
	return func(yield func(*Record) bool) {
		for _, r := range t.records {
			if !yield(r) {
				return
			}
		}
	}
}

// Record returns the record whose primary-key fields equal key, or
// [ErrNotExist] if no such record exists. key must have one value per
// primary-key column in column order.
func (t *Table) Record(key ...any) (*Record, error) {
	if len(key) != len(t.primary) {
		return nil, newError("record", t.name, fmt.Errorf("expected %d primary-key values, got %d", len(t.primary), len(key)))
	}
	candidate := &Record{table: t, fields: make([]fieldValue, len(t.columns))}
	var err error
	for ki, i := range t.primary {
		if candidate.fields[i], err = t.toFieldValue(t.columns[i], key[ki], false); err != nil {
			if errors.Is(err, errUnknownString) {
				return nil, newError("record", t.name, ErrNotExist)
			}
			return nil, newError("record", t.name+"."+t.columns[i].Name, err)
		}
	}
	idx, found := slices.BinarySearchFunc(t.records, candidate, t.comparePK)
	if !found {
		return nil, newError("record", t.name, ErrNotExist)
	}
	return t.records[idx], nil
}

// Insert inserts a record. values must have one entry per column in
// column order; valid Go types per column are:
//
//   - ColumnInt: int (or any other Go integer type), or nil for NULL
//   - ColumnString: string, or nil for NULL
//   - ColumnBinary: io.Reader or []byte, or nil for NULL
//
// Records are kept in primary-key order. Returns an error on length or
// type mismatch, NULL passed to a non-nullable column, or duplicate
// primary key.
func (t *Table) Insert(values ...any) (*Record, error) {
	if len(values) != len(t.columns) {
		return nil, newError("insert", t.name, fmt.Errorf("got %d values for %d columns", len(values), len(t.columns)))
	}
	r := &Record{table: t, fields: make([]fieldValue, len(t.columns))}
	var err error
	for i, v := range values {
		c := t.columns[i]
		if r.fields[i], err = t.toFieldValue(c, v, true); err != nil {
			r.release()
			return nil, newError("insert", t.name+"."+c.Name, err)
		}
	}
	idx, found := slices.BinarySearchFunc(t.records, r, t.comparePK)
	if found {
		r.release()
		return nil, newError("insert", t.name, ErrExist)
	}
	if err := r.validateStreamName(); err != nil {
		r.release()
		return nil, newError("insert", t.name, err)
	}
	if err := t.db.markDirty(); err != nil {
		r.release()
		return nil, err
	}
	t.records = slices.Insert(t.records, idx, r)
	return r, nil
}

// comparePK orders a and b by their primary-key fields.
func (t *Table) comparePK(a, b *Record) int {
	for _, i := range t.primary {
		af, bf := a.fields[i], b.fields[i]
		if af.null != bf.null {
			if af.null {
				return -1
			}
			return 1
		}
		if d := cmp.Compare(af.num, bf.num); d != 0 {
			return d
		}
	}
	return 0
}

// toFieldValue converts v to a fieldValue for c. intern selects Intern
// (mutating) vs LookupID (read-only).
func (t *Table) toFieldValue(c Column, v any, intern bool) (fieldValue, error) { //nolint:funlen
	if v == nil {
		if !c.Nullable {
			return fieldValue{}, errors.New("NULL not allowed")
		}
		return fieldValue{null: true}, nil
	}
	switch c.Type {
	case ColumnInteger:
		var n int
		var err error
		switch x := v.(type) {
		case int:
			n, err = toInt(x, c.Size)
		case int8:
			n, err = toInt(x, c.Size)
		case int16:
			n, err = toInt(x, c.Size)
		case int32:
			n, err = toInt(x, c.Size)
		case int64:
			n, err = toInt(x, c.Size)
		case uint:
			n, err = toInt(x, c.Size)
		case uint8:
			n, err = toInt(x, c.Size)
		case uint16:
			n, err = toInt(x, c.Size)
		case uint32:
			n, err = toInt(x, c.Size)
		case uint64:
			n, err = toInt(x, c.Size)
		case uintptr:
			n, err = toInt(x, c.Size)
		default:
			return fieldValue{}, fmt.Errorf("expected integer, got %T", v)
		}
		if err != nil {
			return fieldValue{}, err
		}
		return fieldValue{num: uint32(n)}, nil
	case ColumnText:
		s, ok := v.(string)
		if !ok {
			return fieldValue{}, fmt.Errorf("expected string, got %T", v)
		}
		if c.Size > 0 && utf8.RuneCountInString(s) > c.Size {
			return fieldValue{}, fmt.Errorf("length %d exceeds column max %d", utf8.RuneCountInString(s), c.Size)
		}
		if intern {
			id, err := t.db.pool.Intern(s, t.name != systemTableStreams)
			if err != nil {
				return fieldValue{}, err
			}
			return fieldValue{num: id, null: id == 0}, nil
		}
		id, ok := t.db.pool.LookupID(s)
		if !ok {
			return fieldValue{}, errUnknownString
		}
		return fieldValue{num: id, null: id == 0}, nil
	case ColumnBinary:
		if !intern {
			return fieldValue{}, errors.New("binary column not supported for lookup")
		}
		var r io.Reader
		switch x := v.(type) {
		case []byte:
			r = bytes.NewReader(x)
		case io.Reader:
			r = x
		default:
			return fieldValue{}, fmt.Errorf("expected io.Reader or []byte, got %T", v)
		}
		sw := &streamWriter{db: t.db}
		if _, err := io.Copy(sw, r); err != nil {
			return fieldValue{}, err
		}
		src, err := sw.build()
		if err != nil {
			return fieldValue{}, err
		}
		return fieldValue{bin: src}, nil
	}
	return fieldValue{}, fmt.Errorf("unsupported column type %d", c.Type)
}

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
		return 0, fmt.Errorf("value %d out of range for int(%d) column", v, size)
	}

	return int(v), nil
}
