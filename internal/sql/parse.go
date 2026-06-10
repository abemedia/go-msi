package sql

import (
	"fmt"
	"strconv"
)

// parser is a recursive-descent parser with one token of lookahead in tok.
type parser struct {
	sc  scanner
	tok token // current token (one-token lookahead)
}

func (p *parser) advance() {
	if p.tok.Kind != kindEOF && p.tok.Kind != kindError {
		p.tok = p.sc.scan()
	}
}

func (p *parser) accept(k kind) bool {
	if p.tok.Kind == k {
		p.advance()
		return true
	}
	return false
}

func (p *parser) expect(k kind, what string) (token, error) {
	t := p.tok
	if t.Kind != k {
		return t, p.errorf(t, "expected %s", what)
	}
	p.advance()
	return t, nil
}

// errorf reports a parse error at t. A lexical error already recorded by the
// scanner takes precedence over format and args.
func (p *parser) errorf(t token, format string, args ...any) error {
	if p.sc.err != nil {
		return p.sc.err
	}
	return &Error{Pos: t.Pos, Msg: fmt.Sprintf(format, args...)}
}

func (p *parser) parse() (Stmt, error) {
	var (
		stmt Stmt
		err  error
	)
	switch p.tok.Kind {
	case kwSELECT:
		stmt, err = p.parseSelect()
	case kwINSERT:
		stmt, err = p.parseInsert()
	case kwUPDATE:
		stmt, err = p.parseUpdate()
	case kwDELETE:
		stmt, err = p.parseDelete()
	case kwCREATE:
		stmt, err = p.parseCreate()
	case kwALTER:
		stmt, err = p.parseAlter()
	case kwDROP:
		stmt, err = p.parseDrop()
	default:
		return nil, p.errorf(p.tok, "expected statement keyword")
	}
	if err != nil {
		return nil, err
	}
	if p.tok.Kind != kindEOF {
		return nil, p.errorf(p.tok, "unexpected trailing input")
	}
	return stmt, nil
}

func (p *parser) parseName(what string) (string, error) {
	t, err := p.expect(kindIdent, what)
	if err != nil {
		return "", err
	}
	return t.Text, nil
}

func (p *parser) parseColumnRef() (ColumnRef, error) {
	first, err := p.parseName("column name")
	if err != nil {
		return ColumnRef{}, err
	}
	if p.accept(kindDot) {
		second, err := p.parseName("column name after '.'")
		if err != nil {
			return ColumnRef{}, err
		}
		return ColumnRef{Table: first, Name: second}, nil
	}
	return ColumnRef{Name: first}, nil
}

func (p *parser) parseTableList() ([]string, error) {
	var tables []string
	for {
		name, err := p.parseName("table name")
		if err != nil {
			return nil, err
		}
		tables = append(tables, name)
		if !p.accept(kindComma) {
			return tables, nil
		}
	}
}

func (p *parser) parseSelect() (Stmt, error) {
	p.advance() // SELECT
	s := &Select{Distinct: p.accept(kwDISTINCT)}
	if !p.accept(kindStar) {
		for {
			cr, err := p.parseColumnRef()
			if err != nil {
				return nil, err
			}
			s.Columns = append(s.Columns, cr)
			if !p.accept(kindComma) {
				break
			}
		}
	}
	if _, err := p.expect(kwFROM, "'FROM'"); err != nil {
		return nil, err
	}
	from, err := p.parseTableList()
	if err != nil {
		return nil, err
	}
	s.From = from
	if p.accept(kwWHERE) {
		if s.Where, err = p.parseExpr(); err != nil {
			return nil, err
		}
	}
	if p.accept(kwORDER) {
		if _, err := p.expect(kwBY, "'BY'"); err != nil {
			return nil, err
		}
		for {
			cr, err := p.parseColumnRef()
			if err != nil {
				return nil, err
			}
			s.OrderBy = append(s.OrderBy, cr)
			if !p.accept(kindComma) {
				break
			}
		}
	}
	return s, nil
}

