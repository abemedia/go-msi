package msidb

import (
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"slices"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/blobstore"
	"github.com/abemedia/go-msi/internal/stringpool"
)

var errClosed = errors.New("msidb: database closed")

// Database is a Windows Installer database.
type Database struct {
	cfb    *cfb.Reader
	closer io.Closer

	pool    *stringpool.Pool
	tables  map[string]*Table
	streams map[string]streamSource // data streams keyed by MSI name; mirrors _Streams

	blob blobstore.Store // staging store for binary columns; backing file is created lazily

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
	db := &Database{
		tables:  map[string]*Table{},
		streams: map[string]streamSource{},
		w:       w,
		dirty:   true,
	}

	// The discarded errors depend only on package constants (a supported code
	// page and a valid system-table schema), so they are always nil here.
	db.pool, _ = stringpool.New(defaultCodepage)
	db.tables[systemTableStreams], _ = newTable(db, systemTableStreams, schemaStreams)
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

// insertStreamsRecord adds a _Streams record for name if none exists yet.
func (db *Database) insertStreamsRecord(name string) {
	t := db.tables[systemTableStreams]
	if id, ok := db.pool.LookupID(name); ok {
		probe := &Record{table: t, fields: []uint32{id, 1}}
		if _, found := slices.BinarySearchFunc(t.records, probe, t.comparePK); found {
			return
		}
	}
	rec := &Record{table: t, fields: []uint32{db.pool.Intern(name, false), 1}}
	idx, _ := slices.BinarySearchFunc(t.records, rec, t.comparePK)
	t.records = slices.Insert(t.records, idx, rec)
}

// removeStreamsRecord removes the _Streams record for name, if present.
func (db *Database) removeStreamsRecord(name string) {
	id, ok := db.pool.LookupID(name)
	if !ok {
		return
	}
	t := db.tables[systemTableStreams]
	probe := &Record{table: t, fields: []uint32{id, 1}}
	if idx, found := slices.BinarySearchFunc(t.records, probe, t.comparePK); found {
		t.records[idx].release()
		t.records = slices.Delete(t.records, idx, idx+1)
	}
}

// createStream stages src into the blob store as the data stream named name,
// inserting a _Streams record for a new name and freeing the stream it replaces.
func (db *Database) createStream(name string, src io.Reader) error {
	h, w, err := db.blob.Create()
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, src); err != nil {
		w.Close()
		db.blob.Delete(h)
		return err
	}
	if err := w.Close(); err != nil {
		db.blob.Delete(h)
		return err
	}
	if old, ok := db.streams[name]; ok {
		old.delete()
	}
	db.streams[name] = &blobStreamSource{store: &db.blob, handle: h}
	db.insertStreamsRecord(name)
	return nil
}

// deleteStream frees the data stream named name and removes its _Streams record.
func (db *Database) deleteStream(name string) {
	src, ok := db.streams[name]
	if !ok {
		return
	}
	src.delete()
	delete(db.streams, name)
	db.removeStreamsRecord(name)
}

// renameStream moves the stream named oldName and its _Streams record to
// newName, keeping the source.
func (db *Database) renameStream(oldName, newName string) {
	src, ok := db.streams[oldName]
	if !ok {
		return
	}
	delete(db.streams, oldName)
	db.removeStreamsRecord(oldName)
	if old, ok := db.streams[newName]; ok {
		old.delete()
	}
	db.streams[newName] = src
	db.insertStreamsRecord(newName)
}

// Codepage returns the Windows code page used to store string fields.
func (db *Database) Codepage() uint16 {
	if db.closed {
		return 0
	}
	return db.pool.Codepage()
}

// SetCodepage sets the Windows code page used to store string fields.
func (db *Database) SetCodepage(cp uint16) error {
	if db.closed {
		return errClosed
	}
	original := db.pool.Codepage()
	if cp == original {
		return nil
	}
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
	if db.closed {
		return nil, errClosed
	}
	if t, ok := db.tables[name]; ok {
		return t, nil
	}
	return nil, newError("table", name, ErrNotExist)
}

// CreateTable adds a table with the given column schema. Returns
// [ErrExist] if a table with the same name already exists.
func (db *Database) CreateTable(name string, cols ...Column) (*Table, error) {
	if db.closed {
		return nil, errClosed
	}
	switch name {
	case systemTableTables, systemTableColumns, systemTableStreams, stringpoolName, stringdataName:
		return nil, newError("create table", name, errors.New("reserved name"))
	}
	t, err := newTable(db, name, slices.Clone(cols))
	if err != nil {
		return nil, newError("create table", name, err)
	}
	if err := db.pool.Validate(name); err != nil {
		return nil, newError("create table", name, err)
	}
	for _, c := range t.columns {
		if err := db.pool.Validate(c.Name); err != nil {
			return nil, newError("create table", name, err)
		}
	}
	if err := db.markDirty(); err != nil {
		return nil, err
	}
	db.pool.Intern(name, true)
	for _, c := range t.columns {
		db.pool.Intern(name, true)
		db.pool.Intern(c.Name, true)
	}
	db.tables[name] = t
	return t, nil
}

// DropTable removes the named table. Returns [ErrNotExist] if no such
// table exists.
func (db *Database) DropTable(name string) error {
	if db.closed {
		return errClosed
	}
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
	*t = Table{}
	return nil
}

// Close persists pending changes, if any, and releases resources.
// Readers obtained from db are not valid after Close.
func (db *Database) Close() error {
	if db.closed {
		return errClosed
	}
	defer func() {
		if db.tmp != nil {
			db.tmp.Close()
			os.Remove(db.tmp.Name())
		}
		if c := db.closer; c != nil {
			c.Close()
		}
		_ = db.blob.Close()
		*db = Database{closed: true}
	}()

	switch {
	case !db.dirty:
		return nil
	case db.w != nil:
		return encode(db.w, db)
	default:
		if err := encode(db.tmp, db); err != nil {
			return err
		}
		if err := db.tmp.Close(); err != nil {
			return err
		}
		tmpPath := db.tmp.Name()
		db.tmp = nil
		if c := db.closer; c != nil {
			if err := c.Close(); err != nil {
				return err
			}
			db.closer = nil
		}
		return os.Rename(tmpPath, db.path)
	}
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
