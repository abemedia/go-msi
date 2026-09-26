package msidb

import (
	"errors"
	"fmt"
	"math"

	"github.com/abemedia/go-msi/internal/sql"
)

// boundCol is a column reference resolved against the FROM tables.
type boundCol struct {
	slot int // position of the column's table in FROM
	idx  int // position of the column in that table
	c    Column
}

// binder resolves a parsed statement against the FROM tables.
// Errors it returns are of type [*Error].
type binder struct {
	db     *Database
	op     string
	tabs   []*table
	args   []any
	argpos int // next ? placeholder to consume
	depth  int // deepest FROM slot resolved since splitWhere last reset it
}

func (db *Database) bindFrom(op string, from []string) ([]*table, error) {
	tabs := make([]*table, len(from))
	for i, name := range from {
		t, ok := db.tables[name]
		if !ok {
			return nil, newError(op, name, ErrNotExist)
		}
		tabs[i] = t
	}
	return tabs, nil
}

func (b *binder) resolve(ref sql.ColumnRef) (boundCol, error) {
	known := ref.Table == ""
	for slot, t := range b.tabs {
		if ref.Table != "" && ref.Table != t.name {
			continue
		}
		known = true
		if idx, ok := t.byName[ref.Name]; ok {
			b.depth = max(b.depth, slot)
			return boundCol{slot: slot, idx: idx, c: t.cols[idx]}, nil
		}
	}
	if !known {
		if _, ok := b.db.tables[ref.Table]; ok {
			return boundCol{}, newError(b.op, ref.Table, errors.New("not in FROM"))
		}
		return boundCol{}, newError(b.op, ref.Table, ErrNotExist)
	}
	name := ref.Name
	switch {
	case ref.Table != "":
		name = ref.Table + "." + ref.Name
	case len(b.tabs) == 1:
		name = b.tabs[0].name + "." + ref.Name
	}
	return boundCol{}, newError(b.op, name, ErrNotExist)
}

