// Package sql scans and parses the MSI SQL dialect into a pure-syntax AST.
package sql

import "fmt"

// Parse parses one MSI SQL statement. On a syntax error it returns an
// [*Error] carrying the byte offset of the offending token.
func Parse(sql string) (Stmt, error) {
	p := parser{sc: scanner{input: sql}}
	p.tok = p.sc.scan() // prime one token of lookahead
	return p.parse()
}

// Stmt is a parsed statement: one of [*Select], [*Insert], [*Update],
// [*Delete], [*CreateTable], [*AlterTable], or [*DropTable].
type Stmt interface{ isStmt() }

// Select is a SELECT statement. A nil Columns means SELECT *.
type Select struct {
	Distinct bool
	Columns  []ColumnRef // nil = all columns
	From     []string    // one or more tables; multiple is a comma cross-join
	Where    Expr        // nil if absent
	OrderBy  []ColumnRef // nil if absent
}

// Insert is an INSERT statement. Values has one entry per name in Columns.
type Insert struct {
	Table     string
	Columns   []string
	Values    []Value
	Temporary bool
}

// Update is an UPDATE statement.
type Update struct {
	Table string
	Set   []Assignment
	Where Expr // nil if absent
}

// Assignment is one column = value pair in an UPDATE SET list.
type Assignment struct {
	Column string
	Value  Value
}

// Delete is a DELETE statement.
type Delete struct {
	From  []string
	Where Expr // nil if absent
}

// CreateTable is a CREATE TABLE statement. PrimaryKey lists the key columns
// in declared order.
type CreateTable struct {
	Table      string
	Columns    []ColumnDef
	PrimaryKey []string
	Hold       bool
}

// AlterTable is an ALTER TABLE statement.
type AlterTable struct {
	Table  string
	Action AlterAction
	Add    *ColumnDef // set when Action is AlterAdd
	Hold   bool       // ADD ... HOLD
}

// AlterAction selects which ALTER TABLE operation an [AlterTable] performs.
type AlterAction uint8

// ALTER TABLE actions.
const (
	AlterHold AlterAction = iota
	AlterFree
	AlterAdd
)

// DropTable is a DROP TABLE statement.
type DropTable struct{ Table string }

func (*Select) isStmt()      {}
func (*Insert) isStmt()      {}
func (*Update) isStmt()      {}
func (*Delete) isStmt()      {}
func (*CreateTable) isStmt() {}
func (*AlterTable) isStmt()  {}
func (*DropTable) isStmt()   {}

// ColumnDef is one column definition in CREATE TABLE or ALTER TABLE ADD.
type ColumnDef struct {
	Name        string
	Type        DataType
	Size        int // CHAR(n); 0 if unspecified
	NotNull     bool
	Localizable bool
	Temporary   bool
}

// DataType is a column-type keyword in a DDL column definition.
type DataType uint8

// Column-type keywords.
const (
	_            DataType = iota
	TypeChar              // CHAR / CHARACTER, optionally CHAR(n)
	TypeLongChar          // LONGCHAR
	TypeShort             // SHORT
	TypeInt               // INT / INTEGER
	TypeLong              // LONG
	TypeObject            // OBJECT
)

// Expr is a WHERE-clause expression: one of [*And], [*Or], [*Comparison],
// or [*IsNull].
type Expr interface{ isExpr() }

// And is a conjunction of two expressions.
type And struct{ Left, Right Expr }

// Or is a disjunction of two expressions.
type Or struct{ Left, Right Expr }

// Comparison is `column OP value`.
type Comparison struct {
	Column ColumnRef
	Op     CompareOp
	Value  Value
}

// IsNull is `column IS NULL` or, when Not is set, `column IS NOT NULL`.
type IsNull struct {
	Column ColumnRef
	Not    bool
}

func (*And) isExpr()        {}
func (*Or) isExpr()         {}
func (*Comparison) isExpr() {}
func (*IsNull) isExpr()     {}

// CompareOp is a comparison operator.
type CompareOp uint8

// Comparison operators.
const (
	OpEqual CompareOp = iota
	OpNotEqual
	OpLess
	OpLessEqual
	OpGreater
	OpGreaterEqual
)

// Value is a literal or column reference: one of [ColumnRef], [IntLit],
// [StringLit], [Wildcard], or [Null].
type Value interface{ isValue() }

// ColumnRef names a column, optionally qualified by its table.
type ColumnRef struct {
	Table string // "" if unqualified
	Name  string
}

// IntLit is an integer literal.
type IntLit int

// StringLit is a single-quoted string literal.
type StringLit string

// Wildcard is a `?` parameter placeholder.
type Wildcard struct{}

// Null is the NULL literal.
type Null struct{}

func (ColumnRef) isValue() {}
func (IntLit) isValue()    {}
func (StringLit) isValue() {}
func (Wildcard) isValue()  {}
func (Null) isValue()      {}

// Error is a syntax error at a byte offset into the source SQL.
type Error struct {
	Pos int
	Msg string
}

func (e *Error) Error() string {
	return fmt.Sprintf("at offset %d: %s", e.Pos, e.Msg)
}
