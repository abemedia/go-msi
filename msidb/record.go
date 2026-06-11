package msidb

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"unsafe"

	"github.com/abemedia/go-msi/internal/streamname"
)

// Record is one record in a [Table].
type Record struct {
	table  *Table
	fields []uint32
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
		if fv == 0 {
			return nil, nil //nolint:nilnil // fast path: a 0 cell is null
		}
		name, err := r.streamName()
		if err != nil {
			return nil, err
		}
		src, ok := r.table.db.streams[name]
		if !ok {
			return nil, nil //nolint:nilnil // missing stream reads as null
		}
		return src.open(), nil
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

	cand := &Record{table: t, fields: slices.Clone(r.fields)}
	newFV, payload, err := toFieldValue(cand, i, value)
	if err != nil {
		return newError("set field", t.name+"."+col, err)
	}
	cand.fields[i] = newFV

	// A binary column stages nothing until commit, so its rollback is a no-op.
	fail := func(err error) error {
		if c.Type != ColumnBinary {
			cand.releaseField(c, newFV)
		}
		return newError("set field", t.name+"."+col, err)
	}

	oldIdx, newIdx := -1, -1
	if c.PrimaryKey {
		var found bool
		if newIdx, found = slices.BinarySearchFunc(t.records, cand, t.comparePK); found && t.records[newIdx] != r {
			return fail(ErrExist)
		}
		oldIdx, _ = slices.BinarySearchFunc(t.records, r, t.comparePK)
		if newIdx > oldIdx {
			newIdx--
		}
	}
	if err := cand.validate(); err != nil {
		return fail(err)
	}
	if err := t.db.markDirty(); err != nil {
		return fail(err)
	}

	// Reconcile the backing stream, then adopt cand's fields.
	switch {
	case c.Type == ColumnBinary && newFV != 0:
		// A binary column is never a primary key, so the name is unchanged;
		// createStream replaces any current stream.
		name, err := cand.streamName()
		if err != nil {
			return fail(err)
		}
		if err := t.db.createStream(name, payload); err != nil {
			return fail(err)
		}
	case c.PrimaryKey && slices.ContainsFunc(t.columns, func(col Column) bool {
		return col.Type == ColumnBinary && r.fields[t.names[col.Name]] != 0
	}):
		// A primary-key change moves the stream to the new name.
		oldName, _ := r.streamName()
		newName, _ := cand.streamName()
		if oldName != newName {
			if t.name == systemTableStreams {
				t.db.streams[newName] = t.db.streams[oldName]
				delete(t.db.streams, oldName)
			} else {
				t.db.renameStream(oldName, newName)
			}
		}
		r.releaseField(c, r.fields[i])
	default:
		r.releaseField(c, r.fields[i])
	}

	r.fields = cand.fields
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
	r.release() // frees field resources, including the backing data stream
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
	name, err := r.streamName()
	if err != nil {
		return err
	}
	if n := streamname.EncodedLen(name); n > 30 {
		return fmt.Errorf("stream name %q encodes to %d wchars, max %d", name, n, 30)
	}
	return nil
}

// streamName returns the MSI name of r's data stream: the Name field for a
// _Streams record, otherwise the table name joined with the primary-key values.
func (r *Record) streamName() (string, error) {
	t := r.table

	if t.name == systemTableStreams {
		name, ok := t.db.pool.Lookup(r.fields[0])
		if !ok {
			return "", fmt.Errorf("unknown string ID %d for _Streams Name", r.fields[0])
		}
		return name, nil
	}

	const maxStreamName = 62
	b := make([]byte, 0, maxStreamName)
	b = append(b, t.name...)
	for i, c := range t.columns {
		if !c.PrimaryKey {
			continue
		}
		b = append(b, '.')
		fv := r.fields[i]
		if fv == 0 {
			return "", errors.New("nil primary-key field in stream-name lookup")
		}
		switch c.Type {
		case ColumnString:
			s, ok := t.db.pool.Lookup(fv)
			if !ok {
				return "", fmt.Errorf("unknown string ID %d in primary-key field", fv)
			}
			b = append(b, s...)
		case ColumnInteger:
			b = strconv.AppendInt(b, int64(decodeInt(fv, c.Size)), 10)
		default:
			panic("primary-key column is not integer or string") // unreachable
		}
	}
	return unsafe.String(unsafe.SliceData(b), len(b)), nil
}

// release frees every resource held by r.
func (r *Record) release() {
	for i, c := range r.table.columns {
		r.releaseField(c, r.fields[i])
	}
}

// releaseField frees the resource held by field value fv: a string-pool
// reference, or the backing data stream of a populated binary field.
func (r *Record) releaseField(c Column, fv uint32) {
	switch {
	case c.Type == ColumnString:
		r.table.db.pool.Release(fv, r.table.name != systemTableStreams)
	case c.Type == ColumnBinary && fv != 0:
		name, err := r.streamName()
		if err != nil {
			return
		}
		if r.table.name == systemTableStreams {
			// A _Streams record drops only its bytes; the record itself is removed by its own deletion.
			if src, ok := r.table.db.streams[name]; ok {
				src.delete()
				delete(r.table.db.streams, name)
			}
			return
		}
		r.table.db.deleteStream(name)
	}
}

// check reports an error if r was deleted or its table is no longer usable.
func (r *Record) check() error {
	if r.table == nil {
		return errors.New("msidb: record deleted")
	}
	return r.table.check()
}
