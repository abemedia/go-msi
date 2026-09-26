package msidb

import (
	"errors"
	"fmt"
	"io"
)

// Rows is the result of a query. Call [Rows.Next] to advance from record to
// record.
//
// Membership and order are fixed when the query executes; values resolve
// live as they are read. A record deleted under an open Rows reads as all
// NULL.
//
// Calls to a Rows method are not safe for concurrent use, but separate Rows
// on the same [Database] are; a mutation under an open Rows is also safe.
type Rows struct {
	db   *Database
	res  []boundCol
	tabs []*table
	buf  [][]uint32 // gathered tuples, flat with stride len(tabs)
	pos  int        // current tuple; -1 before the first Next
	err  error
}

// Next advances to the next record, reporting whether one is available. It
// closes the rows at the end of the result.
func (r *Rows) Next() bool {
	if r.err != nil {
		return false
	}
	next := r.pos + 1
	if r.buf == nil || (next+1)*len(r.tabs) > len(r.buf) {
		r.Close()
		return false
	}
	r.pos = next
	return true
}

// Scan copies the current record's columns into dest, by position. The
// destination must suit the column type:
//
//   - integer: *int or a sized integer type
//   - string: *string or *[]byte
//   - binary: *io.ReadSeeker, which reads lazily, or *[]byte or *string
//
// A named type with one of those underlying types works too, as does any
// [database/sql.Scanner].
//
// A NULL integer is an error and a NULL string scans as ""; a pointer to any
// of the above, such as **int, reads NULL as nil.
func (r *Rows) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.pos < 0 || r.buf == nil {
		return errors.New("msidb: Scan called before Next")
	}
	if len(dest) != len(r.res) {
		return fmt.Errorf("msidb: Scan got %d destinations for %d columns", len(dest), len(r.res))
	}
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()
	if r.db.closed {
		return errClosed
	}
	for i, d := range dest {
		res := r.res[i]
		rec := r.record(res.slot)
		var raw uint32
		if res.idx < len(rec) {
			raw = rec[res.idx]
		}
		var rs io.ReadSeeker
		if res.c.Type == ColumnBinary && raw != 0 {
			rs = r.db.openStream(r.tabs[res.slot], rec)
		}
		if err := convertAssign(r.db.pool, res.c, raw, rs, d); err != nil {
			return newError("scan", res.c.Name, err)
		}
	}
	return nil
}

// Values returns the current record's columns as their natural Go values:
// int, string, io.ReadSeeker (binary), or nil for NULL.
func (r *Rows) Values() ([]any, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.pos < 0 || r.buf == nil {
		return nil, errors.New("msidb: Values called before Next")
	}
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()
	if r.db.closed {
		return nil, errClosed
	}
	out := make([]any, len(r.res))
	for i, res := range r.res {
		rec := r.record(res.slot)
		if res.idx >= len(rec) || rec[res.idx] == 0 {
			continue
		}
		raw := rec[res.idx]
		switch res.c.Type {
		case ColumnInteger:
			out[i] = decodeInt(raw, res.c.Size)
		case ColumnString:
			out[i], _ = r.db.pool.Lookup(raw)
		case ColumnBinary:
			if rs := r.db.openStream(r.tabs[res.slot], rec); rs != nil {
				out[i] = rs
			}
		}
	}
	return out, nil
}

// Columns returns the result's column metadata, in SELECT order.
func (r *Rows) Columns() []Column {
	cols := make([]Column, len(r.res))
	for i := range r.res {
		cols[i] = r.res[i].c
	}
	return cols
}

// Err returns any error that occurred while reading. It may be checked after
// [Rows.Next] returns false.
func (r *Rows) Err() error { return r.err }

// Close releases the rows. It is idempotent and safe to call on an errored Rows.
func (r *Rows) Close() error {
	if r.buf == nil {
		return nil
	}
	r.buf = nil
	for _, t := range r.tabs {
		if t.readers.Add(-1) == 0 && t.hasMoved.Load() {
			r.db.mu.Lock()
			if t.readers.Load() == 0 {
				t.moved = nil
				t.hasMoved.Store(false)
			}
			r.db.mu.Unlock()
		}
	}
	return nil
}

// record returns the current record from slot, as resized since the query ran.
// It is cut to the narrowest width seen, so a column dropped since then stays
// dropped even if a later column reuses its index.
func (r *Rows) record(slot int) []uint32 {
	rec := r.buf[r.pos*len(r.tabs)+slot]
	width := len(rec)
	if moved := r.tabs[slot].moved; moved != nil {
		for {
			resized, ok := moved[&rec[0]]
			if !ok {
				break
			}
			rec = resized
			width = min(width, len(rec))
		}
	}
	return rec[:width]
}

// fatal latches the first error and closes the rows.
func (r *Rows) fatal(err error) {
	if r.err != nil {
		return
	}
	r.err = err
	r.Close()
}

// Row is the result of [Database.QueryRow]: a query for at most one record
// whose errors are deferred to [Row.Scan].
type Row struct{ rows *Rows }

// Err returns the query error, if any, without scanning.
func (r *Row) Err() error { return r.rows.err }

// Scan reads the single record, mapping no records to [ErrNoRows] and
// surfacing any deferred error.
func (r *Row) Scan(dest ...any) error {
	if r.rows.err != nil {
		return r.rows.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		return ErrNoRows
	}
	return r.rows.Scan(dest...)
}

// Result summarises an executed statement.
type Result struct {
	rowsAffected int64
}

// RowsAffected returns the number of records inserted, updated, or deleted.
func (r Result) RowsAffected() int64 { return r.rowsAffected }
