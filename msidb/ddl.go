package msidb

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/abemedia/go-msi/internal/sql"
	"github.com/abemedia/go-msi/internal/streamname"
)

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

	cols := make([]Column, 0, len(s.Columns))
	index := make(map[string]int, len(s.Columns))
	persistent := 0
	for _, d := range s.Columns {
		c, err := columnFromDef(name, d, slices.Contains(s.PrimaryKey, d.Name))
		if err != nil {
			return newError("create table", name, err)
		}
		if _, ok := index[c.Name]; ok {
			return newError("create table", name, fmt.Errorf("duplicate column %q", c.Name))
		}
		index[c.Name] = len(cols)
		if err := db.pool.Validate(c.Name); err != nil {
			return newError("create table", name, err)
		}
		if !c.Temporary {
			persistent++
		}
		cols = append(cols, c)
	}
	for i, k := range s.PrimaryKey {
		if slices.Contains(s.PrimaryKey[:i], k) {
			return newError("create table", name, fmt.Errorf("duplicate primary key column %q", k))
		}
		j, ok := index[k]
		if !ok {
			return newError("create table", name, fmt.Errorf("unknown primary key column %q", k))
		}
		if cols[j].Temporary && persistent > 0 {
			return newError("create table", name, errors.New("temporary primary key in a persistent table"))
		}
		cols[i], cols[j] = cols[j], cols[i]
		index[cols[i].Name], index[cols[j].Name] = i, j
	}
	if slices.ContainsFunc(cols[:persistent], func(c Column) bool { return c.Temporary }) {
		return newError("create table", name, errors.New("temporary columns must follow persistent ones"))
	}
	if persistent > maxPersistentColumns || len(cols) > maxColumns {
		return newError("create table", name, errors.New("too many columns"))
	}

	if !s.Hold {
		// Without HOLD, temporary columns are silently not created.
		cols = cols[:persistent]
		if len(cols) == 0 {
			return nil
		}
	}

	tableID := db.pool.Intern(name, persistent > 0)
	t := db.tables[systemTableTables]
	i, _ := t.find([]uint32{tableID})
	t.records = slices.Insert(t.records, i, []uint32{tableID})
	for i, c := range cols {
		db.insertColumnRecord(name, i+1, c)
	}
	db.tables[name] = newTable(name, cols)
	if s.Hold {
		db.tables[name].holds++
	}
	db.dirty = true
	return nil
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
		if t.holds > 0 {
			return nil
		}
		if !t.persistent() {
			db.dropTable(t)
			return nil
		}
		i := slices.IndexFunc(t.cols, func(c Column) bool { return c.Temporary })
		if i < 0 {
			return nil
		}
		nameID, _ := db.pool.LookupID(t.name)
		ct := db.tables[systemTableColumns]
		lo, hi := db.columnsRange(nameID)
		for _, rec := range ct.records[lo+i : hi] {
			db.releaseColumnRecord(rec)
		}
		ct.records = slices.Delete(ct.records, lo+i, hi)
		db.refresh(t, t.cols[:i])
		return nil
	case sql.AlterAdd:
		if _, ok := t.byName[s.Add.Name]; ok {
			return newError("alter table", t.name+"."+s.Add.Name, ErrExist)
		}
		c, err := columnFromDef(t.name, *s.Add, false)
		if err != nil {
			return newError("alter table", t.name, err)
		}
		if err := db.pool.Validate(c.Name); err != nil {
			return newError("alter table", t.name, err)
		}
		if !c.Temporary && t.cols[len(t.cols)-1].Temporary {
			return newError("alter table", t.name, errors.New("cannot add a persistent column behind temporary ones"))
		}
		// Past the check above, a persistent column is only added behind persistent ones.
		if n := len(t.cols) + 1; n > maxColumns || !c.Temporary && n > maxPersistentColumns {
			return newError("alter table", t.name, errors.New("too many columns"))
		}
		if s.Hold {
			t.holds++
		} else if c.Temporary && t.holds == 0 {
			return nil // Without a hold a temporary column is silently not created.
		}
		db.insertColumnRecord(t.name, len(t.cols)+1, c)
		db.refresh(t, append(t.cols, c))
		db.dirty = true
		return nil
	}
	return newError("alter table", s.Table, errors.New("unsupported ALTER action"))
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

func columnFromDef(table string, d sql.ColumnDef, pk bool) (Column, error) {
	c := Column{
		Table:       table,
		Name:        d.Name,
		Nullable:    !d.NotNull,
		Localizable: d.Localizable,
		Temporary:   d.Temporary,
		PrimaryKey:  pk,
	}
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
