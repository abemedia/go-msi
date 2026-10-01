package msidb

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/abemedia/go-msi/internal/sql"
)

var (
	errReadOnly        = errors.New("system table is read-only")
	errTemporaryBinary = errors.New("temporary field cannot hold binary data")
)

func (db *Database) execSelect(rows *Rows, sel *sql.Select, args []any) error {
	q, err := db.query("query", sel.Tables, args)
	if err != nil {
		return err
	}
	if sel.Where != nil {
		if err := q.where(sel.Where); err != nil {
			return err
		}
	}
	order, err := q.columns(sel.OrderBy)
	if err != nil {
		return err
	}
	res, err := q.columns(sel.Columns)
	if err != nil {
		return err
	}
	if sel.Columns == nil {
		for i, t := range q.tables {
			for cell, c := range t.cols {
				res = append(res, boundCol{table: i, cell: cell, Column: c})
			}
		}
	}

	buf := q.gather()
	if len(order) > 0 {
		sortTuples(buf, len(q.tables), order)
	}
	if sel.Distinct {
		buf = distinct(buf, len(q.tables), res)
	}
	rows.res, rows.tabs, rows.buf = res, q.tables, buf
	return nil
}

func sortTuples(buf [][]uint32, stride int, keys []boundCol) {
	if stride == 1 {
		slices.SortStableFunc(buf, func(a, b []uint32) int {
			for _, k := range keys {
				if d := cmp.Compare(a[k.cell], b[k.cell]); d != 0 {
					return d
				}
			}
			return 0
		})
		return
	}
	sort.Stable(&tupleSorter{buf: buf, stride: stride, keys: keys})
}

type tupleSorter struct {
	buf    [][]uint32
	stride int
	keys   []boundCol
}

func (s *tupleSorter) Len() int { return len(s.buf) / s.stride }

func (s *tupleSorter) Less(i, j int) bool {
	for _, k := range s.keys {
		a := s.buf[i*s.stride+k.table][k.cell]
		b := s.buf[j*s.stride+k.table][k.cell]
		if a != b {
			return a < b
		}
	}
	return false
}

func (s *tupleSorter) Swap(i, j int) {
	a := s.buf[i*s.stride : (i+1)*s.stride]
	b := s.buf[j*s.stride : (j+1)*s.stride]
	for k := range a {
		a[k], b[k] = b[k], a[k]
	}
}

// distinct removes duplicate result records in place, preserving first
// occurrence. Identity is the raw projected cells.
func distinct(buf [][]uint32, stride int, res []boundCol) [][]uint32 {
	if len(buf) == 0 {
		return buf
	}
	seen := make(map[string]struct{}, len(buf)/stride)
	key := make([]byte, len(res)*4)
	w := 0
	for i := 0; i < len(buf); i += stride {
		tuple := buf[i : i+stride]
		for j, r := range res {
			binary.LittleEndian.PutUint32(key[j*4:], tuple[r.table][r.cell])
		}
		if _, ok := seen[string(key)]; ok {
			continue
		}
		seen[string(key)] = struct{}{}
		copy(buf[w:], tuple)
		w += stride
	}
	clear(buf[w:])
	return buf[:w]
}

func (db *Database) execInsert(s *sql.Insert, args []any) (_ Result, err error) { //nolint:gocognit
	if isReadOnlyTable(s.Table) {
		return Result{}, newError("insert", s.Table, errReadOnly)
	}
	t, ok := db.tables[s.Table]
	if !ok {
		return Result{}, newError("insert", s.Table, ErrNotExist)
	}
	if t.name == systemTableStreams {
		return db.insertStream(s, args)
	}
	if len(t.keys) == 0 {
		return Result{}, newError("insert", s.Table, errors.New("table has no primary key"))
	}

	rec := make([]uint32, len(t.cols))
	if s.Temporary {
		if t.temp == nil {
			t.temp = map[*uint32]struct{}{}
		}
		t.temp[&rec[0]] = struct{}{}
	}
	defer func() {
		if err != nil {
			db.releaseCells(t, rec)
			delete(t.temp, &rec[0])
		}
	}()
	var payload io.Reader
	payloadCol := -1
	for i, name := range s.Columns {
		idx, ok := t.byName[name]
		if !ok {
			return Result{}, newError("insert", s.Table+"."+name, ErrNotExist)
		}
		v, err := literal(s.Values[i], args)
		if err != nil {
			return Result{}, newError("insert", s.Table, err)
		}
		if slices.Contains(s.Columns[:i], name) {
			continue // A repeated column keeps its first value.
		}
		c := t.cols[idx]
		persistent := t.cellPersistent(rec, c)
		v, err = convertValue(db.pool, c, v)
		if err != nil {
			return Result{}, newColError("insert", t, c, err)
		}
		if rd, ok := v.(io.Reader); ok {
			if !persistent {
				return Result{}, newColError("insert", t, c, errTemporaryBinary)
			}
			// The record's OBJECT cells share one stream; the highest column's payload is stored.
			if idx >= payloadCol {
				payload, payloadCol = rd, idx
			}
		}
		rec[idx] = encodeValue(db.pool, c, persistent, v)
	}
	for i, c := range t.cols {
		if rec[i] == 0 && !c.Nullable {
			return Result{}, newColError("insert", t, c, errors.New("NULL not allowed"))
		}
	}

	stream := ""
	if payload != nil {
		if stream = db.streamKey(t, rec); stream == "" {
			return Result{}, newError("insert", s.Table, errors.New("cannot derive a stream name from the key"))
		}
		if err := checkStreamName(stream); err != nil {
			return Result{}, newError("insert", s.Table, err)
		}
	}

	idx, found := t.find(rec)
	if found {
		return Result{}, newError("insert", s.Table, ErrExist)
	}
	var src streamSource
	if payload != nil {
		if src, err = db.stage(payload); err != nil {
			return Result{}, newError("insert", s.Table, err)
		}
	}
	t.records = slices.Insert(t.records, idx, rec)
	if src != nil {
		db.addStream(stream, src)
	}
	db.dirty = true
	return Result{rowsAffected: 1}, nil
}

