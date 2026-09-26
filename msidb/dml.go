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

func (db *Database) execSelect(rows *Rows, sel *sql.Select, args []any) {
	tabs, err := db.bindFrom("query", sel.From)
	if err != nil {
		rows.fatal(err)
		return
	}
	b := &binder{db: db, op: "query", tabs: tabs, args: args}

	var where []predicate
	if sel.Where != nil {
		if where, err = b.where(sel.Where); err != nil {
			rows.fatal(err)
			return
		}
	}

	keys := make([]boundCol, len(sel.OrderBy))
	for i, ref := range sel.OrderBy {
		if keys[i], err = b.resolve(ref); err != nil {
			rows.fatal(err)
			return
		}
	}

	res, err := b.projection(sel.Columns)
	if err != nil {
		rows.fatal(err)
		return
	}
	if b.argpos != len(args) {
		rows.fatal(newError("query", "", errArgCount))
		return
	}

	buf := gather(tabs, where)
	if len(keys) > 0 {
		sortTuples(buf, len(tabs), keys)
	}
	if sel.Distinct {
		buf = distinct(buf, len(tabs), res)
	}

	rows.res = res
	rows.tabs = tabs
	rows.buf = buf
}

// gather collects the record tuples satisfying where into a flat buffer of
// stride len(tabs), in FROM-order nested-loop order. A nil where matches
// every tuple.
func gather(tabs []*table, where []predicate) [][]uint32 {
	if len(tabs) == 1 {
		t := tabs[0]
		if where == nil || where[0] == nil {
			return slices.Clone(t.records)
		}
		pred := where[0]
		var buf [][]uint32
		var tup [1][]uint32
		for _, rec := range t.records {
			tup[0] = rec
			if pred(tup[:]) {
				buf = append(buf, rec)
			}
		}
		return buf
	}

	var buf [][]uint32
	tup := make([][]uint32, len(tabs))
	var walk func(level int)
	walk = func(level int) {
		var pred predicate
		if where != nil {
			pred = where[level]
		}
		for _, rec := range tabs[level].records {
			tup[level] = rec
			if pred != nil && !pred(tup) {
				continue
			}
			if level == len(tabs)-1 {
				buf = append(buf, tup...)
			} else {
				walk(level + 1)
			}
		}
	}
	walk(0)
	return buf
}

