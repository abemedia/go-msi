package msidb

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/abemedia/go-msi/internal/sql"
)

// boundCol is a column reference resolved against a statement's tables.
type boundCol struct {
	Column

	table, cell int
}

// ordinal returns the column's cell in tuple as an int that orders and
// compares like the column's values: integers decoded, other cells as stored.
func (col boundCol) ordinal(tuple [][]uint32) int {
	return ordinal(col.Column, tuple[col.table][col.cell])
}

func ordinal(c Column, raw uint32) int {
	if c.Type == ColumnInteger {
		if raw == 0 {
			return math.MinInt32 // NULL is one value for both integer widths.
		}
		return decodeInt(raw, c.Size)
	}
	return int(raw)
}

// scan is how the records of one of a statement's tables are read: those
// whose first key column has the ordinal seek yields, or all when seek is
// nil, each accepted by match unless match is nil. A tuple holds one record
// per table of the statement.
type scan struct {
	seek  func(tuple [][]uint32) int
	match func(tuple [][]uint32) bool
}

// query is the tables a statement reads, its arguments, and its WHERE clause
// once compiled.
type query struct {
	db     *Database
	op     string
	tables []*table
	args   []any
	scans  []scan
}

func (db *Database) query(op string, names []string, args []any) (*query, error) {
	q := &query{
		db:     db,
		op:     op,
		tables: make([]*table, len(names)),
		args:   args,
		scans:  make([]scan, len(names)),
	}
	for i, name := range names {
		t, ok := db.tables[name]
		if !ok {
			return nil, newError(op, name, ErrNotExist)
		}
		if slices.Contains(names[:i], name) {
			return nil, newError(op, name, errors.New("duplicate table"))
		}
		q.tables[i] = t
	}
	return q, nil
}

func (q *query) column(ref sql.ColumnRef) (boundCol, error) {
	var col boundCol
	found := false
	for i, t := range q.tables {
		cell, ok := t.byName[ref.Name]
		if !ok || (ref.Table != "" && ref.Table != t.name) {
			continue
		}
		if found {
			return boundCol{}, newError(q.op, ref.Name, errors.New("ambiguous column"))
		}
		found = true
		col = boundCol{table: i, cell: cell, Column: t.cols[cell]}
	}
	if found {
		return col, nil
	}

	if ref.Table != "" && !slices.ContainsFunc(q.tables, func(t *table) bool { return t.name == ref.Table }) {
		if _, ok := q.db.tables[ref.Table]; ok {
			return boundCol{}, newError(q.op, ref.Table, errors.New("not in the table list"))
		}
		return boundCol{}, newError(q.op, ref.Table, ErrNotExist)
	}

	name := ref.Name
	if ref.Table != "" {
		name = ref.Table + "." + name
	} else if len(q.tables) == 1 {
		name = q.tables[0].name + "." + name
	}
	return boundCol{}, newError(q.op, name, ErrNotExist)
}

func (q *query) columns(refs []sql.ColumnRef) ([]boundCol, error) {
	cols := make([]boundCol, len(refs))
	for i, ref := range refs {
		var err error
		if cols[i], err = q.column(ref); err != nil {
			return nil, err
		}
	}
	return cols, nil
}

func (q *query) firstKey(col boundCol) bool {
	return col.PrimaryKey && q.tables[col.table].keys[0] == col.cell
}

// literal returns the Go value of a literal, or of the argument a ? takes.
func literal(v sql.Value, args []any) (any, error) {
	switch lit := v.(type) {
	case sql.IntLit:
		return int(lit), nil
	case sql.StringLit:
		return string(lit), nil
	case sql.Null:
		return nil, nil //nolint:nilnil // NULL is a valid value with no error
	case sql.Marker:
		return args[lit], nil
	}
	return nil, fmt.Errorf("unexpected value %T", v)
}

