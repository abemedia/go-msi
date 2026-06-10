package sql_test

import (
	"testing"

	sql "github.com/abemedia/go-msi/internal/sql"
	"github.com/google/go-cmp/cmp"
)

func TestParse(t *testing.T) {
	tests := []struct {
		sql  string
		want sql.Stmt
	}{
		{
			"SELECT * FROM File",
			&sql.Select{From: []string{"File"}},
		},
		{
			"SELECT `Name`, Data FROM _Streams",
			&sql.Select{
				Columns: []sql.ColumnRef{{Name: "Name"}, {Name: "Data"}},
				From:    []string{"_Streams"},
			},
		},
		{
			"select distinct a from T",
			&sql.Select{Distinct: true, Columns: []sql.ColumnRef{{Name: "a"}}, From: []string{"T"}},
		},
		{
			"SELECT a FROM T1, T2 WHERE T1.x = T2.y ORDER BY a, b",
			&sql.Select{
				Columns: []sql.ColumnRef{{Name: "a"}},
				From:    []string{"T1", "T2"},
				Where:   &sql.Comparison{Column: sql.ColumnRef{Table: "T1", Name: "x"}, Op: sql.OpEqual, Value: sql.ColumnRef{Table: "T2", Name: "y"}},
				OrderBy: []sql.ColumnRef{{Name: "a"}, {Name: "b"}},
			},
		},
		{
			"SELECT a FROM T WHERE x = 1 AND y <> 'z' OR w >= -3",
			&sql.Select{
				Columns: []sql.ColumnRef{{Name: "a"}},
				From:    []string{"T"},
				Where: &sql.Or{
					Left: &sql.And{
						Left:  &sql.Comparison{Column: sql.ColumnRef{Name: "x"}, Op: sql.OpEqual, Value: sql.IntLit(1)},
						Right: &sql.Comparison{Column: sql.ColumnRef{Name: "y"}, Op: sql.OpNotEqual, Value: sql.StringLit("z")},
					},
					Right: &sql.Comparison{Column: sql.ColumnRef{Name: "w"}, Op: sql.OpGreaterEqual, Value: sql.IntLit(-3)},
				},
			},
		},
		{
			"SELECT a FROM T WHERE a IS NULL AND b IS NOT NULL",
			&sql.Select{
				Columns: []sql.ColumnRef{{Name: "a"}},
				From:    []string{"T"},
				Where: &sql.And{
					Left:  &sql.IsNull{Column: sql.ColumnRef{Name: "a"}},
					Right: &sql.IsNull{Column: sql.ColumnRef{Name: "b"}, Not: true},
				},
			},
		},
		{
			"SELECT a FROM T WHERE (x = 1 OR y = 2) AND z = ?",
			&sql.Select{
				Columns: []sql.ColumnRef{{Name: "a"}},
				From:    []string{"T"},
				Where: &sql.And{
					Left: &sql.Or{
						Left:  &sql.Comparison{Column: sql.ColumnRef{Name: "x"}, Op: sql.OpEqual, Value: sql.IntLit(1)},
						Right: &sql.Comparison{Column: sql.ColumnRef{Name: "y"}, Op: sql.OpEqual, Value: sql.IntLit(2)},
					},
					Right: &sql.Comparison{Column: sql.ColumnRef{Name: "z"}, Op: sql.OpEqual, Value: sql.Wildcard{}},
				},
			},
		},
		{
			"SELECT T.a FROM T WHERE T.a < 1 AND T.b <= 2 AND T.c > 3 ORDER BY T.a",
			&sql.Select{
				Columns: []sql.ColumnRef{{Table: "T", Name: "a"}},
				From:    []string{"T"},
				Where: &sql.And{
					Left: &sql.And{
						Left:  &sql.Comparison{Column: sql.ColumnRef{Table: "T", Name: "a"}, Op: sql.OpLess, Value: sql.IntLit(1)},
						Right: &sql.Comparison{Column: sql.ColumnRef{Table: "T", Name: "b"}, Op: sql.OpLessEqual, Value: sql.IntLit(2)},
					},
					Right: &sql.Comparison{Column: sql.ColumnRef{Table: "T", Name: "c"}, Op: sql.OpGreater, Value: sql.IntLit(3)},
				},
				OrderBy: []sql.ColumnRef{{Table: "T", Name: "a"}},
			},
		},
		{
			"SELECT a FROM T WHERE a != NULL", // '!=' operator and NULL literal value
			&sql.Select{
				Columns: []sql.ColumnRef{{Name: "a"}},
				From:    []string{"T"},
				Where:   &sql.Comparison{Column: sql.ColumnRef{Name: "a"}, Op: sql.OpNotEqual, Value: sql.Null{}},
			},
		},
		{
			"INSERT INTO File (Name, Size) VALUES (?, 10) TEMPORARY",
			&sql.Insert{
				Table:     "File",
				Columns:   []string{"Name", "Size"},
				Values:    []sql.Value{sql.Wildcard{}, sql.IntLit(10)},
				Temporary: true,
			},
		},
		{
			"UPDATE File SET Size = 5, Name = 'x' WHERE Name = ?",
			&sql.Update{
				Table: "File",
				Set:   []sql.Assignment{{Column: "Size", Value: sql.IntLit(5)}, {Column: "Name", Value: sql.StringLit("x")}},
				Where: &sql.Comparison{Column: sql.ColumnRef{Name: "Name"}, Op: sql.OpEqual, Value: sql.Wildcard{}},
			},
		},
		{
			"DELETE FROM File WHERE Size = 0",
			&sql.Delete{
				From:  []string{"File"},
				Where: &sql.Comparison{Column: sql.ColumnRef{Name: "Size"}, Op: sql.OpEqual, Value: sql.IntLit(0)},
			},
		},
		{
			"SELECT a FROM T WHERE x = 'it''s'", // '' decodes to one quote
			&sql.Select{
				Columns: []sql.ColumnRef{{Name: "a"}},
				From:    []string{"T"},
				Where:   &sql.Comparison{Column: sql.ColumnRef{Name: "x"}, Op: sql.OpEqual, Value: sql.StringLit("it's")},
			},
		},
		{
			"CREATE TABLE `T` (Id INT NOT NULL, Name CHAR(72) LOCALIZABLE, Blob OBJECT, PRIMARY KEY Id) HOLD",
			&sql.CreateTable{
				Table: "T",
				Columns: []sql.ColumnDef{
					{Name: "Id", Type: sql.TypeInt, NotNull: true},
					{Name: "Name", Type: sql.TypeChar, Size: 72, Localizable: true},
					{Name: "Blob", Type: sql.TypeObject},
				},
				PrimaryKey: []string{"Id"},
				Hold:       true,
			},
		},
		{
			"CREATE TABLE [T2] (a CHARACTER, b LONGCHAR, c SHORT TEMPORARY, d INTEGER, PRIMARY KEY a, b)",
			&sql.CreateTable{
				Table: "T2",
				Columns: []sql.ColumnDef{
					{Name: "a", Type: sql.TypeChar},
					{Name: "b", Type: sql.TypeLongChar},
					{Name: "c", Type: sql.TypeShort, Temporary: true},
					{Name: "d", Type: sql.TypeInt},
				},
				PrimaryKey: []string{"a", "b"},
			},
		},
		{
			"ALTER TABLE Foo ADD Bar LONG TEMPORARY HOLD",
			&sql.AlterTable{
				Table:  "Foo",
				Action: sql.AlterAdd,
				Add:    &sql.ColumnDef{Name: "Bar", Type: sql.TypeLong, Temporary: true},
				Hold:   true,
			},
		},
		{
			"ALTER TABLE Foo FREE",
			&sql.AlterTable{Table: "Foo", Action: sql.AlterFree},
		},
		{
			"ALTER TABLE Foo HOLD",
			&sql.AlterTable{Table: "Foo", Action: sql.AlterHold},
		},
		{
			"DROP TABLE Foo",
			&sql.DropTable{Table: "Foo"},
		},
	}

	for _, test := range tests {
		got, err := sql.Parse(test.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", test.sql, err)
		} else if diff := cmp.Diff(test.want, got); diff != "" {
			t.Errorf("Parse(%q) mismatch (-want +got):\n%s", test.sql, diff)
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		sql  string
		want *sql.Error
	}{
		// statement dispatch
		{"", &sql.Error{Pos: 0, Msg: "expected statement keyword"}},
		{"FROBNICATE T", &sql.Error{Pos: 0, Msg: "expected statement keyword"}},
		{"UPDATE T SET a = 1 b = 2", &sql.Error{Pos: 19, Msg: "unexpected trailing input"}},

		// SELECT
		{"SELECT", &sql.Error{Pos: 6, Msg: "expected column name"}},
		{"SELECT * File", &sql.Error{Pos: 9, Msg: "expected 'FROM'"}},
		{"SELECT a. FROM T", &sql.Error{Pos: 10, Msg: "expected column name after '.'"}},
		{"SELECT * FROM 1", &sql.Error{Pos: 14, Msg: "expected table name"}},
		{"SELECT * FROM T ORDER a", &sql.Error{Pos: 22, Msg: "expected 'BY'"}},
		{"SELECT * FROM T ORDER BY 1", &sql.Error{Pos: 25, Msg: "expected column name"}},

		// INSERT
		{"INSERT T (a) VALUES (1)", &sql.Error{Pos: 7, Msg: "expected 'INTO'"}},
		{"INSERT INTO 1", &sql.Error{Pos: 12, Msg: "expected table name"}},
		{"INSERT INTO T a", &sql.Error{Pos: 14, Msg: "expected '('"}},
		{"INSERT INTO T (a, 1) VALUES (2)", &sql.Error{Pos: 18, Msg: "expected column name"}},
		{"INSERT INTO T (a VALUES (1)", &sql.Error{Pos: 17, Msg: "expected ')'"}},
		{"INSERT INTO T (a) 1", &sql.Error{Pos: 18, Msg: "expected 'VALUES'"}},
		{"INSERT INTO T (a) VALUES 1", &sql.Error{Pos: 25, Msg: "expected '('"}},
		{"INSERT INTO T (a) VALUES ()", &sql.Error{Pos: 26, Msg: "expected value"}},
		{"INSERT INTO T (a) VALUES (b)", &sql.Error{Pos: 26, Msg: "expected literal value, not a column"}},
		{"INSERT INTO T (a) VALUES (1, 2)", &sql.Error{Pos: 29, Msg: "expected 1 values to match the column list"}},
		{"INSERT INTO T (a, b) VALUES (1)", &sql.Error{Pos: 30, Msg: "expected 2 values to match the column list"}},
		{"INSERT INTO T (a) VALUES (1", &sql.Error{Pos: 27, Msg: "expected ')'"}},

		// UPDATE
		{"UPDATE 1", &sql.Error{Pos: 7, Msg: "expected table name"}},
		{"UPDATE T a = 1", &sql.Error{Pos: 9, Msg: "expected 'SET'"}},
		{"UPDATE T SET a 1", &sql.Error{Pos: 15, Msg: "expected '='"}},
		{"UPDATE T SET a = b", &sql.Error{Pos: 17, Msg: "expected literal value, not a column"}},
		{"UPDATE T SET a = 1, 2 = 3", &sql.Error{Pos: 20, Msg: "expected column name"}},
		{"UPDATE T SET a = 1 WHERE", &sql.Error{Pos: 24, Msg: "expected column name"}},

		// DELETE
		{"DELETE T", &sql.Error{Pos: 7, Msg: "expected 'FROM'"}},
		{"DELETE FROM 1", &sql.Error{Pos: 12, Msg: "expected table name"}},
		{"DELETE FROM T WHERE", &sql.Error{Pos: 19, Msg: "expected column name"}},

		// CREATE TABLE
		{"CREATE Foo", &sql.Error{Pos: 7, Msg: "expected 'TABLE'"}},
		{"CREATE TABLE 1", &sql.Error{Pos: 13, Msg: "expected table name"}},
		{"CREATE TABLE T a", &sql.Error{Pos: 15, Msg: "expected '('"}},
		{"CREATE TABLE T (1 INT, PRIMARY KEY a)", &sql.Error{Pos: 16, Msg: "expected column name"}},
		{"CREATE TABLE T (a FOO, PRIMARY KEY a)", &sql.Error{Pos: 18, Msg: "expected column type"}},
		{"CREATE TABLE T (a INT)", &sql.Error{Pos: 21, Msg: "expected ',' or 'PRIMARY KEY'"}},
		{"CREATE TABLE T (a CHAR(x), PRIMARY KEY a)", &sql.Error{Pos: 23, Msg: "expected column width"}},
		{"CREATE TABLE T (a CHAR(999), PRIMARY KEY a)", &sql.Error{Pos: 23, Msg: "column width must be 0-255"}},
		{"CREATE TABLE T (a CHAR(72, PRIMARY KEY a)", &sql.Error{Pos: 25, Msg: "expected ')'"}},
		{"CREATE TABLE T (a INT NOT x, PRIMARY KEY a)", &sql.Error{Pos: 26, Msg: "expected 'NULL'"}},
		{"CREATE TABLE T (a INT, PRIMARY a)", &sql.Error{Pos: 31, Msg: "expected 'KEY'"}},
		{"CREATE TABLE T (a INT, PRIMARY KEY 1)", &sql.Error{Pos: 35, Msg: "expected key column name"}},
		{"CREATE TABLE T (a INT, PRIMARY KEY a", &sql.Error{Pos: 36, Msg: "expected ')'"}},

		// ALTER TABLE
		{"ALTER Foo", &sql.Error{Pos: 6, Msg: "expected 'TABLE'"}},
		{"ALTER TABLE 1", &sql.Error{Pos: 12, Msg: "expected table name"}},
		{"ALTER TABLE T FOO", &sql.Error{Pos: 14, Msg: "expected 'HOLD', 'FREE', or 'ADD'"}},
		{"ALTER TABLE Foo ADD Bar BADTYPE", &sql.Error{Pos: 24, Msg: "expected column type"}},

		// DROP TABLE
		{"DROP Foo", &sql.Error{Pos: 5, Msg: "expected 'TABLE'"}},
		{"DROP TABLE 1", &sql.Error{Pos: 11, Msg: "expected table name"}},

		// WHERE expressions (shared by SELECT/UPDATE/DELETE)
		{"SELECT * FROM T WHERE 1 = a", &sql.Error{Pos: 22, Msg: "expected column name"}},
		{"SELECT * FROM T WHERE a LIKE 'x'", &sql.Error{Pos: 24, Msg: "expected comparison operator"}},
		{"SELECT a FROM T WHERE a = )", &sql.Error{Pos: 26, Msg: "expected value"}},
		{"SELECT a FROM T WHERE a = -x", &sql.Error{Pos: 27, Msg: "expected integer after '-'"}},
		{"SELECT * FROM T WHERE ()", &sql.Error{Pos: 23, Msg: "expected column name"}},
		{"SELECT a FROM T WHERE (x = 1", &sql.Error{Pos: 28, Msg: "expected ')'"}},
		{"SELECT * FROM T WHERE a IS NOT x", &sql.Error{Pos: 31, Msg: "expected 'NULL'"}},
		{"SELECT * FROM T WHERE a = 1 AND", &sql.Error{Pos: 31, Msg: "expected column name"}},
		{"SELECT * FROM T WHERE a = 1 OR", &sql.Error{Pos: 30, Msg: "expected column name"}},

		// literals & lexical
		{"SELECT * FROM `T WHERE a = 1", &sql.Error{Pos: 14, Msg: "unterminated quoted identifier"}},
		{"SELECT * FROM T;", &sql.Error{Pos: 15, Msg: "unexpected character ';'"}},
		{"SELECT * FROM T WHERE a !> 1", &sql.Error{Pos: 24, Msg: "expected '!='"}},
		{"SELECT * FROM T WHERE a = 99999999999999999999", &sql.Error{Pos: 26, Msg: "integer 99999999999999999999 out of range"}},
		{"SELECT * FROM T WHERE a = 'foo", &sql.Error{Pos: 26, Msg: "unterminated string literal"}},
	}
	for _, test := range tests {
		_, err := sql.Parse(test.sql)
		if diff := cmp.Diff(test.want, err); diff != "" {
			t.Errorf("Parse(%q) error mismatch (-want +got):\n%s", test.sql, diff)
		}
	}
}
