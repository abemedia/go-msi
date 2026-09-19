package msidb

import (
	"errors"
	"fmt"
	"slices"

	"github.com/abemedia/go-msi/internal/guid"
	"github.com/abemedia/go-msi/internal/streamname"
)

// installerCLSID identifies a compound file as a Windows Installer database.
var installerCLSID = guid.MustParse("000C1084-0000-0000-C000-000000000046")

// tableMarker prefixes every stream name that holds table content.
const tableMarker = "\u4840"

const defaultCodepage = 1252

// Stream names within the table-marker namespace that are not user tables.
const (
	systemTableTables   = "_Tables"
	systemTableColumns  = "_Columns"
	systemTableStreams  = "_Streams"
	systemTableStorages = "_Storages"
	stringPoolName      = "_StringPool"
	stringDataName      = "_StringData"
)

const (
	streamNameCol = "Name"
	streamDataCol = "Data"
)

var (
	schemaTables = []Column{
		{Table: systemTableTables, Name: "Name", Type: ColumnString, Size: 64, PrimaryKey: true},
	}
	schemaColumns = []Column{
		{Table: systemTableColumns, Name: "Table", Type: ColumnString, Size: 64, PrimaryKey: true},
		{Table: systemTableColumns, Name: "Number", Type: ColumnInteger, Size: 2, PrimaryKey: true},
		{Table: systemTableColumns, Name: "Name", Type: ColumnString, Size: 64},
		{Table: systemTableColumns, Name: "Type", Type: ColumnInteger, Size: 2},
	}
	schemaStreams = []Column{
		{Table: systemTableStreams, Name: streamNameCol, Type: ColumnString, Size: 62, PrimaryKey: true},
		{Table: systemTableStreams, Name: streamDataCol, Type: ColumnBinary, Nullable: true},
	}
)

// Cell positions of a _Columns record, matching schemaColumns.
const (
	columnsTable = iota
	columnsNumber
	columnsName
	columnsType
)

func isSystemTable(name string) bool {
	switch name {
	case systemTableTables, systemTableColumns, systemTableStreams:
		return true
	}
	return false
}

func isReadOnlyTable(name string) bool {
	return name == systemTableTables || name == systemTableColumns
}

func isReservedName(name string) bool {
	switch name {
	case systemTableTables, systemTableColumns, systemTableStreams, systemTableStorages, stringPoolName, stringDataName:
		return true
	}
	return false
}

func tableStreamName(name string) string {
	return tableMarker + streamname.Encode(name)
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

// deriveTable derives name's table from its catalog records.
func (db *Database) deriveTable(name string) (*table, error) {
	nameID, ok := db.pool.LookupID(name)
	if !ok || nameID == 0 {
		return nil, errors.New("no _Tables record")
	}
	if _, ok := db.tables[systemTableTables].find([]uint32{nameID}); !ok {
		return nil, errors.New("no _Tables record")
	}
	lo, hi := db.columnsRange(nameID)
	if hi == lo {
		return nil, errors.New("no _Columns records")
	}

	cols := db.tables[systemTableColumns].records[lo:hi]
	t := &table{name: name, cols: make([]Column, 0, len(cols)), byName: make(map[string]int, len(cols))}
	for _, rec := range cols {
		n := len(t.cols) + 1
		if rec[columnsNumber] == 0 || decodeInt(rec[columnsNumber], 2) != n {
			return nil, fmt.Errorf("column %d missing", n)
		}
		colName, ok := db.pool.Lookup(rec[columnsName])
		if !ok || colName == "" {
			return nil, fmt.Errorf("column %d has no name", n)
		}
		if rec[columnsType] == 0 {
			return nil, fmt.Errorf("column %q has no type", colName)
		}
		c, err := unpackType(uint16(decodeInt(rec[columnsType], 2)))
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", colName, err)
		}
		c.Name, c.Table = colName, name
		if c.Type == ColumnBinary && c.PrimaryKey {
			return nil, fmt.Errorf("column %q: binary primary key", colName)
		}
		if _, dup := t.byName[colName]; dup {
			return nil, fmt.Errorf("duplicate column %q", colName)
		}
		t.byName[colName] = len(t.cols)
		if c.PrimaryKey {
			t.keys = append(t.keys, len(t.cols))
		}
		t.cols = append(t.cols, c)
	}
	return t, nil
}

// refresh rebuilds name's derived table after a catalog change, keeping its records.
func (db *Database) refresh(name string) {
	fresh, _ := db.deriveTable(name)
	t := db.tables[name]
	if t == nil {
		db.tables[name] = fresh
		return
	}
	if len(fresh.cols) != len(t.cols) {
		if t.readers.Load() > 0 && t.moved == nil {
			t.moved = make(map[*uint32][]uint32, len(t.records))
			t.hasMoved.Store(true)
		}
		for i, rec := range t.records {
			for c := len(fresh.cols); c < len(t.cols); c++ {
				db.releaseCell(t, rec, c)
			}
			if len(fresh.cols) < len(t.cols) && t.moved == nil {
				t.records[i] = rec[:len(fresh.cols)]
				continue
			}
			resized := make([]uint32, len(fresh.cols))
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
	}
	t.cols, t.byName, t.keys = fresh.cols, fresh.byName, fresh.keys
}

func persistentPrefix(cols []Column) []Column {
	for i, c := range cols {
		if c.Temporary {
			return cols[:i]
		}
	}
	return cols
}

// insertColumnRecord adds the _Columns record describing c as column n
// (1-based) of table.
func (db *Database) insertColumnRecord(table string, n int, c Column) {
	persistent := !c.Temporary
	rec := []uint32{
		db.pool.Intern(table, persistent),
		encodeInt(n, 2),
		db.pool.Intern(c.Name, persistent),
		encodeInt(int(packType(c)), 2),
	}
	ct := db.tables[systemTableColumns]
	i, _ := ct.find(rec)
	ct.records = slices.Insert(ct.records, i, rec)
}

func (db *Database) releaseColumnRecord(rec []uint32) {
	persistent := rec[columnsType]&typePersistent != 0
	tid, cid := rec[columnsTable], rec[columnsName]
	clear(rec)
	db.pool.Release(tid, persistent)
	db.pool.Release(cid, persistent)
}

// tableRecordPersistent reports whether the _Tables record naming nameID is
// written by encode.
func (db *Database) tableRecordPersistent(nameID uint32) bool {
	lo, hi := db.columnsRange(nameID)
	if hi == lo {
		return true // orphan records round-trip
	}
	recs := db.tables[systemTableColumns].records
	for i := lo; i < hi; i++ {
		if recs[i][columnsType]&typePersistent != 0 {
			return true
		}
	}
	return false
}