// cond is a compiled WHERE expression.
type cond struct {
	level int // the deepest table it reads
	match func(tuple [][]uint32) bool
	seek  func(tuple [][]uint32) int // set when it fixes the first key column of the table at level to a value known there
}

// where compiles e. Each top-level AND-term is tested at the deepest table it
// reads; a term equating a table's first key column with a constant, NULL, or
// an earlier table's column narrows that table's scan to the matching records.
func (q *query) where(e sql.Expr) error {
	if and, ok := e.(*sql.And); ok {
		if err := q.where(and.Left); err != nil {
			return err
		}
		return q.where(and.Right)
	}
	c, err := q.compile(e)
	if err != nil {
		return err
	}
	s := &q.scans[c.level]
	if prev := s.match; prev != nil {
		match := c.match
		c.match = func(tuple [][]uint32) bool { return prev(tuple) && match(tuple) }
	}
	s.match = c.match
	if c.seek != nil && s.seek == nil {
		s.seek = c.seek
	}
	return nil
}

func (q *query) compile(e sql.Expr) (cond, error) {
	switch x := e.(type) {
	case *sql.And:
		return q.compileAnd(x)
	case *sql.Or:
		return q.compileOr(x)
	case *sql.IsNull:
		return q.compileIsNull(x)
	case *sql.ColumnEqual:
		return q.compileColumnEqual(x)
	case *sql.Comparison:
		return q.compileComparison(x)
	}
	return cond{}, newError(q.op, "", errors.New("unsupported expression"))
}

func (q *query) compileAnd(x *sql.And) (cond, error) {
	l, err := q.compile(x.Left)
	if err != nil {
		return cond{}, err
	}
	r, err := q.compile(x.Right)
	if err != nil {
		return cond{}, err
	}
	return cond{
		level: max(l.level, r.level),
		match: func(tuple [][]uint32) bool { return l.match(tuple) && r.match(tuple) },
	}, nil
}

func (q *query) compileOr(x *sql.Or) (cond, error) {
	l, err := q.compile(x.Left)
	if err != nil {
		return cond{}, err
	}
	r, err := q.compile(x.Right)
	if err != nil {
		return cond{}, err
	}
	return cond{
		level: max(l.level, r.level),
		match: func(tuple [][]uint32) bool { return l.match(tuple) || r.match(tuple) },
	}, nil
}

func (q *query) compileIsNull(x *sql.IsNull) (cond, error) {
	col, err := q.column(x.Column)
	if err != nil {
		return cond{}, err
	}
	c := cond{
		level: col.table,
		match: func(tuple [][]uint32) bool { return (tuple[col.table][col.cell] == 0) != x.Not },
	}
	if !x.Not && q.firstKey(col) {
		c.seek = func([][]uint32) int { return ordinal(col.Column, 0) }
	}
	return c, nil
}

func (q *query) compileColumnEqual(x *sql.ColumnEqual) (cond, error) {
	l, err := q.column(x.Left)
	if err != nil {
		return cond{}, err
	}
	r, err := q.column(x.Right)
	if err != nil {
		return cond{}, err
	}
	if l.Type != r.Type || l.Type == ColumnBinary {
		err := fmt.Errorf("cannot compare %s and %s columns", l.Type, r.Type)
		return cond{}, newColError(q.op, q.tables[l.table], l.Column, err)
	}
	deep, shallow := l, r
	if r.table > l.table {
		deep, shallow = r, l
	}
	c := cond{
		level: deep.table,
		match: func(tuple [][]uint32) bool { return l.ordinal(tuple) == r.ordinal(tuple) },
	}
	if deep.table > shallow.table && q.firstKey(deep) {
		c.seek = shallow.ordinal
	}
	return c, nil
}

