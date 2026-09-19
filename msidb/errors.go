package msidb

import (
	"errors"
	"fmt"
	"strconv"

	internalsql "github.com/abemedia/go-msi/internal/sql"
)

// Sentinel errors, comparable with [errors.Is].
var (
	// ErrFormat is returned when a stream's bytes don't conform to the MSI format.
	ErrFormat = errors.New("msidb: not a valid MSI database")

	// ErrExist is returned when a name or primary key is already in use.
	ErrExist = errors.New("already exists")

	// ErrNotExist is returned when a referenced name does not exist.
	ErrNotExist = errors.New("does not exist")

	// ErrNoRows is returned by [Row.Scan] when a query selected no rows.
	ErrNoRows = errors.New("msidb: no rows in result set")
)

// errClosed is returned by operations on a closed database.
var errClosed = errors.New("msidb: database closed")

// errArgCount reports a mismatch between ? placeholders and supplied arguments.
var errArgCount = errors.New("argument count mismatch")

// Error reports a [Database] operation that failed on a named table or stream.
type Error struct {
	Op   string // "open", "query", "exec", ...
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

func newColError(op string, t *table, c Column, err error) *Error {
	return newError(op, t.name+"."+c.Name, err)
}

// ParseError reports a SQL syntax error at a byte offset into the query.
type ParseError struct {
	SQL string
	Pos int
	msg string
	err error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("msidb: parse error at offset %d: %s", e.Pos, e.msg)
}

func (e *ParseError) Unwrap() error { return e.err }

func parseError(sql string, err error) error {
	if se, ok := errors.AsType[*internalsql.Error](err); ok {
		return &ParseError{SQL: sql, Pos: se.Pos, msg: se.Msg, err: err}
	}
	return &ParseError{SQL: sql, msg: err.Error(), err: err}
}