func (p *parser) parseInsert() (Stmt, error) {
	p.advance() // INSERT
	if _, err := p.expect(kwINTO, "'INTO'"); err != nil {
		return nil, err
	}
	ins := &Insert{}
	name, err := p.parseName("table name")
	if err != nil {
		return nil, err
	}
	ins.Table = name
	if _, err := p.expect(kindLParen, "'('"); err != nil {
		return nil, err
	}
	for {
		col, err := p.parseName("column name")
		if err != nil {
			return nil, err
		}
		ins.Columns = append(ins.Columns, col)
		if !p.accept(kindComma) {
			break
		}
	}
	if _, err := p.expect(kindRParen, "')'"); err != nil {
		return nil, err
	}
	if _, err := p.expect(kwVALUES, "'VALUES'"); err != nil {
		return nil, err
	}
	if _, err := p.expect(kindLParen, "'('"); err != nil {
		return nil, err
	}
	ins.Values = make([]Value, 0, len(ins.Columns))
	for {
		if len(ins.Values) == len(ins.Columns) {
			return nil, p.errorf(p.tok, "expected %d values to match the column list", len(ins.Columns))
		}
		v, err := p.parseConst()
		if err != nil {
			return nil, err
		}
		ins.Values = append(ins.Values, v)
		if !p.accept(kindComma) {
			break
		}
	}
	if len(ins.Values) < len(ins.Columns) {
		return nil, p.errorf(p.tok, "expected %d values to match the column list", len(ins.Columns))
	}
	if _, err := p.expect(kindRParen, "')'"); err != nil {
		return nil, err
	}
	ins.Temporary = p.accept(kwTEMPORARY)
	return ins, nil
}

func (p *parser) parseUpdate() (Stmt, error) {
	p.advance() // UPDATE
	upd := &Update{}
	name, err := p.parseName("table name")
	if err != nil {
		return nil, err
	}
	upd.Table = name
	if _, err := p.expect(kwSET, "'SET'"); err != nil {
		return nil, err
	}
	for {
		col, err := p.parseName("column name")
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(kindEq, "'='"); err != nil {
			return nil, err
		}
		v, err := p.parseConst()
		if err != nil {
			return nil, err
		}
		upd.Set = append(upd.Set, Assignment{Column: col, Value: v})
		if !p.accept(kindComma) {
			break
		}
	}
	if p.accept(kwWHERE) {
		if upd.Where, err = p.parseExpr(); err != nil {
			return nil, err
		}
	}
	return upd, nil
}

func (p *parser) parseDelete() (Stmt, error) {
	p.advance() // DELETE
	if _, err := p.expect(kwFROM, "'FROM'"); err != nil {
		return nil, err
	}
	del := &Delete{}
	from, err := p.parseTableList()
	if err != nil {
		return nil, err
	}
	del.From = from
	if p.accept(kwWHERE) {
		if del.Where, err = p.parseExpr(); err != nil {
			return nil, err
		}
	}
	return del, nil
}

func (p *parser) parseCreate() (Stmt, error) {
	p.advance() // CREATE
	if _, err := p.expect(kwTABLE, "'TABLE'"); err != nil {
		return nil, err
	}
	ct := &CreateTable{}
	name, err := p.parseName("table name")
	if err != nil {
		return nil, err
	}
	ct.Table = name
	if _, err := p.expect(kindLParen, "'('"); err != nil {
		return nil, err
	}
	for p.tok.Kind != kwPRIMARY {
		cd, err := p.parseColumnDef()
		if err != nil {
			return nil, err
		}
		ct.Columns = append(ct.Columns, cd)
		if _, err := p.expect(kindComma, "',' or 'PRIMARY KEY'"); err != nil {
			return nil, err
		}
	}
	p.advance() // PRIMARY
	if _, err := p.expect(kwKEY, "'KEY'"); err != nil {
		return nil, err
	}
	for {
		col, err := p.parseName("key column name")
		if err != nil {
			return nil, err
		}
		ct.PrimaryKey = append(ct.PrimaryKey, col)
		if !p.accept(kindComma) {
			break
		}
	}
	if _, err := p.expect(kindRParen, "')'"); err != nil {
		return nil, err
	}
	ct.Hold = p.accept(kwHOLD)
	return ct, nil
}

func (p *parser) parseAlter() (Stmt, error) {
	p.advance() // ALTER
	if _, err := p.expect(kwTABLE, "'TABLE'"); err != nil {
		return nil, err
	}
	at := &AlterTable{}
	name, err := p.parseName("table name")
	if err != nil {
		return nil, err
	}
	at.Table = name
	switch p.tok.Kind {
	case kwHOLD:
		p.advance()
		at.Action = AlterHold
	case kwFREE:
		p.advance()
		at.Action = AlterFree
	case kwADD:
		p.advance()
		at.Action = AlterAdd
		cd, err := p.parseColumnDef()
		if err != nil {
			return nil, err
		}
		at.Add = &cd
		at.Hold = p.accept(kwHOLD)
	default:
		return nil, p.errorf(p.tok, "expected 'HOLD', 'FREE', or 'ADD'")
	}
	return at, nil
}

func (p *parser) parseDrop() (Stmt, error) {
	p.advance() // DROP
	if _, err := p.expect(kwTABLE, "'TABLE'"); err != nil {
		return nil, err
	}
	name, err := p.parseName("table name")
	if err != nil {
		return nil, err
	}
	return &DropTable{Table: name}, nil
}

