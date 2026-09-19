package msidb

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/abemedia/go-msi/internal/sql"
	"github.com/abemedia/go-msi/internal/streamname"
)

// execCreate creates a table; a failed CREATE creates nothing.
func (db *Database) execCreate(s *sql.CreateTable) error {
	name := s.Table
	if isReservedName(name) {
		return newError("create table", name, errors.New("reserved name"))
	}
	if _, ok := db.tables[name]; ok {
		return newError("create table", name, ErrExist)
	}
	if n := streamname.EncodedLen(name); n > 30 {
		return newError("create table", name, fmt.Errorf("name encodes to %d wchars, max 30", n))
	}
	if i := strings.IndexAny(name, cfbForbidden); i >= 0 {
		return newError("create table", name, fmt.Errorf("name contains invalid character %q", name[i]))
	}
	if err := db.pool.Validate(name); err != nil {
		return newError("create table", name, err)
	}
	if len(s.PrimaryKey) == 0 {
		return newError("create table", name, errors.New("no primary key"))
	}

	cols, err := db.buildColumns(s)
	if err != nil {
		return newError("create table", name, err)
	}
	if !cols[0].Temporary && slices.ContainsFunc(cols, func(c Column) bool { return c.Temporary && c.PrimaryKey }) {
		return newError("create table", name, errors.New("temporary primary key in a persistent table"))
	}

	if !s.Hold {
		// Without HOLD, temporary columns are silently not created.
		cols = persistentPrefix(cols)
		if len(cols) == 0 {
			return nil
		}
	}
	persistent := len(persistentPrefix(cols)) > 0

	tableID := db.pool.Intern(name, persistent)
	t := db.tables[systemTableTables]
	i, _ := t.find([]uint32{tableID})
	t.records = slices.Insert(t.records, i, []uint32{tableID})
	for i, c := range cols {
		db.insertColumnRecord(name, i+1, c)
	}
	db.refresh(name)
	if s.Hold {
		db.tables[name].holds++
	}
	db.dirty = true
	return nil
}

func (db *Database) buildColumns(s *sql.CreateTable) ([]Column, error) {
	pk := make(map[string]bool, len(s.PrimaryKey))
	for _, k := range s.PrimaryKey {
		pk[k] = true
	}
	cols := make([]Column, 0, len(s.Columns))
	seen := make(map[string]bool, len(s.Columns))
	for _, d := range s.Columns {
		c, err := columnFromDef(d, pk[d.Name])
		if err != nil {
			return nil, err
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("duplicate column %q", c.Name)
		}
		seen[c.Name] = true
		if err := db.pool.Validate(c.Name); err != nil {
			return nil, err
		}
		if !c.Temporary && len(cols) > 0 && cols[len(cols)-1].Temporary {
			return nil, errors.New("temporary columns must follow persistent ones")
		}
		cols = append(cols, c)
	}
	for _, k := range s.PrimaryKey {
		if !seen[k] {
			return nil, fmt.Errorf("unknown primary key column %q", k)
		}
	}
	return cols, nil
}

func (db *Database) execAlter(s *sql.AlterTable) error {
	if isSystemTable(s.Table) {
		return newError("alter table", s.Table, errors.New("cannot alter a system table"))
	}
	t, ok := db.tables[s.Table]
	if !ok {
		return newError("alter table", s.Table, ErrNotExist)
	}
	switch s.Action {
	case sql.AlterHold:
		t.holds++
		return nil
	case sql.AlterFree:
		if t.holds == 0 {
			return nil // FREE without a hold is an accepted no-op
		}
		t.holds--
		if t.holds == 0 {
			db.releaseTemp(t)
		}
		return nil
	case sql.AlterAdd:
		if _, dup := t.byName[s.Add.Name]; dup {
			return newError("alter table", t.name+"."+s.Add.Name, ErrExist)
		}
		c, err := columnFromDef(*s.Add, false)
		if err != nil {
			return newError("alter table", t.name, err)
		}
		if err := db.pool.Validate(c.Name); err != nil {
			return newError("alter table", t.name, err)
		}
		if !c.Temporary && t.cols[len(t.cols)-1].Temporary {
			return newError("alter table", t.name, errors.New("cannot add a persistent column behind temporary ones"))
		}
		if s.Hold {
			t.holds++
		} else if c.Temporary && t.holds == 0 {
			return nil // Without a hold a temporary column is silently not created.
		}
		db.insertColumnRecord(t.name, len(t.cols)+1, c)
		db.refresh(t.name)
		db.dirty = true
		return nil
	}
	return newError("alter table", s.Table, errors.New("unsupported ALTER action"))
}

// releaseTemp drops t's temporary columns, or the whole table when it has
// no persistent ones.
func (db *Database) releaseTemp(t *table) {
	if !t.persistent() {
		db.dropTable(t)
		return
	}
	nameID, _ := db.pool.LookupID(t.name)
	ct := db.tables[systemTableColumns]
	lo, hi := db.columnsRange(nameID)
	w := lo
	for i := lo; i < hi; i++ {
		rec := ct.records[i]
		if rec[columnsType]&typePersistent != 0 {
			ct.records[w] = rec
			w++
			continue
		}
		db.releaseColumnRecord(rec)
	}
	ct.records = slices.Delete(ct.records, w, hi)
	db.refresh(t.name)
}

func (db *Database) execDrop(s *sql.DropTable) error {
	if isSystemTable(s.Table) {
		return newError("drop table", s.Table, errors.New("cannot drop a system table"))
	}
	t, ok := db.tables[s.Table]
	if !ok {
		return newError("drop table", s.Table, ErrNotExist)
	}
	db.dropTable(t)
	db.dirty = true
	return nil
}

func (db *Database) dropTable(t *table) {
	for _, rec := range t.records {
		db.releaseRecord(t, rec)
	}
	t.records = nil

	nameID, _ := db.pool.LookupID(t.name)
	ct := db.tables[systemTableColumns]
	lo, hi := db.columnsRange(nameID)
	for _, rec := range ct.records[lo:hi] {
		db.releaseColumnRecord(rec)
	}
	ct.records = slices.Delete(ct.records, lo, hi)

	tt := db.tables[systemTableTables]
	if i, found := tt.find([]uint32{nameID}); found {
		clear(tt.records[i])
		tt.records = slices.Delete(tt.records, i, i+1)
		db.pool.Release(nameID, t.persistent())
	}

	delete(db.tables, t.name)
}

func columnFromDef(d sql.ColumnDef, pk bool) (Column, error) {
	c := Column{Name: d.Name, Nullable: !d.NotNull, Localizable: d.Localizable, Temporary: d.Temporary, PrimaryKey: pk}
	switch d.Type {
	case sql.TypeChar, sql.TypeLongChar:
		c.Type = ColumnString
		c.Size = d.Size
		if c.Size < 0 || c.Size > 255 {
			return Column{}, fmt.Errorf("column %q string size %d, want 0-255", c.Name, c.Size)
		}
	case sql.TypeShort, sql.TypeInt:
		c.Type = ColumnInteger
		c.Size = 2
	case sql.TypeLong:
		c.Type = ColumnInteger
		c.Size = 4
	case sql.TypeObject:
		c.Type = ColumnBinary
		if pk {
			return Column{}, fmt.Errorf("column %q: binary primary key", c.Name)
		}
	default:
		return Column{}, fmt.Errorf("invalid column type for %q", d.Name)
	}
	return c, nil
}
