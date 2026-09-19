package msidb_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/abemedia/go-msi/msidb"
	"github.com/google/go-cmp/cmp"
)

func TestSelect(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  [][]any
	}{
		{"records in primary key order", "SELECT Id, Name FROM `T`", [][]any{{1, "apple"}, {2, "banana"}, {3, "cherry"}}},
		{"composite key order", "SELECT A, B FROM `C`", [][]any{{1, 1}, {1, 2}, {2, 1}}},
		{"where on integer", "SELECT Id FROM `T` WHERE Id >= 2", [][]any{{2}, {3}}},
		{"where on string", "SELECT Id FROM `T` WHERE Name = 'banana'", [][]any{{2}}},
		{"order by string sorts by string id", "SELECT Name FROM `T` ORDER BY Name", [][]any{{"banana"}, {"apple"}, {"cherry"}}},
		{"order by sorts null first", "SELECT N FROM `T` ORDER BY N", [][]any{{nil}, {1}, {5}}},
		{"distinct keeps first occurrence", "SELECT DISTINCT Kind FROM `T`", [][]any{{"b"}, {"a"}}},
		{"join matches null keys", "SELECT T.Id, B.Label FROM `T`, `B` WHERE T.N = B.Id", [][]any{{1, "none"}, {2, "five"}, {3, "one"}}},
		{"join with and on one table", "SELECT T.Id, B.Label FROM `T`, `B` WHERE T.N = B.Id AND T.Id = 2", [][]any{{2, "five"}}},
		{"join with or across tables", "SELECT T.Id, B.Label FROM `T`, `B` WHERE T.Id = 1 OR B.Id = 5", [][]any{{1, "none"}, {1, "one"}, {1, "five"}, {2, "five"}, {3, "five"}}},
	}

	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL, Name CHAR(8), N INT, Kind CHAR(8) PRIMARY KEY Id)")
	mustExec(t, db, "CREATE TABLE `B` (Id INT, Label CHAR(8) PRIMARY KEY Id)")
	mustExec(t, db, "CREATE TABLE `C` (A INT NOT NULL, B INT NOT NULL PRIMARY KEY A, B)")
	// Interning banana before apple makes string id order differ from both
	// alphabetical and primary key order.
	mustExec(t, db, "INSERT INTO `T` (Id, Name, N, Kind) VALUES (2, 'banana', 5, 'a')")
	mustExec(t, db, "INSERT INTO `T` (Id, Name, N, Kind) VALUES (1, 'apple', NULL, 'b')")
	mustExec(t, db, "INSERT INTO `T` (Id, Name, N, Kind) VALUES (3, 'cherry', 1, 'a')")
	mustExec(t, db, "INSERT INTO `B` (Id, Label) VALUES (5, 'five')")
	mustExec(t, db, "INSERT INTO `B` (Id, Label) VALUES (NULL, 'none')")
	mustExec(t, db, "INSERT INTO `B` (Id, Label) VALUES (1, 'one')")
	mustExec(t, db, "INSERT INTO `C` (A, B) VALUES (1, 2)")
	mustExec(t, db, "INSERT INTO `C` (A, B) VALUES (1, 1)")
	mustExec(t, db, "INSERT INTO `C` (A, B) VALUES (2, 1)")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if diff := cmp.Diff(test.want, queryRows(t, db, test.query)); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestDistinctBinarySentinel(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL, Data OBJECT PRIMARY KEY Id)")
	mustExec(t, db, "INSERT INTO `T` (Id, Data) VALUES (?, ?)", 1, []byte("aaa"))
	mustExec(t, db, "INSERT INTO `T` (Id, Data) VALUES (?, ?)", 2, []byte("bbb"))

	got := queryRows(t, db, "SELECT DISTINCT Data FROM `T`")
	if len(got) != 1 {
		t.Errorf("DISTINCT over binary = %d records, want 1 (sentinel identity)", len(got))
	}
}

