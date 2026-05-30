package msidb

import (
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/stringpool"
)

// Database is a Windows Installer database.
type Database struct {
	cfb    *cfb.Reader
	closer io.Closer

	pool    *stringpool.Pool
	schemas map[string][]Column
	tables  map[string]*Table

	stageDir string
	cleanup  runtime.Cleanup

	path string         // set by Open and Create
	w    io.WriteSeeker // set by New
	tmp  *os.File       // path-based dbs: the .tmp file mutations and Close write through

	dirty  bool
	closed bool
}

// New returns an empty database. [Database.Close] writes the database to
// w starting at offset 0; the caller is responsible for ensuring w is
// empty. For writes to a file path, use [Create].
func New(w io.WriteSeeker) *Database {
	pool, _ := stringpool.New(defaultCodepage)
	db := &Database{
		pool:    pool,
		schemas: map[string][]Column{},
		tables:  map[string]*Table{},
		w:       w,
	}
	db.tables[systemTableStreams], _ = newTable(db, systemTableStreams, schemaStreams)
	_ = db.SetSummaryInformation(SummaryInformation{Codepage: defaultCodepage})
	db.dirty = true
	return db
}

// Create returns an empty database that [Database.Close] writes
// atomically to path.
func Create(path string) (*Database, error) {
	db := New(nil)
	db.path = path
	if err := db.markDirty(); err != nil {
		return nil, err
	}
	return db, nil
}

// Open opens the named MSI database for editing. [Database.Close] writes
// the database back to path if it was mutated.
func Open(path string) (*Database, error) {
	rc, err := cfb.OpenReader(path)
	if err != nil {
		return nil, err
	}
	db, err := decode(rc.Reader)
	if err != nil {
		rc.Close()
		return nil, newError("open", path, err)
	}
	db.cfb = rc.Reader
	db.closer = rc
	db.path = path
	return db, nil
}

// Codepage returns the Windows code page used to store string fields.
func (db *Database) Codepage() uint16 { return db.pool.Codepage() }

// SetCodepage sets the Windows code page used to store string fields.
func (db *Database) SetCodepage(cp uint16) error {
	original := db.pool.Codepage()
	if err := db.pool.SetCodepage(cp); err != nil {
		return newError("set codepage", "", err)
	}
	if err := db.markDirty(); err != nil {
		_ = db.pool.SetCodepage(original) // restore; original was valid
		return err
	}
	return nil
}

// Tables returns an iterator over the database's tables, in name order.
func (db *Database) Tables() iter.Seq[*Table] {
	names := make([]string, 0, len(db.tables))
	for name := range db.tables {
		names = append(names, name)
	}
	slices.Sort(names)
	return func(yield func(*Table) bool) {
		for _, name := range names {
			if !yield(db.tables[name]) {
				return
			}
		}
	}
}

// Table returns the named table, or [ErrNotExist] if no such table exists.
func (db *Database) Table(name string) (*Table, error) {
	if t, ok := db.tables[name]; ok {
		return t, nil
	}
	return nil, newError("table", name, ErrNotExist)
}

// CreateTable adds a table with the given column schema. Returns
// [ErrExist] if a table with the same name already exists.
func (db *Database) CreateTable(name string, cols ...Column) (*Table, error) {
	switch name {
	case systemTableTables, systemTableColumns, systemTableStreams, stringpoolName, stringdataName:
		return nil, newError("create table", name, errors.New("reserved name"))
	}
	t, err := newTable(db, name, slices.Clone(cols))
	if err != nil {
		return nil, newError("create table", name, err)
	}
	if err := db.markDirty(); err != nil {
		return nil, err
	}
	db.tables[name] = t
	db.schemas[name] = t.columns
	if _, err := db.pool.Intern(name, true); err != nil {
		return nil, newError("create table", name, err)
	}
	for _, c := range t.columns {
		if _, err := db.pool.Intern(name, true); err != nil {
			return nil, newError("create table", name, err)
		}
		if _, err := db.pool.Intern(c.Name, true); err != nil {
			return nil, newError("create table", name, err)
		}
	}
	return t, nil
}

// DropTable removes the named table. Returns [ErrNotExist] if no such
// table exists.
//
// Any [*Table] previously returned for this name, and any [*Record] obtained
// from it, become invalid; subsequent method calls on them have unspecified
// behavior.
func (db *Database) DropTable(name string) error {
	t, ok := db.tables[name]
	if !ok {
		return newError("drop table", name, ErrNotExist)
	}
	if err := db.markDirty(); err != nil {
		return err
	}
	for _, r := range t.records {
		r.release()
	}
	nameID, _ := db.pool.LookupID(name)
	db.pool.Release(nameID, true)
	for _, c := range t.columns {
		db.pool.Release(nameID, true)
		colID, _ := db.pool.LookupID(c.Name)
		db.pool.Release(colID, true)
	}
	delete(db.tables, name)
	delete(db.schemas, name)
	*t = Table{}
	return nil
}

// Close persists pending changes, if any, and releases resources.
// Readers obtained from db are not valid after Close.
func (db *Database) Close() error {
	if db.closed {
		return newError("close", "", errors.New("already closed"))
	}
	db.closed = true

	var writeErr error
	switch {
	case !db.dirty:
		if db.tmp != nil {
			db.tmp.Close()
			_ = os.Remove(db.tmp.Name())
		}
	case db.w != nil:
		writeErr = encode(db.w, db)
	default:
		tmpName := db.tmp.Name()
		defer func() { _ = os.Remove(tmpName) }()
		if err := encode(db.tmp, db); err != nil {
			db.tmp.Close()
			writeErr = err
			break
		}
		if err := db.tmp.Close(); err != nil {
			writeErr = err
			break
		}
		if c := db.closer; c != nil {
			db.closer = nil
			if err := c.Close(); err != nil {
				writeErr = err
				break
			}
			db.cfb = nil
		}
		if err := os.Rename(tmpName, db.path); err != nil {
			writeErr = err
		}
	}
	if c := db.closer; c != nil {
		db.closer = nil
		if err := c.Close(); err != nil && writeErr == nil {
			writeErr = err
		}
	}
	db.cfb = nil

	if db.stageDir != "" {
		db.cleanup.Stop()
		_ = os.RemoveAll(db.stageDir)
		db.stageDir = ""
	}

	return writeErr
}

// markDirty prepares the database for mutation. The first call on a
// path-based database creates the temp file the next [Database.Close]
// will encode into and rename over path.
func (db *Database) markDirty() error {
	if db.path != "" && db.tmp == nil {
		tmp, err := os.CreateTemp(filepath.Dir(db.path), filepath.Base(db.path)+".*.tmp")
		if err != nil {
			return err
		}
		db.tmp = tmp
	}
	db.dirty = true
	return nil
}