func (q *query) compileComparison(x *sql.Comparison) (cond, error) {
	l, err := q.column(x.Column)
	if err != nil {
		return cond{}, err
	}
	op := x.Op
	lit, err := literal(x.Value, q.args)
	if err != nil {
		return cond{}, newError(q.op, "", err)
	}
	v, err := q.constant(l, op, lit)
	if err != nil {
		return cond{}, newColError(q.op, q.tables[l.table], l.Column, err)
	}
	c := cond{
		level: l.table,
		match: func(tuple [][]uint32) bool { return compareInt(l.ordinal(tuple), v, op) },
	}
	if l.Type != ColumnInteger {
		raw, equal := uint32(v), op == sql.OpEqual
		c.match = func(tuple [][]uint32) bool { return (v >= 0 && tuple[l.table][l.cell] == raw) == equal }
	} else if op != sql.OpEqual && op != sql.OpNotEqual {
		c.match = func(tuple [][]uint32) bool { return tuple[l.table][l.cell] != 0 && compareInt(l.ordinal(tuple), v, op) }
	}
	if op == sql.OpEqual && q.firstKey(l) {
		c.seek = func([][]uint32) int { return v }
	}
	return c, nil
}

// constant converts lit to the ordinal column col compares with under op.
func (q *query) constant(col boundCol, op sql.CompareOp, lit any) (int, error) {
	lit, err := normalize(lit)
	if err != nil {
		return 0, err
	}
	equality := op == sql.OpEqual || op == sql.OpNotEqual
	if lit == nil || lit == "" {
		if !equality {
			return 0, errors.New("cannot compare a column to NULL with that operator")
		}
		return ordinal(col.Column, 0), nil
	}
	switch col.Type {
	case ColumnInteger:
		return intValue(lit, math.MaxInt32)
	case ColumnString:
		if !equality {
			return 0, errors.New("string columns support only = and <>")
		}
		s, err := stringValue(lit)
		if err != nil {
			return 0, err
		}
		if id, ok := q.db.pool.LookupID(s); ok {
			return int(id), nil
		}
		return -1, nil // an ordinal no cell has
	}
	return 0, fmt.Errorf("cannot compare a %s column", col.Type)
}

// gather returns the records of every combination of one record per table
// that the WHERE clause accepts, concatenated in table order.
func (q *query) gather() [][]uint32 {
	return q.walk(0, make([][]uint32, len(q.tables)), nil)
}

// walk appends to buf the records of every accepted tuple from level down,
// given the records already chosen for the levels above it in tuple.
func (q *query) walk(level int, tuple, buf [][]uint32) [][]uint32 {
	lo, hi := q.window(level, tuple)
	match := q.scans[level].match
	if match == nil && len(tuple) == 1 {
		return append(buf, q.tables[0].records[lo:hi]...)
	}
	for _, rec := range q.tables[level].records[lo:hi] {
		tuple[level] = rec
		if match != nil && !match(tuple) {
			continue
		}
		if level+1 < len(tuple) {
			buf = q.walk(level+1, tuple, buf)
		} else {
			buf = append(buf, tuple...)
		}
	}
	return buf
}

// window returns the half-open range of the records of the table at level
// that the query scans: those whose first key column has the sought ordinal,
// or all of them.
func (q *query) window(level int, tuple [][]uint32) (lo, hi int) {
	t := q.tables[level]
	seek := q.scans[level].seek
	if seek == nil {
		return 0, len(t.records)
	}
	cell := t.keys[0]
	col := t.cols[cell]
	want := seek(tuple)
	lo = sort.Search(len(t.records), func(i int) bool { return ordinal(col, t.records[i][cell]) >= want })
	hi = lo + sort.Search(len(t.records)-lo, func(i int) bool { return ordinal(col, t.records[lo+i][cell]) > want })
	return lo, hi
}

func compareInt(a, b int, op sql.CompareOp) bool {
	switch op {
	case sql.OpEqual:
		return a == b
	case sql.OpNotEqual:
		return a != b
	case sql.OpLess:
		return a < b
	case sql.OpLessEqual:
		return a <= b
	case sql.OpGreater:
		return a > b
	case sql.OpGreaterEqual:
		return a >= b
	}
	return false
}