func TestUpdateDelete(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL, A INT, B CHAR(8) PRIMARY KEY Id)")
	mustExec(t, db, "INSERT INTO `T` (Id, A, B) VALUES (?, ?, ?)", 1, 10, "x")
	mustExec(t, db, "INSERT INTO `T` (Id, A, B) VALUES (?, ?, ?)", 2, 20, "y")

	r := mustExec(t, db, "UPDATE `T` SET A = ?, B = ? WHERE Id = ?", 99, "z", 1)
	if r.RowsAffected() != 1 {
		t.Errorf("UPDATE RowsAffected = %d, want 1", r.RowsAffected())
	}
	got := queryRows(t, db, "SELECT A, B FROM `T` WHERE Id = ?", 1)
	if diff := cmp.Diff([][]any{{99, "z"}}, got); diff != "" {
		t.Errorf("after UPDATE (-want +got):\n%s", diff)
	}

	r = mustExec(t, db, "DELETE FROM `T` WHERE Id = ?", 2)
	if r.RowsAffected() != 1 {
		t.Errorf("DELETE RowsAffected = %d, want 1", r.RowsAffected())
	}
	got = queryRows(t, db, "SELECT Id FROM `T`")
	if diff := cmp.Diff([][]any{{1}}, got); diff != "" {
		t.Errorf("after DELETE (-want +got):\n%s", diff)
	}
}

func TestUpdateAcrossJoin(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `A` (K CHAR(8) NOT NULL, V INT NOT NULL PRIMARY KEY K)")
	mustExec(t, db, "CREATE TABLE `B` (K CHAR(8) NOT NULL, W INT NOT NULL PRIMARY KEY K)")
	for _, kv := range [][2]any{{"x", 1}, {"y", 2}, {"z", 3}} {
		mustExec(t, db, "INSERT INTO `A` (K, V) VALUES (?, ?)", kv[0], kv[1])
	}
	mustExec(t, db, "INSERT INTO `B` (K, W) VALUES (?, ?)", "x", 10)
	mustExec(t, db, "INSERT INTO `B` (K, W) VALUES (?, ?)", "y", 20)

	r := mustExec(t, db, "UPDATE `A`, `B` SET A.V = ? WHERE A.K = B.K", 9)
	if r.RowsAffected() != 2 {
		t.Errorf("join UPDATE RowsAffected = %d, want 2", r.RowsAffected())
	}
	got := queryRows(t, db, "SELECT K, V FROM `A`")
	if diff := cmp.Diff([][]any{{"x", 9}, {"y", 9}, {"z", 3}}, got); diff != "" {
		t.Errorf("matched records only (-want +got):\n%s", diff)
	}

	// V is unqualified but unambiguous.
	mustExec(t, db, "UPDATE `A`, `B` SET V = ?, B.W = ? WHERE A.K = B.K", 5, 6)
	if got := queryRows(t, db, "SELECT W FROM `B`"); !cmp.Equal([][]any{{6}, {6}}, got) {
		t.Errorf("B after both-table SET = %v", got)
	}

	// Without WHERE the cross product matches every A record.
	mustExec(t, db, "UPDATE `A`, `B` SET A.V = ?", 1)
	if got := queryRows(t, db, "SELECT V FROM `A`"); !cmp.Equal([][]any{{1}, {1}, {1}}, got) {
		t.Errorf("A after no-WHERE join update = %v", got)
	}
}

