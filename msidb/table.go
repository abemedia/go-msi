package msidb

import (
	"cmp"
	"slices"
	"sync/atomic"
)

type table struct {
	name     string
	cols     []Column
	byName   map[string]int
	keys     []int                // in column order
	holds    int                  // zero releases the temporary columns
	records  [][]uint32           // in raw key order; file order when keyless
	temp     map[*uint32]struct{} // records inserted with INSERT ... TEMPORARY, by backing array
	moved    map[*uint32][]uint32 // records widened under an open Rows, by their old backing array
	hasMoved atomic.Bool
	readers  atomic.Int32
}

func newSystemTable(name string, schema []Column) *table {
	t := &table{name: name, cols: schema, byName: make(map[string]int, len(schema))}
	for i, c := range schema {
		t.byName[c.Name] = i
		if c.PrimaryKey {
			t.keys = append(t.keys, i)
		}
	}
	return t
}

func (t *table) compareKeys(a, b []uint32) int {
	for _, i := range t.keys {
		if d := cmp.Compare(a[i], b[i]); d != 0 {
			return d
		}
	}
	return 0
}

// find locates the record whose key cells equal rec's, returning the
// insertion index when absent.
func (t *table) find(rec []uint32) (int, bool) {
	return slices.BinarySearchFunc(t.records, rec, t.compareKeys)
}

func (t *table) persistent() bool { return !t.cols[0].Temporary }

// cellPersistent reports whether rec's cell in column c reaches the file.
func (t *table) cellPersistent(rec []uint32, c Column) bool {
	if c.Temporary || !t.persistent() {
		return false
	}
	_, temp := t.temp[&rec[0]]
	return !temp
}

// releaseRecord zeroes rec and releases its string references and derived
// stream.
func (db *Database) releaseRecord(t *table, rec []uint32) {
	stream := ""
	if hasBinary(t.cols, rec) {
		stream = db.streamKey(t, rec)
	}
	db.releaseCells(t, rec)
	delete(t.temp, &rec[0])
	if stream != "" {
		db.dropStream(stream)
	}
}

func (db *Database) releaseCells(t *table, rec []uint32) {
	for i := range t.cols {
		db.releaseCell(t, rec, i)
	}
}

func (db *Database) releaseCell(t *table, rec []uint32, i int) {
	id := rec[i]
	rec[i] = 0
	if c := t.cols[i]; c.Type == ColumnString && id != 0 {
		db.pool.Release(id, t.cellPersistent(rec, c))
	}
}

func hasBinary(cols []Column, rec []uint32) bool {
	for i, c := range cols {
		if c.Type == ColumnBinary && rec[i] != 0 {
			return true
		}
	}
	return false
}