func (db *Database) insertStream(s *sql.Insert, args []any) (Result, error) {
	if s.Temporary {
		return Result{}, newError("insert", systemTableStreams+"."+streamDataCol, errTemporaryBinary)
	}
	t := db.tables[systemTableStreams]
	var name string
	var data io.Reader
	for i, col := range s.Columns {
		idx, ok := t.byName[col]
		if !ok {
			return Result{}, newError("insert", systemTableStreams+"."+col, ErrNotExist)
		}
		v, err := literal(s.Values[i], args)
		if err != nil {
			return Result{}, newError("insert", systemTableStreams, err)
		}
		if slices.Contains(s.Columns[:i], col) {
			continue // A repeated column keeps its first value.
		}
		if v, err = convertValue(db.pool, t.cols[idx], v); err != nil {
			return Result{}, newError("insert", systemTableStreams+"."+col, err)
		}
		switch col {
		case streamNameCol:
			name, _ = v.(string)
		case streamDataCol:
			data, _ = v.(io.Reader)
		}
	}
	if name == "" {
		return Result{}, newError("insert", systemTableStreams, fmt.Errorf("%s is required", streamNameCol))
	}
	if data == nil {
		return Result{}, newError("insert", systemTableStreams, fmt.Errorf("%s is required", streamDataCol))
	}
	if err := checkStreamName(name); err != nil {
		return Result{}, newError("insert", systemTableStreams, err)
	}
	if id, ok := db.pool.LookupID(name); ok {
		if _, ok := db.sources[id]; ok {
			return Result{}, newError("insert", systemTableStreams, ErrExist)
		}
	}
	if strings.HasPrefix(name, "\x05") {
		for id := range db.sources {
			if existing, _ := db.pool.Lookup(id); strings.HasPrefix(existing, "\x05") && strings.EqualFold(existing, name) {
				return Result{}, newError("insert", systemTableStreams, ErrExist)
			}
		}
	}
	src, err := db.stage(data)
	if err != nil {
		return Result{}, newError("insert", systemTableStreams, err)
	}
	db.addStream(name, src)
	db.dirty = true
	return Result{rowsAffected: 1}, nil
}

// assignment is one resolved SET clause of an UPDATE.
type assignment struct {
	boundCol

	val any // as convertValue returns it
}

// change is what an UPDATE does to one of its tables.
type change struct {
	sets    []assignment      // in SET order
	binary  bool              // an OBJECT column is assigned, so each record's stream changes
	payload *assignment       // the highest OBJECT column assigned a reader
	src     *blobStreamSource // payload staged
	recs    [][]uint32        // the records to update
}