func TestRepeatedColumnKeepsFirstValue(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL, S CHAR(8) PRIMARY KEY Id)")

	mustExec(t, db, "INSERT INTO `T` (Id, S, S) VALUES (1, 'aaa', 'bbb')")
	mustExec(t, db, "INSERT INTO `T` (Id, S, S) VALUES (?, ?, ?)", 2, "aaa", "bbb")
	mustExec(t, db, "INSERT INTO `T` (Id, S, S) VALUES (3, ?, 'bbb')", nil)
	want := [][]any{{1, "aaa"}, {2, "aaa"}, {3, nil}}
	if diff := cmp.Diff(want, queryRows(t, db, "SELECT Id, S FROM `T`")); diff != "" {
		t.Errorf("repeated INSERT column (-want +got):\n%s", diff)
	}

	mustExec(t, db, "UPDATE `T` SET S = 'ccc', S = 'ddd' WHERE Id = 1")
	mustExec(t, db, "UPDATE `T` SET S = ?, S = 'ddd' WHERE Id = 2", nil)
	mustExec(t, db, "UPDATE `T` SET S = ?, S = ? WHERE Id = 3", "ccc", "ddd")
	want = [][]any{{1, "ccc"}, {2, nil}, {3, "ccc"}}
	if diff := cmp.Diff(want, queryRows(t, db, "SELECT Id, S FROM `T`")); diff != "" {
		t.Errorf("repeated SET column (-want +got):\n%s", diff)
	}
}

func TestUpdateFailsBeforeApplying(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (K CHAR(64) NOT NULL, S CHAR(8), Data OBJECT PRIMARY KEY K)")
	mustExec(t, db, "INSERT INTO `T` (K) VALUES (?)", "a")
	mustExec(t, db, "INSERT INTO `T` (K) VALUES (?)", strings.Repeat("k", 64))

	// Only the second record's derived stream name is too long.
	res, err := db.Exec("UPDATE `T` SET S = 'x', Data = ?", []byte("payload"))
	if err == nil {
		t.Fatal("expected a stream-name length error")
	}
	if res.RowsAffected() != 0 {
		t.Errorf("RowsAffected() = %d, want 0", res.RowsAffected())
	}
	got := queryRows(t, db, "SELECT S, Data FROM `T`")
	if diff := cmp.Diff([][]any{{nil, nil}, {nil, nil}}, got); diff != "" {
		t.Errorf("records after failed UPDATE (-want +got):\n%s", diff)
	}
}

func TestSetBinaryNullIgnoresStreamName(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (K CHAR(72) NOT NULL, D OBJECT PRIMARY KEY K)")
	mustExec(t, db, "INSERT INTO `T` (K) VALUES (?)", strings.Repeat("a", 70))
	mustExec(t, db, "INSERT INTO `T` (K, D) VALUES (?, ?)", "b", []byte("payload"))

	// Only record b's derived stream name is valid.
	if r := mustExec(t, db, "UPDATE `T` SET D = NULL"); r.RowsAffected() != 2 {
		t.Errorf("RowsAffected = %d, want 2", r.RowsAffected())
	}
	if got := queryRows(t, db, "SELECT Name FROM `_Streams`"); len(got) != 0 {
		t.Errorf("_Streams = %v, want empty", got)
	}
}

func TestNullIsAKeyValue(t *testing.T) {
	db, path := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (K CHAR(8), V INT NOT NULL PRIMARY KEY K)")
	mustExec(t, db, "INSERT INTO `T` (K, V) VALUES (?, ?)", nil, 1)
	mustExec(t, db, "INSERT INTO `T` (K, V) VALUES (?, ?)", "x", 2)

	if _, err := db.Exec("INSERT INTO `T` (K, V) VALUES (?, ?)", nil, 3); !errors.Is(err, msidb.ErrExist) {
		t.Errorf("second NULL key err = %v, want ErrExist", err)
	}

	re := reopen(t, db, path)
	got := queryRows(t, re, "SELECT K, V FROM `T`")
	if diff := cmp.Diff([][]any{{nil, 1}, {"x", 2}}, got); diff != "" {
		t.Errorf("NULL key round-trip (-want +got):\n%s", diff)
	}
}