func sortTuples(buf [][]uint32, stride int, keys []boundCol) {
	if stride == 1 {
		slices.SortStableFunc(buf, func(a, b []uint32) int {
			for _, k := range keys {
				if d := cmp.Compare(a[k.idx], b[k.idx]); d != 0 {
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
		a := s.buf[i*s.stride+k.slot][k.idx]
		b := s.buf[j*s.stride+k.slot][k.idx]
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
		tup := buf[i : i+stride]
		for j, r := range res {
			binary.LittleEndian.PutUint32(key[j*4:], tup[r.slot][r.idx])
		}
		if _, dup := seen[string(key)]; dup {
			continue
		}
		seen[string(key)] = struct{}{}
		copy(buf[w:], tup)
		w += stride
	}
	clear(buf[w:])
	return buf[:w]
}

func (db *Database) execInsert(s *sql.Insert, args []any) (_ Result, err error) { //nolint:funlen,gocognit
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
	pos := 0
	for i, name := range s.Columns {
		idx, ok := t.byName[name]
		if !ok {
			return Result{}, newError("insert", s.Table+"."+name, ErrNotExist)
		}
		v, err := litValue(s.Values[i], args, &pos)
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
	if pos != len(args) {
		return Result{}, newError("insert", s.Table, errArgCount)
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

// insertStream handles INSERT INTO _Streams. A record without a payload is rejected.
func (db *Database) insertStream(s *sql.Insert, args []any) (Result, error) {
	if s.Temporary {
		return Result{}, newError("insert", systemTableStreams+"."+streamDataCol, errTemporaryBinary)
	}
	t := db.tables[systemTableStreams]
	var name string
	var data io.Reader
	pos := 0
	for i, col := range s.Columns {
		idx, ok := t.byName[col]
		if !ok {
			return Result{}, newError("insert", systemTableStreams+"."+col, ErrNotExist)
		}
		v, err := litValue(s.Values[i], args, &pos)
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
	if pos != len(args) {
		return Result{}, newError("insert", systemTableStreams, errArgCount)
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
		if _, exists := db.sources[id]; exists {
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

// assignment is one resolved SET clause.
type assignment struct {
	boundCol

	val  any          // as convertValue returns it
	src  streamSource // staged payload; nil clears the cell
	used bool         // the staged payload is owned by a target
}

// target is one record an UPDATE matched, with the FROM slot its table occupies.
type target struct {
	table      *table
	rec        []uint32
	slot       int
	streamName string       // derived; "" unless an OBJECT column is assigned
	streamData streamSource // staged payload to store; nil stores none
}

// execUpdate overwrites cells of the records matched by the WHERE clause.
// Primary keys are immutable in every table. On error nothing is changed.
func (db *Database) execUpdate(u *sql.Update, args []any) (_ Result, err error) { //nolint:funlen,gocognit
	for _, name := range u.Tables {
		if isReadOnlyTable(name) {
			return Result{}, newError("update", name, errReadOnly)
		}
	}
	tabs, err := db.bindFrom("update", u.Tables)
	if err != nil {
		return Result{}, err
	}
	b := &binder{db: db, op: "update", tabs: tabs, args: args}

	assigns := make([]assignment, 0, len(u.Set))
	var targets []target
	defer func() {
		for _, a := range assigns {
			if a.src != nil && !a.used {
				a.src.release()
			}
		}
		if err == nil {
			return
		}
		for _, tgt := range targets {
			if tgt.streamData != nil {
				tgt.streamData.release()
			}
		}
	}()
	for _, set := range u.Set {
		bc, err := b.resolve(set.Column)
		if err != nil {
			return Result{}, err
		}
		t := tabs[bc.slot]
		if bc.c.PrimaryKey {
			return Result{}, newColError("update", t, bc.c, errors.New("cannot update a primary key"))
		}
		val, err := b.literal(set.Value)
		if err != nil {
			return Result{}, err
		}
		if slices.ContainsFunc(assigns, func(a assignment) bool { return a.slot == bc.slot && a.idx == bc.idx }) {
			continue // A repeated column keeps its first value.
		}
		v, err := convertValue(db.pool, bc.c, val)
		if err != nil {
			return Result{}, newColError("update", t, bc.c, err)
		}
		a := assignment{boundCol: bc, val: v}
		if rd, ok := v.(io.Reader); ok {
			if a.src, err = db.stage(rd); err != nil {
				return Result{}, newError("update", t.name, err)
			}
		} else if t.name == systemTableStreams && bc.c.Type == ColumnBinary {
			return Result{}, newError("update", systemTableStreams, fmt.Errorf("%s is required", streamDataCol))
		}
		assigns = append(assigns, a)
	}

	var where []predicate
	if u.Where != nil {
		if where, err = b.where(u.Where); err != nil {
			return Result{}, err
		}
	}
	if b.argpos != len(args) {
		return Result{}, newError("update", "", errArgCount)
	}

	var slots []int
	for _, a := range assigns {
		if !slices.Contains(slots, a.slot) {
			slots = append(slots, a.slot)
		}
	}

	buf := gather(tabs, where)
	stride := len(tabs)
	var seen map[*uint32]struct{}
	if stride > 1 {
		seen = make(map[*uint32]struct{})
	}
	for i := 0; i < len(buf); i += stride {
		tup := buf[i : i+stride]
		for _, slot := range slots {
			rec := tup[slot]
			if stride > 1 {
				key := &rec[0]
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
			}
			tgt := target{table: tabs[slot], rec: rec, slot: slot}
			if err := db.planTarget(&tgt, assigns); err != nil {
				return Result{}, err
			}
			targets = append(targets, tgt)
		}
	}

	for _, tgt := range targets {
		db.applyAssignments(tgt, assigns)
	}
	if len(targets) > 0 {
		db.dirty = true
	}
	return Result{rowsAffected: int64(len(targets))}, nil
}

// planTarget derives tgt's stream name and the payload it stores from the
// assignments, or reports why they cannot be applied to it.
func (db *Database) planTarget(tgt *target, assigns []assignment) error {
	var payload *assignment
	binary := false
	for i := range assigns {
		a := &assigns[i]
		if a.slot != tgt.slot || a.c.Type != ColumnBinary {
			continue
		}
		binary = true
		// The record's OBJECT cells share one stream; the highest column's payload is stored.
		if a.src != nil && (payload == nil || a.idx > payload.idx) {
			payload = a
		}
	}
	if !binary {
		return nil
	}

	name := db.streamKey(tgt.table, tgt.rec)
	if payload == nil {
		// Clearing only drops an existing stream, which an invalid name cannot have.
		if checkStreamName(name) == nil {
			tgt.streamName = name
		}
		return nil
	}

	if !tgt.table.cellPersistent(tgt.rec, payload.c) {
		return newColError("update", tgt.table, payload.c, errTemporaryBinary)
	}
	if name == "" {
		return newError("update", tgt.table.name, errors.New("cannot derive a stream name from the key"))
	}
	if err := checkStreamName(name); err != nil {
		return newError("update", tgt.table.name, err)
	}
	tgt.streamName = name
	if !payload.used {
		tgt.streamData, payload.used = payload.src, true
		return nil
	}
	// A further matched record duplicates the staged payload.
	src, err := db.stage(payload.src.open())
	if err != nil {
		return newError("update", tgt.table.name, err)
	}
	tgt.streamData = src
	return nil
}

// applyAssignments overwrites tgt's cells with the assignments, then stores
// or drops the record's data stream.
func (db *Database) applyAssignments(tgt target, assigns []assignment) {
	t, rec := tgt.table, tgt.rec
	for _, a := range assigns {
		if a.slot != tgt.slot {
			continue
		}
		persistent := t.cellPersistent(rec, a.c)
		old := rec[a.idx]
		rec[a.idx] = encodeValue(db.pool, a.c, persistent, a.val)
		if a.c.Type == ColumnString && old != 0 {
			db.pool.Release(old, persistent)
		}
	}
	if tgt.streamName == "" {
		return
	}
	if tgt.streamData != nil {
		db.addStream(tgt.streamName, tgt.streamData)
	}
	if !hasBinary(t.cols, rec) {
		db.dropStream(tgt.streamName)
	}
}

// execDelete removes the records matched by the WHERE clause. DELETE is
// single-table.
func (db *Database) execDelete(s *sql.Delete, args []any) (Result, error) {
	if len(s.From) != 1 {
		return Result{}, newError("delete", "", errors.New("DELETE from a join is not supported"))
	}
	if isReadOnlyTable(s.From[0]) {
		return Result{}, newError("delete", s.From[0], errReadOnly)
	}
	t, ok := db.tables[s.From[0]]
	if !ok {
		return Result{}, newError("delete", s.From[0], ErrNotExist)
	}

	var pred predicate
	argpos := 0
	if s.Where != nil {
		b := &binder{db: db, op: "delete", tabs: []*table{t}, args: args}
		where, err := b.where(s.Where)
		if err != nil {
			return Result{}, err
		}
		pred, argpos = where[0], b.argpos
	}
	if argpos != len(args) {
		return Result{}, newError("delete", t.name, errArgCount)
	}

	if t.name == systemTableStreams {
		return db.deleteStreams(t, pred)
	}

	n := int64(0)
	w := 0
	var tup [1][]uint32
	for _, rec := range t.records {
		tup[0] = rec
		if pred != nil && !pred(tup[:]) {
			t.records[w] = rec
			w++
			continue
		}
		db.releaseRecord(t, rec)
		n++
	}
	clear(t.records[w:])
	t.records = t.records[:w]
	if n > 0 {
		db.dirty = true
	}
	return Result{rowsAffected: n}, nil
}

func (db *Database) deleteStreams(t *table, pred predicate) (Result, error) {
	var names []string
	var tup [1][]uint32
	for _, rec := range t.records {
		tup[0] = rec
		if pred == nil || pred(tup[:]) {
			if name, ok := db.pool.Lookup(rec[0]); ok {
				names = append(names, name)
			}
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
