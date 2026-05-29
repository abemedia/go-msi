package msidb

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"
	"sort"

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
	t := &Table{db: db, name: name, columns: cols, names: make(map[string]int, len(cols))}
	for i, c := range cols {
		switch c.Type {
		case ColumnInteger:
			if c.Size != 2 && c.Size != 4 {
				return nil, fmt.Errorf("column %q int size %d, want 2 or 4", c.Name, c.Size)
			}
		case ColumnString:
			if c.Size < 0 || c.Size > 255 {
				return nil, fmt.Errorf("column %q string size %d, want 0-255", c.Name, c.Size)
			}
		case ColumnBinary:
			if c.PrimaryKey {
				return nil, fmt.Errorf("column %q binary primary key not supported", c.Name)
			}
		case columnInvalid:
			return nil, fmt.Errorf("missing definition for column %d", i+1)
		default:
			return nil, fmt.Errorf("column %q unknown type %d", c.Name, c.Type)
		}
		if _, dup := t.names[c.Name]; dup {
			return nil, fmt.Errorf("duplicate column name %q", c.Name)
		}
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
	if err := t.check(); err != nil {
		return nil, err
	}
	if len(key) != len(t.primary) {
		return nil, newError("record", t.name, fmt.Errorf("expected %d primary-key values, got %d", len(t.primary), len(key)))
	}
	keys := make([]uint32, len(t.primary))
	for k, i := range t.primary {
		c := t.columns[i]
		if key[k] == nil {
			if !c.Nullable {
				return nil, newError("record", t.name+"."+c.Name, errors.New("NULL not allowed"))
			}
			continue // keys[k] stays 0 (NULL)
		}
		switch c.Type {
		case ColumnInteger:
			n, err := intValue(key[k], c.Size)
			if err != nil {
				if errors.Is(err, errOutOfRange) {
					return nil, newError("record", t.name, ErrNotExist) // no record can hold an out-of-range value
				}
				return nil, newError("record", t.name+"."+c.Name, err)
			}
			keys[k] = n
		case ColumnString:
			s, ok := key[k].(string)
			if !ok {
				return nil, newError("record", t.name+"."+c.Name, fmt.Errorf("expected string, got %T", key[k]))
			}
			id, ok := t.db.pool.LookupID(s)
			if !ok {
				return nil, newError("record", t.name, ErrNotExist) // unknown string: no record can match
			}
			keys[k] = id
		default:
			panic("primary-key column is not integer or string") // unreachable
		}
	}
	idx, found := sort.Find(len(t.records), func(j int) int {
		r := t.records[j]
		for k, i := range t.primary {
			if d := cmp.Compare(keys[k], r.fields[i]); d != 0 {
				return d
			}
		}
		return 0
	})
	if !found {
		return nil, newError("record", t.name, ErrNotExist)
	}
	return t.records[idx], nil
}

// Insert inserts a record. values must have one entry per column in
// column order; valid Go types per column are:
//
//   - ColumnInt: int (or any other integer type), or nil for NULL
//   - ColumnString: string, or nil for NULL
//   - ColumnBinary: io.Reader or []byte, or nil for NULL
//
// Records are kept in primary-key order. Returns an error on length or
// type mismatch, NULL passed to a non-nullable column, or duplicate
// primary key.
func (t *Table) Insert(values ...any) (*Record, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	if len(values) != len(t.columns) {
		return nil, newError("insert", t.name, fmt.Errorf("got %d values for %d columns", len(values), len(t.columns)))
	}
	r := &Record{table: t, fields: make([]uint32, len(t.columns))}
	var payload io.Reader
	for i, v := range values {
		fv, rd, err := toFieldValue(r, i, v)
		if err != nil {
			r.release()
			return nil, newError("insert", t.name+"."+t.columns[i].Name, err)
		}
		r.fields[i] = fv
		if rd != nil {
			payload = rd
		}
	}
	idx, found := slices.BinarySearchFunc(t.records, r, t.comparePK)
	if found {
		r.release()
		return nil, newError("insert", t.name, ErrExist)
	}
	if err := r.validate(); err != nil {
		r.release()
		return nil, newError("insert", t.name, err)
	}
	if err := t.db.markDirty(); err != nil {
		r.release()
		return nil, err
	}
	// Insert the record before staging its stream so createStream finds the
	// _Streams record when the table being inserted into is _Streams itself.
	t.records = slices.Insert(t.records, idx, r)
	if payload != nil {
		name, err := r.streamName()
		if err == nil {
			err = t.db.createStream(name, payload)
		}
		if err != nil {
			t.records = slices.Delete(t.records, idx, idx+1)
			r.release()
			return nil, newError("insert", t.name, err)
		}
	}
	return r, nil
}

// comparePK orders a and b by their primary-key fields.
func (t *Table) comparePK(a, b *Record) int {
	for _, i := range t.primary {
		if d := cmp.Compare(a.fields[i], b.fields[i]); d != 0 {
			return d
		}
	}
	return 0
}

// check reports an error if t was dropped or its database is closed.
func (t *Table) check() error {
	if t.db == nil {
		return errors.New("msidb: table dropped")
	}
	if t.db.closed {
		return errClosed
	}
	return nil
}