func TestEmptyStringIsNull(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL, B CHAR(8) PRIMARY KEY Id)")
	mustExec(t, db, "INSERT INTO `T` (Id, B) VALUES (?, ?)", 2, "")
	mustExec(t, db, "INSERT INTO `T` (Id, B) VALUES (?, ?)", 3, "set")

	got := queryRows(t, db, "SELECT B FROM `T` WHERE Id = ?", 2)
	if diff := cmp.Diff([][]any{{nil}}, got); diff != "" {
		t.Errorf("stored empty string (-want +got):\n%s", diff)
	}
	isNull := queryRows(t, db, "SELECT Id FROM `T` WHERE B IS NULL")
	eqEmpty := queryRows(t, db, "SELECT Id FROM `T` WHERE B = ''")
	if diff := cmp.Diff(isNull, eqEmpty); diff != "" {
		t.Errorf("= '' vs IS NULL (-IS NULL +=''):\n%s", diff)
	}
	if diff := cmp.Diff([][]any{{2}}, isNull); diff != "" {
		t.Errorf("IS NULL (-want +got):\n%s", diff)
	}
}

func TestMultipleBinaryColumns(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL, D1 OBJECT, D2 OBJECT PRIMARY KEY Id)")
	mustExec(t, db, "INSERT INTO `T` (Id) VALUES (2)")

	// D2 is listed before D1 so statement order cannot be what decides.
	tests := []struct {
		name  string
		query string
		id    int
	}{
		{"insert", "INSERT INTO `T` (Id, D2, D1) VALUES (1, ?, ?)", 1},
		{"update", "UPDATE `T` SET D2 = ?, D1 = ? WHERE Id = 2", 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mustExec(t, db, test.query, []byte("two"), []byte("one"))

			got := queryRows(t, db, "SELECT D1, D2 FROM `T` WHERE Id = ?", test.id)
			for i, v := range got[0] {
				rs, ok := v.(io.ReadSeeker)
				if !ok {
					t.Fatalf("column %d not a reader: %T", i, v)
				}
				if b, _ := io.ReadAll(rs); string(b) != "two" {
					t.Errorf("column %d = %q, want two (highest column wins)", i, b)
				}
			}
		})
	}
}

func TestTemporaryBinaryAcceptsNull(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL, Data OBJECT PRIMARY KEY Id)")
	mustExec(t, db, "INSERT INTO `T` (Id) VALUES (1)")
	mustExec(t, db, "ALTER TABLE `T` ADD Tmp OBJECT TEMPORARY HOLD")
	mustExec(t, db, "UPDATE `T` SET Tmp = ? WHERE Id = 1", nil)
	mustExec(t, db, "INSERT INTO `T` (Id, Data) VALUES (2, ?) TEMPORARY", nil)
}