func (b *binder) projection(cols []sql.ColumnRef) ([]boundCol, error) {
	if cols == nil {
		var res []boundCol
		for slot, t := range b.tabs {
			for idx, c := range t.cols {
				res = append(res, boundCol{slot: slot, idx: idx, c: c})
			}
		}
		return res, nil
	}
	res := make([]boundCol, len(cols))
	for i, ref := range cols {
		var err error
		if res[i], err = b.resolve(ref); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// predicate is a compiled condition over a tuple of per-table records.
type predicate func(tup [][]uint32) bool

// where compiles e into one predicate per FROM slot: where[i] is the
// conjunction of the top-level AND-terms that read slot i and none deeper,
// or nil when there are none.
func (b *binder) where(e sql.Expr) ([]predicate, error) {
	where := make([]predicate, len(b.tabs))
	if err := b.splitWhere(e, where); err != nil {
		return nil, err
	}
	return where, nil
}

func (b *binder) splitWhere(e sql.Expr, where []predicate) error {
	if and, ok := e.(*sql.And); ok {
		if err := b.splitWhere(and.Left, where); err != nil {
			return err
		}
		return b.splitWhere(and.Right, where)
	}
	b.depth = 0
	p, err := b.expr(e)
	if err != nil {
		return err
	}
	if q := where[b.depth]; q != nil {
		r := p
		p = func(tup [][]uint32) bool { return q(tup) && r(tup) }
	}
	where[b.depth] = p
	return nil
}

func (b *binder) expr(e sql.Expr) (predicate, error) {
	switch x := e.(type) {
	case *sql.And:
		l, err := b.expr(x.Left)
		if err != nil {
			return nil, err
		}
		r, err := b.expr(x.Right)
		if err != nil {
			return nil, err
		}
		return func(tup [][]uint32) bool { return l(tup) && r(tup) }, nil
	case *sql.Or:
		l, err := b.expr(x.Left)
		if err != nil {
			return nil, err
		}
		r, err := b.expr(x.Right)
		if err != nil {
			return nil, err
		}
		return func(tup [][]uint32) bool { return l(tup) || r(tup) }, nil
	case *sql.IsNull:
		bc, err := b.resolve(x.Column)
		if err != nil {
			return nil, err
		}
		not := x.Not
		return func(tup [][]uint32) bool { return (tup[bc.slot][bc.idx] == 0) != not }, nil
	case *sql.Comparison:
		return b.comparison(x)
	}
	return nil, newError(b.op, "", errors.New("unsupported expression"))
}

func (b *binder) comparison(x *sql.Comparison) (predicate, error) {
	bc, err := b.resolve(x.Column)
	if err != nil {
		return nil, err
	}
	if ref, ok := x.Value.(sql.ColumnRef); ok {
		other, err := b.resolve(ref)
		if err != nil {
			return nil, err
		}
		return b.colCompare(bc, other, x.Op)
	}
	lit, err := b.literal(x.Value)
	if err != nil {
		return nil, err
	}
	return b.litCompare(bc, x.Op, lit)
}

func (b *binder) literal(v sql.Value) (any, error) {
	lit, err := litValue(v, b.args, &b.argpos)
	if err != nil {
		return nil, newError(b.op, "", err)
	}
	return lit, nil
}

func litValue(v sql.Value, args []any, pos *int) (any, error) {
	switch lit := v.(type) {
	case sql.IntLit:
		return int(lit), nil
	case sql.StringLit:
		return string(lit), nil
	case sql.Null:
		return nil, nil //nolint:nilnil // NULL is a valid value with no error
	case sql.Wildcard:
		if *pos >= len(args) {
			return nil, errArgCount
		}
		a := args[*pos]
		*pos++
		return a, nil
	}
	return nil, fmt.Errorf("unexpected value %T", v)
}

func (b *binder) colCompare(a, c boundCol, op sql.CompareOp) (predicate, error) {
	switch {
	case a.c.Type == ColumnInteger && c.c.Type == ColumnInteger:
		sa, sc := a.c.Size, c.c.Size
		return func(tup [][]uint32) bool {
			return compareInt(decodeInt(tup[a.slot][a.idx], sa), decodeInt(tup[c.slot][c.idx], sc), op)
		}, nil
	case a.c.Type == ColumnString && c.c.Type == ColumnString:
		if op != sql.OpEqual && op != sql.OpNotEqual {
			return nil, newColError(b.op, b.tabs[a.slot], a.c, errors.New("string columns support only = and <>"))
		}
		wantEqual := op == sql.OpEqual
		return func(tup [][]uint32) bool {
			return (tup[a.slot][a.idx] == tup[c.slot][c.idx]) == wantEqual
		}, nil
	default:
		return nil, newColError(b.op, b.tabs[a.slot], a.c, fmt.Errorf("cannot compare %s and %s columns", a.c.Type, c.c.Type))
	}
}

func (b *binder) litCompare(bc boundCol, op sql.CompareOp, lit any) (predicate, error) {
	slot, idx := bc.slot, bc.idx
	lit, err := normalize(lit)
	if err != nil {
		return nil, newColError(b.op, b.tabs[slot], bc.c, err)
	}
	if lit == nil {
		switch op {
		case sql.OpEqual:
			return func(tup [][]uint32) bool { return tup[slot][idx] == 0 }, nil
		case sql.OpNotEqual:
			return func(tup [][]uint32) bool { return tup[slot][idx] != 0 }, nil
		default:
			return nil, newColError(b.op, b.tabs[slot], bc.c, errors.New("cannot compare a column to NULL with that operator"))
		}
	}
	switch bc.c.Type {
	case ColumnInteger:
		n, err := intValue(lit, math.MaxInt)
		if err != nil {
			return nil, newColError(b.op, b.tabs[slot], bc.c, err)
		}
		size := bc.c.Size
		return func(tup [][]uint32) bool { return compareInt(decodeInt(tup[slot][idx], size), n, op) }, nil
	case ColumnString:
		if op != sql.OpEqual && op != sql.OpNotEqual {
			return nil, newColError(b.op, b.tabs[slot], bc.c, errors.New("string columns support only = and <>"))
		}
		s, err := stringValue(lit)
		if err != nil {
			return nil, newColError(b.op, b.tabs[slot], bc.c, err)
		}
		id, present := b.db.pool.LookupID(s)
		wantEqual := op == sql.OpEqual
		return func(tup [][]uint32) bool { return (present && tup[slot][idx] == id) == wantEqual }, nil
	default:
		return nil, newColError(b.op, b.tabs[slot], bc.c, fmt.Errorf("cannot compare a %s column", bc.c.Type))
	}
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
