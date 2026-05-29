package msidb

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/abemedia/go-msi/internal/streamname"
)

// Record is one record in a [Table].
type Record struct {
	table  *Table
	fields []uint32
	bin    streamSource
}

// Field returns the value of the named column as int ([ColumnInteger]), string
// ([ColumnString]), or [io.ReadSeeker] ([ColumnBinary]). Returns a nil
// value and nil error if the column is NULL, or [ErrNotExist] if no
// column with that name exists.
func (r *Record) Field(col string) (any, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	i, ok := r.table.names[col]
	if !ok {
		return nil, newError("field", r.table.name+"."+col, ErrNotExist)
	}
	c := r.table.columns[i]
	fv := r.fields[i]
	if fv == 0 && c.Nullable {
		return nil, nil //nolint:nilnil
	}
	switch c.Type {
	case ColumnInteger:
		return decodeInt(fv, c.Size), nil
	case ColumnString:
		s, _ := r.table.db.pool.Lookup(fv)
		return s, nil
	case ColumnBinary:
		return r.bin.open(), nil
	}
	return nil, nil //nolint:nilnil
}

// Set updates the named column. value must be int (ColumnInt), string
// (ColumnString), [io.Reader] or []byte (ColumnBinary), or nil for a
// nullable column.
func (r *Record) Set(col string, value any) error {
	if err := r.check(); err != nil {
		return err
	}
	t := r.table
	i, ok := t.names[col]
	if !ok {
		return newError("set field", t.name+"."+col, ErrNotExist)
	}
	c := t.columns[i]

	cand := &Record{table: t, fields: slices.Clone(r.fields), bin: r.bin}
	newFV, err := toFieldValue(cand, i, value)
	if err != nil {
		return newError("set field", t.name+"."+col, err)
	}
	cand.fields[i] = newFV

	oldIdx, newIdx := -1, -1
	if c.PrimaryKey {
		var found bool
		if newIdx, found = slices.BinarySearchFunc(t.records, cand, t.comparePK); found && t.records[newIdx] != r {
			cand.releaseField(c, newFV)
			return newError("set field", t.name+"."+col, ErrExist)
		}
		oldIdx, _ = slices.BinarySearchFunc(t.records, r, t.comparePK)
		if newIdx > oldIdx {
			newIdx--
		}
	}
	if err := cand.validate(); err != nil {
		cand.releaseField(c, newFV)
		return newError("set field", t.name+"."+col, err)
	}
	if err := t.db.markDirty(); err != nil {
		cand.releaseField(c, newFV)
		return err
	}

	r.releaseField(c, r.fields[i])
	r.fields = cand.fields
	r.bin = cand.bin
	if oldIdx >= 0 && newIdx != oldIdx {
		// Rotate r from oldIdx to newIdx in place: shift the elements between
		// them by one slot and overwrite newIdx.
		if newIdx < oldIdx {
			copy(t.records[newIdx+1:oldIdx+1], t.records[newIdx:oldIdx])
		} else {
			copy(t.records[oldIdx:newIdx], t.records[oldIdx+1:newIdx+1])
		}
		t.records[newIdx] = r
	}
	return nil
}

// Delete removes the record from its table.
func (r *Record) Delete() error {
	if err := r.check(); err != nil {
		return err
	}
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

// validate enforces r's per-mutation invariants.
func (r *Record) validate() error {
	if r.table.name == systemTableStreams {
		v, _ := r.Field("Name")
		name, _ := v.(string)
		if name == "" {
			return errors.New("_Streams record requires a non-empty Name")
		}
		if n := streamname.EncodedLen(name); n > 31 {
			return fmt.Errorf("stream name %q encodes to %d wchars, max %d", name, n, 31)
		}
		return nil
	}

	var binCount int
	var hasBinaryCol bool
	for i, c := range r.table.columns {
		if c.Type != ColumnBinary {
			continue
		}
		hasBinaryCol = true
		if r.fields[i] == 0 {
			continue
		}
		if binCount++; binCount > 1 {
			return errors.New("multiple binary columns populated; only one allowed per record")
		}
	}
	if !hasBinaryCol {
		return nil
	}
	name, err := r.binaryStreamName()
	if err != nil {
		return err
	}
	if n := streamname.EncodedLen(name); n > 30 {
		return fmt.Errorf("stream name %q encodes to %d wchars, max %d", name, n, 30)
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
		if fv == 0 {
			return "", errors.New("nil primary-key field in binary-stream lookup")
		}
		switch c.Type {
		case ColumnString:
			s, ok := t.db.pool.Lookup(fv)
			if !ok {
				return "", fmt.Errorf("unknown string ID %d in primary-key field", fv)
			}
			b.WriteString(s)
		case ColumnInteger:
			b.WriteString(strconv.Itoa(decodeInt(fv, c.Size)))
		default:
			panic("primary-key column is not integer or string") // unreachable
		}
	}
	return b.String(), nil
}

// release frees every resource held by r.
func (r *Record) release() {
	for i, c := range r.table.columns {
		r.releaseField(c, r.fields[i])
	}
}

// releaseField frees the string-pool ref or binary payload held by field value fv.
func (r *Record) releaseField(c Column, fv uint32) {
	switch c.Type {
	case ColumnString:
		r.table.db.pool.Release(fv, r.table.name != systemTableStreams)
	case ColumnBinary:
		if fv != 0 && r.bin != nil {
			r.bin.delete()
			r.bin = nil
		}
	}
}

// check reports an error if r was deleted or its table is no longer usable.
func (r *Record) check() error {
	if r.table == nil {
		return errors.New("msidb: record deleted")
	}
	return r.table.check()
}
