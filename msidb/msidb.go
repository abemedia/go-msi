// Package msidb provides low-level access to MSI database tables, records,
// and streams.
package msidb

import (
	"errors"
	"strconv"

	"github.com/abemedia/go-msi/internal/guid"
)

// tableMarker is the codepoint prefixing every stream name that holds
// table content or binary payloads.
const tableMarker = "\u4840"

// defaultCodepage is the code page used for new databases.
const defaultCodepage = 1252

// Stream names within the table-marker namespace that are not user tables.
const (
	systemTableTables  = "_Tables"
	systemTableColumns = "_Columns"
	systemTableStreams = "_Streams"
	stringpoolName     = "_StringPool"
	stringdataName     = "_StringData"
)

// Schemas of the system tables.
var (
	schemaTables = []Column{
		{Name: "Name", Type: ColumnText, Size: 64, PrimaryKey: true}, //nolint:goconst
	}
	schemaColumns = []Column{
		{Name: "Table", Type: ColumnText, Size: 64, PrimaryKey: true},
		{Name: "Number", Type: ColumnInteger, Size: 2, PrimaryKey: true},
		{Name: "Name", Type: ColumnText, Size: 64},
		{Name: "Type", Type: ColumnInteger, Size: 2},
	}
	schemaStreams = []Column{
		{Name: "Name", Type: ColumnText, Size: 62, PrimaryKey: true},
		{Name: "Data", Type: ColumnBinary, Nullable: true},
	}
)

// installerCLSID identifies a compound file as a Windows Installer database.
var installerCLSID = guid.MustParse("000C1084-0000-0000-C000-000000000046")

// ErrFormat is returned when a stream's bytes don't conform to the MSI format.
var ErrFormat = errors.New("msidb: not a valid MSI database")

// ErrExist is returned when a name is already in use.
var ErrExist = errors.New("already exists")

// ErrNotExist is returned when a referenced name does not exist.
var ErrNotExist = errors.New("does not exist")

// Error reports a [Database] operation that failed on a named table or
// stream.
type Error struct {
	Op   string // "create table", "drop table", "create stream", ...
	Name string
	Err  error
}

func (e *Error) Error() string {
	if e.Name == "" {
		return "msidb: " + e.Op + ": " + e.Err.Error()
	}
	return "msidb: " + e.Op + " " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

func newError(op, name string, err error) *Error {
	return &Error{Op: op, Name: name, Err: err}
}