func (p *parser) parseColumnDef() (ColumnDef, error) {
	name, err := p.parseName("column name")
	if err != nil {
		return ColumnDef{}, err
	}
	cd := ColumnDef{Name: name}
	switch p.tok.Kind {
	case kwCHAR:
		p.advance()
		cd.Type = TypeChar
		if p.accept(kindLParen) {
			n, err := p.parseWidth()
			if err != nil {
				return ColumnDef{}, err
			}
			cd.Size = n
			if _, err := p.expect(kindRParen, "')'"); err != nil {
				return ColumnDef{}, err
			}
		}
	case kwLONGCHAR:
		p.advance()
		cd.Type = TypeLongChar
	case kwSHORT:
		p.advance()
		cd.Type = TypeShort
	case kwINT:
		p.advance()
		cd.Type = TypeInt
	case kwLONG:
		p.advance()
		cd.Type = TypeLong
	case kwOBJECT:
		p.advance()
		cd.Type = TypeObject
	default:
		return ColumnDef{}, p.errorf(p.tok, "expected column type")
	}
	for {
		switch p.tok.Kind {
		case kwLOCALIZABLE:
			p.advance()
			cd.Localizable = true
		case kwTEMPORARY:
			p.advance()
			cd.Temporary = true
		case kwNOT:
			p.advance()
			if _, err := p.expect(kwNULL, "'NULL'"); err != nil {
				return ColumnDef{}, err
			}
			cd.NotNull = true
		default:
			return cd, nil
		}
	}
}

func (p *parser) parseWidth() (int, error) {
	t, err := p.expect(kindInt, "column width")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(t.Text)
	if err != nil || n < 0 || n > 255 {
		return 0, p.errorf(t, "column width must be 0-255")
	}
	return n, nil
}

// parseExpr parses a WHERE expression with precedence OR < AND < comparison.
func (p *parser) parseExpr() (Expr, error) { return p.parseOr() }

func (p *parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.accept(kwOR) {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &Or{Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (Expr, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for p.accept(kwAND) {
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		left = &And{Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parsePrimary() (Expr, error) {
	if p.accept(kindLParen) {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(kindRParen, "')'"); err != nil {
			return nil, err
		}
		return e, nil
	}
	col, err := p.parseColumnRef()
	if err != nil {
		return nil, err
	}
	if p.accept(kwIS) {
		not := p.accept(kwNOT)
		if _, err := p.expect(kwNULL, "'NULL'"); err != nil {
			return nil, err
		}
		return &IsNull{Column: col, Not: not}, nil
	}
	op, err := p.parseCompareOp()
	if err != nil {
		return nil, err
	}
	val, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	return &Comparison{Column: col, Op: op, Value: val}, nil
}

func (p *parser) parseCompareOp() (CompareOp, error) {
	switch p.tok.Kind {
	case kindEq:
		p.advance()
		return OpEqual, nil
	case kindNe:
		p.advance()
		return OpNotEqual, nil
	case kindLt:
		p.advance()
		return OpLess, nil
	case kindLe:
		p.advance()
		return OpLessEqual, nil
	case kindGt:
		p.advance()
		return OpGreater, nil
	case kindGe:
		p.advance()
		return OpGreaterEqual, nil
	default:
		return 0, p.errorf(p.tok, "expected comparison operator")
	}
}

// parseValue parses a value: a column reference, an integer (optional leading
// '-'), a string, '?', or NULL.
func (p *parser) parseValue() (Value, error) {
	switch t := p.tok; t.Kind {
	case kindIdent:
		return p.parseColumnRef()
	case kindInt:
		p.advance()
		return p.intLit(t, 1)
	case kindMinus:
		p.advance()
		it, err := p.expect(kindInt, "integer after '-'")
		if err != nil {
			return nil, err
		}
		return p.intLit(it, -1)
	case kindString:
		p.advance()
		return StringLit(t.Text), nil
	case kindParam:
		p.advance()
		return Wildcard{}, nil
	case kwNULL:
		p.advance()
		return Null{}, nil
	default:
		return nil, p.errorf(t, "expected value")
	}
}

// parseConst parses a value that must be a literal, not a column reference.
func (p *parser) parseConst() (Value, error) {
	t := p.tok
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if _, ok := v.(ColumnRef); ok {
		return nil, p.errorf(t, "expected literal value, not a column")
	}
	return v, nil
}

func (p *parser) intLit(t token, sign int) (Value, error) {
	n, err := strconv.Atoi(t.Text)
	if err != nil {
		// The scanner guarantees digits, so Atoi can fail only by overflowing.
		return nil, p.errorf(t, "integer %s out of range", t.Text)
	}
	return IntLit(sign * n), nil
}