func (db *Database) execUpdate(u *sql.Update, args []any) (Result, error) { //nolint:gocognit
	for _, name := range u.Tables {
		if isReadOnlyTable(name) {
			return Result{}, newError("update", name, errReadOnly)
		}
	}
	q, err := db.query("update", u.Tables, args)
	if err != nil {
		return Result{}, err
	}

	changes := make([]change, len(q.tables))
	for _, set := range u.Set {
		col, err := q.column(set.Column)
		if err != nil {
			return Result{}, err
		}
		t, ch := q.tables[col.table], &changes[col.table]
		if col.PrimaryKey {
			return Result{}, newColError("update", t, col.Column, errors.New("cannot update a primary key"))
		}
		lit, err := literal(set.Value, args)
		if err != nil {
			return Result{}, newError("update", "", err)
		}
		if slices.ContainsFunc(ch.sets, func(a assignment) bool { return a.cell == col.cell }) {
			continue // A repeated column keeps its first value.
		}
		v, err := convertValue(db.pool, col.Column, lit)
		if err != nil {
			return Result{}, newColError("update", t, col.Column, err)
		}
		if _, ok := v.(io.Reader); !ok && t.name == systemTableStreams && col.Type == ColumnBinary {
			return Result{}, newError("update", systemTableStreams, fmt.Errorf("%s is required", streamDataCol))
		}
		ch.sets = append(ch.sets, assignment{col, v})
	}
	if u.Where != nil {
		if err := q.where(u.Where); err != nil {
			return Result{}, err
		}
	}

	for i := range changes {
		ch := &changes[i]
		for j := range ch.sets {
			a := &ch.sets[j]
			if a.Type != ColumnBinary {
				continue
			}
			ch.binary = true
			// The record's OBJECT cells share one stream; the highest column's payload is stored.
			if _, ok := a.val.(io.Reader); ok && (ch.payload == nil || a.cell > ch.payload.cell) {
				ch.payload = a
			}
		}
	}

	var seen map[*uint32]bool
	if len(q.tables) > 1 {
		seen = map[*uint32]bool{}
	}
	buf := q.gather()
	for off := 0; off < len(buf); off += len(q.tables) {
		tuple := buf[off : off+len(q.tables)]
		for i := range changes {
			ch, rec := &changes[i], tuple[i]
			if len(ch.sets) == 0 || seen[&rec[0]] {
				continue
			}
			if seen != nil {
				seen[&rec[0]] = true
			}
			if p := ch.payload; p != nil {
				t := q.tables[i]
				if !t.cellPersistent(rec, p.Column) {
					return Result{}, newColError("update", t, p.Column, errTemporaryBinary)
				}
				name := db.streamKey(t, rec)
				if name == "" {
					return Result{}, newError("update", t.name, errors.New("cannot derive a stream name from the key"))
				}
				if err := checkStreamName(name); err != nil {
					return Result{}, newError("update", t.name, err)
				}
			}
			ch.recs = append(ch.recs, rec)
		}
	}

	for i := range changes {
		ch := &changes[i]
		if ch.payload == nil {
			continue
		}
		if ch.src, err = db.stage(ch.payload.val.(io.Reader)); err != nil {
			for _, staged := range changes[:i] {
				if staged.src != nil {
					staged.src.release()
				}
			}
			return Result{}, newError("update", q.tables[i].name, err)
		}
	}

	var n int64
	for i, ch := range changes {
		t := q.tables[i]
		for _, rec := range ch.recs {
			for _, a := range ch.sets {
				persistent := t.cellPersistent(rec, a.Column)
				old := rec[a.cell]
				rec[a.cell] = encodeValue(db.pool, a.Column, persistent, a.val)
				if a.Type == ColumnString && old != 0 {
					db.pool.Release(old, persistent)
				}
			}
			if ch.src != nil {
				ch.src.refs++
				db.addStream(db.streamKey(t, rec), ch.src)
			}
			if ch.binary && !hasBinary(t.cols, rec) {
				db.dropStream(db.streamKey(t, rec))
			}
		}
		if ch.src != nil {
			ch.src.release()
		}
		n += int64(len(ch.recs))
	}
	if n > 0 {
		db.dirty = true
	}
	return Result{rowsAffected: n}, nil
}

func (db *Database) execDelete(s *sql.Delete, args []any) (Result, error) {
	if isReadOnlyTable(s.Table) {
		return Result{}, newError("delete", s.Table, errReadOnly)
	}
	q, err := db.query("delete", []string{s.Table}, args)
	if err != nil {
		return Result{}, err
	}
	if s.Where != nil {
		if err := q.where(s.Where); err != nil {
			return Result{}, err
		}
	}
	t := q.tables[0]
	if t.name == systemTableStreams {
		var names []string
		for _, rec := range q.gather() {
			if name, ok := db.pool.Lookup(rec[0]); ok {
				names = append(names, name)
			}
		}
		for _, name := range names {
			db.dropStream(name)
		}
		if len(names) > 0 {
			db.dirty = true
		}
		return Result{rowsAffected: int64(len(names))}, nil
	}

	var n int64
	tuple := make([][]uint32, 1)
	lo, hi := q.window(0, tuple)
	match := q.scans[0].match
	w := lo
	for _, rec := range t.records[lo:hi] {
		tuple[0] = rec
		if match != nil && !match(tuple) {
			t.records[w] = rec
			w++
			continue
		}
		db.releaseRecord(t, rec)
		n++
	}
	t.records = slices.Delete(t.records, w, hi)
	if n > 0 {
		db.dirty = true
	}
	return Result{rowsAffected: n}, nil
}