func TestDMLErrors(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"SELECT Nope FROM `T`", `msidb: query "T.Nope": does not exist`},
		{"SELECT K FROM `T` ORDER BY Nope", `msidb: query "T.Nope": does not exist`},
		{"SELECT K FROM `T` WHERE Nope = 1", `msidb: query "T.Nope": does not exist`},
		{"SELECT K FROM `T` WHERE X.K = 1", `msidb: query "X": does not exist`},
		{"SELECT K FROM `T` WHERE U.Id = 1", `msidb: query "U": not in FROM`},
		{"SELECT T.K FROM `T`, `U` WHERE Nope = 1", `msidb: query "Nope": does not exist`},
		{"SELECT K FROM `T` WHERE S < 'x'", `msidb: query "T.S": string columns support only = and <>`},
		{"SELECT K FROM `T` WHERE K = ?", `msidb: query: argument count mismatch`},
		{"INSERT INTO `X` (K) VALUES (1)", `msidb: insert "X": does not exist`},
		{"INSERT INTO `T` (Nope) VALUES (1)", `msidb: insert "T.Nope": does not exist`},
		{"INSERT INTO `T` (K) VALUES (?)", `msidb: insert "T": argument count mismatch`},
		{"INSERT INTO `T` (N) VALUES (1)", `msidb: insert "T.K": NULL not allowed`},
		{"INSERT INTO `T` (K) VALUES (NULL)", `msidb: insert "T.K": NULL not allowed`},
		{"INSERT INTO `T` (K) VALUES ('')", `msidb: insert "T.K": empty string not allowed in a NOT NULL column`},
		{"INSERT INTO `T` (K, N) VALUES ('k', 'x')", `msidb: insert "T.N": expected integer, got "x"`},
		{"INSERT INTO `T` (K) VALUES ('k')", `msidb: insert "T": already exists`},
		{"INSERT INTO `T` (K, B) VALUES ('t', 'x') TEMPORARY", `msidb: insert "T.B": temporary field cannot hold binary data`},
		{"INSERT INTO `TT` (Id, Data) VALUES (1, 'x')", `msidb: insert "TT.Data": temporary field cannot hold binary data`},
		{"INSERT INTO `_Tables` (Name) VALUES ('x')", `msidb: insert "_Tables": system table is read-only`},
		{"INSERT INTO `_Columns` (`Table`, Number, Name, Type) VALUES ('T', 2, 'X', 1234)", `msidb: insert "_Columns": system table is read-only`},
		{"UPDATE `T` SET Nope = 1", `msidb: update "T.Nope": does not exist`},
		{"UPDATE `T` SET N = 1 WHERE Nope = 1", `msidb: update "T.Nope": does not exist`},
		{"UPDATE `T` SET N = ?", `msidb: update: argument count mismatch`},
		{"UPDATE `T` SET N = 1 WHERE K = ?", `msidb: update: argument count mismatch`},
		{"UPDATE `T` SET K = 'z'", `msidb: update "T.K": cannot update a primary key`},
		{"UPDATE `T`, `U` SET T.K = 'z'", `msidb: update "T.K": cannot update a primary key`},
		{"UPDATE `T` SET Tmp = 'x' WHERE K = 'k'", `msidb: update "T.Tmp": temporary field cannot hold binary data`},
		{"UPDATE `_Tables` SET Name = 'X'", `msidb: update "_Tables": system table is read-only`},
		{"UPDATE `_Columns` SET Number = 9", `msidb: update "_Columns": system table is read-only`},
		{"DELETE FROM `T` WHERE Nope = 1", `msidb: delete "T.Nope": does not exist`},
		{"DELETE FROM `T` WHERE K = ?", `msidb: delete: argument count mismatch`},
		{"DELETE FROM `T`, `U`", `msidb: delete: DELETE from a join is not supported`},
		{"DELETE FROM `_Tables` WHERE Name = 'T'", `msidb: delete "_Tables": system table is read-only`},
		{"DELETE FROM `_Columns` WHERE `Table` = 'T'", `msidb: delete "_Columns": system table is read-only`},
	}

	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (K CHAR(8) NOT NULL, N INT, S CHAR(8), B OBJECT PRIMARY KEY K)")
	mustExec(t, db, "CREATE TABLE `U` (Id INT NOT NULL PRIMARY KEY Id)")
	mustExec(t, db, "CREATE TABLE `TT` (Id INT NOT NULL TEMPORARY, Data OBJECT TEMPORARY PRIMARY KEY Id) HOLD")
	mustExec(t, db, "ALTER TABLE `T` ADD Tmp OBJECT TEMPORARY HOLD")
	mustExec(t, db, "INSERT INTO `T` (K) VALUES ('k')")
	for _, test := range tests {
		_, err := db.Exec(test.query)
		if err == nil || err.Error() != test.want {
			t.Errorf("%s: err = %v, want %s", test.query, err, test.want)
		}
	}

	if _, err := db.Exec("UPDATE `T` SET Nope = 1"); !errors.Is(err, msidb.ErrNotExist) {
		t.Errorf("unknown column err = %v, want ErrNotExist", err)
	}
}
