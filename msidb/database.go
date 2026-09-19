// Package msidb provides SQL access to the tables and streams of a Windows
// Installer database.
package msidb

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/blobstore"
	"github.com/abemedia/go-msi/internal/sql"
	"github.com/abemedia/go-msi/internal/stringpool"
)

// Database is a Windows Installer database. It is safe for concurrent use.
type Database struct {
	mu sync.RWMutex

	pool     *stringpool.Pool
	tables   map[string]*table       // the system tables plus every valid derived user table
	storages []*cfb.Storage          // opaque preserved sub-storages, in file order
	sources  map[uint32]streamSource // the file's stream namespace, by name id
	blob     blobstore.Store         // staging store for new stream payloads

	clsid [16]byte // root storage CLSID, written back as read
	cfb   *cfb.ReadCloser
	path  string
	w     io.WriteSeeker

	dirty  bool // pending changes; Close writes back
	closed bool
}

// newDatabase returns a database with the system tables but no string pool.
func newDatabase() *Database {
	db := &Database{
		tables:  make(map[string]*table, 48),
		sources: map[uint32]streamSource{},
		clsid:   installerCLSID,
	}
	db.tables[systemTableTables] = newSystemTable(systemTableTables, schemaTables)
	db.tables[systemTableColumns] = newSystemTable(systemTableColumns, schemaColumns)
	db.tables[systemTableStreams] = newSystemTable(systemTableStreams, schemaStreams)
	return db
}

// New returns an empty database. [Database.Close] writes the database to w
// starting at offset 0; the caller ensures w is empty. For writes to a file
// path, use [Create].
func New(w io.WriteSeeker) *Database {
	db := newDatabase()
	db.pool, _ = stringpool.New(defaultCodepage) // the default code page is supported
	db.dirty = true
	db.w = w
	return db
}

// Create returns an empty database that [Database.Close] writes atomically
// to path.
func Create(path string) (*Database, error) {
	db := newDatabase()
	db.pool, _ = stringpool.New(defaultCodepage) // the default code page is supported
	db.dirty = true
	db.path = path
	return db, nil
}

// Open opens the named MSI database for editing. [Database.Close] writes the
// database back to path if it was mutated.
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
	db.cfb = rc
	db.path = path
	return db, nil
}

// Codepage returns the Windows code page used to store string fields.
func (db *Database) Codepage() uint16 {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return 0
	}
	return db.pool.Codepage()
}

// SetCodepage sets the Windows code page used to store string fields.
func (db *Database) SetCodepage(cp uint16) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return errClosed
	}
	if cp == db.pool.Codepage() {
		return nil
	}
	if err := db.pool.SetCodepage(cp); err != nil {
		return newError("set codepage", "", err)
	}
	db.dirty = true
	return nil
}

// Query runs a statement and returns its rows. Any statement is accepted: a
// non-SELECT executes and returns an empty, drainable [Rows].
//
// Query always returns a non-nil Rows. The same error is reported by
// [Rows.Err], so callers may ignore the error returned here.
func (db *Database) Query(query string, args ...any) (*Rows, error) {
	rows, _ := db.run(query, args)
	return rows, rows.err
}

// QueryRow runs a statement that is expected to return at most one record.
// QueryRow always returns a non-nil [Row]; errors are deferred until
// [Row.Scan] is called.
func (db *Database) QueryRow(query string, args ...any) *Row {
	rows, _ := db.run(query, args)
	return &Row{rows: rows}
}

// Exec runs a statement and returns its [Result]. A SELECT yields a zero
// Result. On error the Result still reports the records affected before the
// failure; mutations are not rolled back.
func (db *Database) Exec(query string, args ...any) (Result, error) {
	rows, res := db.run(query, args)
	rows.Close()
	return res, rows.err
}

// run parses and executes one statement, always returning a usable *Rows.
func (db *Database) run(query string, args []any) (*Rows, Result) {
	rows := &Rows{db: db, pos: -1}
	stmt, err := sql.Parse(query)
	if err != nil {
		rows.fatal(parseError(query, err))
		return rows, Result{}
	}

	if sel, ok := stmt.(*sql.Select); ok {
		db.mu.RLock()
		defer db.mu.RUnlock()
		if db.closed {
			rows.fatal(errClosed)
			return rows, Result{}
		}
		db.execSelect(rows, sel, args)
		if rows.buf != nil {
			for _, t := range rows.tabs {
				t.readers.Add(1)
			}
		}
		return rows, Result{}
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		rows.fatal(errClosed)
		return rows, Result{}
	}
	var res Result
	switch s := stmt.(type) {
	case *sql.Insert:
		res, err = db.execInsert(s, args)
	case *sql.Update:
		res, err = db.execUpdate(s, args)
	case *sql.Delete:
		res, err = db.execDelete(s, args)
	case *sql.CreateTable:
		err = db.execCreate(s)
	case *sql.AlterTable:
		err = db.execAlter(s)
	case *sql.DropTable:
		err = db.execDrop(s)
	default:
		err = fmt.Errorf("msidb: unsupported statement %T", stmt)
	}
	if err != nil {
		rows.fatal(err)
		return rows, res
	}
	return rows, res
}

// Close persists pending changes, if any, and releases resources. Readers
// obtained from db are not valid after Close.
func (db *Database) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	db.closed = true

	err := db.flush()
	if db.cfb != nil {
		db.cfb.Close()
		db.cfb = nil
	}
	_ = db.blob.Close()
	db.pool, db.tables, db.storages, db.sources = nil, nil, nil, nil
	return err
}

func (db *Database) flush() (err error) {
	if !db.dirty {
		return nil
	}
	if db.path == "" {
		return encode(db.w, db)
	}

	tmpPath := db.path + ".tmp"
	tmp, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()
	if info, statErr := os.Stat(db.path); statErr == nil {
		if err = tmp.Chmod(info.Mode().Perm()); err != nil {
			return err
		}
	}
	if err = encode(tmp, db); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// Windows cannot rename over an open file.
	if db.cfb != nil {
		if err = db.cfb.Close(); err != nil {
			return err
		}
		db.cfb = nil
	}
	return os.Rename(tmpPath, db.path)
}
