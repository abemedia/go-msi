package msidb

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/abemedia/go-msi/internal/streamname"
)

// fieldValue is the in-memory representation of one record field. The
// active payload member is implied by the owning column's type:
//
//   - ColumnInt: num holds the signed value bit-cast to uint32
//   - ColumnString: num holds the string-pool ID
//   - ColumnBinary: bin holds the stream payload
type fieldValue struct {
	num  uint32
	bin  streamSource
	null bool
}

// Record is one record in a [Table].
type Record struct {
	table  *Table
	fields []fieldValue
}

// Field returns the value of the named column as int ([ColumnInteger]), string
// ([ColumnText]), or [io.ReadSeeker] ([ColumnBinary]). Returns a nil
// value and nil error if the column is NULL, or [ErrNotExist] if no
// column with that name exists.
func (r *Record) Field(col string) (any, error) {
	i, ok := r.table.names[col]
	if !ok {
		return nil, newError("field", r.table.name+"."+col, ErrNotExist)
	}
	c := r.table.columns[i]
	fv := r.fields[i]
	if fv.null && c.Nullable {
		return nil, nil //nolint:nilnil
	}
	switch c.Type {
	case ColumnInteger:
		return int(int32(fv.num)), nil
	case ColumnText:
		s, _ := r.table.db.pool.Lookup(fv.num)
		return s, nil
	case ColumnBinary:
		return fv.bin.open(), nil
	}
	return nil, nil //nolint:nilnil
}

// Set updates the named column. value must be int (ColumnInt), string
// (ColumnString), [io.Reader] or []byte (ColumnBinary), or nil for a
// nullable column.
func (r *Record) Set(col string, value any) error {
	t := r.table
	i, ok := t.names[col]
	if !ok {
		return newError("set field", t.name+"."+col, ErrNotExist)
	}
	c := t.columns[i]

	newFV, err := t.toFieldValue(c, value, true)
	if err != nil {
		return newError("set field", t.name+"."+col, err)
	}

	oldIdx, newIdx := -1, -1
	if c.PrimaryKey {
		cand := &Record{table: t, fields: slices.Clone(r.fields)}
		cand.fields[i] = newFV
		var found bool
		if newIdx, found = slices.BinarySearchFunc(t.records, cand, t.comparePK); found && t.records[newIdx] != r {
			r.releaseField(c, newFV)
			return newError("set field", t.name+"."+col, ErrExist)
		}
		if err := cand.validateStreamName(); err != nil {
			r.releaseField(c, newFV)
			return newError("set field", t.name+"."+col, err)
		}
		oldIdx, _ = slices.BinarySearchFunc(t.records, r, t.comparePK)
		if newIdx > oldIdx {
			newIdx--
		}
	}
	if err := t.db.markDirty(); err != nil {
		r.releaseField(c, newFV)
		return err
	}
	old := r.fields[i]
	r.fields[i] = newFV
	if oldIdx >= 0 {
		t.records = slices.Delete(t.records, oldIdx, oldIdx+1)
		t.records = slices.Insert(t.records, newIdx, r)
	}
	r.releaseField(c, old)
	return nil
}

// Delete removes the record from its table.
//
// After this call r is invalid; subsequent method calls on it have
// unspecified behavior.
func (r *Record) Delete() error {
	t := r.table
	idx, found := slices.BinarySearchFunc(t.records, r, t.comparePK)
	if !found || t.records[idx] != r {
		return nil
	}
	if err := t.db.markDirty(); err != nil {
		return err
	}
	r.release()
	t.records = slices.Delete(t.records, idx, idx+1)
	*r = Record{}
	return nil
}

func (r *Record) validateStreamName() error {
	var name string
	var maxLen int
	switch {
	case r.table.name == systemTableStreams:
		v, _ := r.Field("Name")
		name, _ = v.(string)
		maxLen = 31
	case slices.ContainsFunc(r.table.columns, func(c Column) bool { return c.Type == ColumnBinary }):
		var err error
		name, err = r.binaryStreamName()
		if err != nil {
			return err
		}
		maxLen = 30 // 1 wchar reserved for the table marker
	default:
		return nil
	}
	if n := streamname.EncodedLen(name); n > maxLen {
		return fmt.Errorf("stream name %q encodes to %d wchars, max %d", name, n, maxLen)
	}
	return nil
}

// binaryStreamName returns the MSI name (without the table marker) of the
// stream holding r's binary payload.
func (r *Record) binaryStreamName() (string, error) {
	t := r.table
	var b strings.Builder
	b.WriteString(t.name)
	for i, c := range t.columns {
		if !c.PrimaryKey {
			continue
		}
		b.WriteByte('.')
		fv := r.fields[i]
		if fv.null {
			return "", errors.New("nil primary-key field in binary-stream lookup")
		}
		switch c.Type {
		case ColumnText:
			s, ok := t.db.pool.Lookup(fv.num)
			if !ok {
				return "", fmt.Errorf("unknown string ID %d in primary-key field", fv.num)
			}
			b.WriteString(s)
		case ColumnInteger:
			b.WriteString(strconv.Itoa(int(int32(fv.num))))
		default:
			return "", fmt.Errorf("unexpected primary-key type %d", c.Type)
		}
	}
	return b.String(), nil
}

// release frees the per-field pool refs and binary stream handles held by r.
func (r *Record) release() {
	for i, c := range r.table.columns {
		r.releaseField(c, r.fields[i])
	}
}

// releaseField frees the pool ref or binary stream handle held by fv.
func (r *Record) releaseField(c Column, fv fieldValue) {
	switch c.Type {
	case ColumnText:
		r.table.db.pool.Release(fv.num, r.table.name != systemTableStreams)
	case ColumnBinary:
		if fv.bin != nil {
			_ = fv.bin.close()
		}
	}
}
