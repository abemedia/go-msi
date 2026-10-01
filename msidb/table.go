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

func newTable(name string, cols []Column) *table {
	t := &table{name: name, cols: cols, byName: make(map[string]int, len(cols))}
	for i, c := range cols {
		t.byName[c.Name] = i
		if c.PrimaryKey {
			t.keys = append(t.keys, i)
		}
	}
	return t
}

// columnsRange returns the half-open range of _Columns records describing
// tableID's columns, in Number order.
func (db *Database) columnsRange(tableID uint32) (lo, hi int) {
	recs := db.tables[systemTableColumns].records
	lo, _ = slices.BinarySearchFunc(recs, tableID, func(rec []uint32, id uint32) int {
		if rec[columnsTable] < id {
			return -1
		}
		return 1
	})
	hi = lo
	for hi < len(recs) && recs[hi][columnsTable] == tableID {
		hi++
	}
	return lo, hi
}

// refresh gives t the columns cols, keeping its records.
func (db *Database) refresh(t *table, cols []Column) {
	fresh := newTable(t.name, cols)
	if len(fresh.cols) == len(t.cols) {
		t.cols, t.byName, t.keys = fresh.cols, fresh.byName, fresh.keys
		return
	}

	if t.moved == nil {
		// hasMoved must be set before readers is read. [Rows.Close] does the
		// reverse, decrementing readers before reading hasMoved, so whichever
		// runs second sees the other's write: either no reader is counted and no
		// map is made, or the last Close sees hasMoved and clears the map.
		t.hasMoved.Store(true)
		if t.readers.Load() > 0 {
			t.moved = make(map[*uint32][]uint32, len(t.records))
		} else {
			t.hasMoved.Store(false)
		}
	}
	var slab []uint32
	if len(fresh.cols) > len(t.cols) || t.moved != nil {
		slab = make([]uint32, len(t.records)*len(fresh.cols))
	}
	for i, rec := range t.records {
		for c := len(fresh.cols); c < len(t.cols); c++ {
			db.releaseCell(t, rec, c)
		}
		if slab == nil {
			t.records[i] = rec[:len(fresh.cols)]
			continue
		}
		resized := slab[i*len(fresh.cols):][:len(fresh.cols):len(fresh.cols)]
		copy(resized, rec)
		if _, temp := t.temp[&rec[0]]; temp {
			delete(t.temp, &rec[0])
			t.temp[&resized[0]] = struct{}{}
		}
		if t.moved != nil {
			t.moved[&rec[0]] = resized
		}
		t.records[i] = resized
	}
	t.cols, t.byName, t.keys = fresh.cols, fresh.byName, fresh.keys
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
	if c.Temporary {
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
