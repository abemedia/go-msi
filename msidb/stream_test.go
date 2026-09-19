package msidb_test

import (
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestStreamsDML(t *testing.T) {
	db, _ := newDB(t)

	mustExec(t, db, "INSERT INTO `_Streams` (Name, Data) VALUES (?, ?)", "s", []byte("one"))
	mustExec(t, db, "UPDATE `_Streams` SET Data = ? WHERE Name = ?", []byte("two"), "s")
	got := queryRows(t, db, "SELECT Data FROM `_Streams` WHERE Name = ?", "s")
	if b, _ := io.ReadAll(got[0][0].(io.ReadSeeker)); string(b) != "two" {
		t.Errorf("after UPDATE = %q, want two", b)
	}

	r := mustExec(t, db, "DELETE FROM `_Streams` WHERE Name = ?", "s")
	if r.RowsAffected() != 1 {
		t.Errorf("DELETE RowsAffected = %d", r.RowsAffected())
	}
	if got := queryRows(t, db, "SELECT Name FROM `_Streams`"); len(got) != 0 {
		t.Errorf("after DELETE: %v", got)
	}
}

func TestDerivedStreamReplacesExplicit(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "INSERT INTO `_Streams` (Name, Data) VALUES (?, ?)", "Bin.1", []byte("explicit"))
	mustExec(t, db, "CREATE TABLE `Bin` (Id INT NOT NULL, Data OBJECT PRIMARY KEY Id)")
	mustExec(t, db, "INSERT INTO `Bin` (Id, Data) VALUES (?, ?)", 1, []byte("derived"))

	got := queryRows(t, db, "SELECT Data FROM `_Streams`")
	if len(got) != 1 {
		t.Fatalf("_Streams = %v, want 1 record", got)
	}
	if b, _ := io.ReadAll(got[0][0].(io.ReadSeeker)); string(b) != "derived" {
		t.Errorf("collision winner = %q, want derived", b)
	}
}

func TestStreamNameLength(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (K CHAR(60) NOT NULL, Data OBJECT PRIMARY KEY K)")

	// "T." + 60 chars = 62 chars -> 31 encoded wchars; must be accepted.
	if _, err := db.Exec("INSERT INTO `T` (K, Data) VALUES (?, ?)", strings.Repeat("a", 60), []byte("x")); err != nil {
		t.Errorf("31-wchar OBJECT stream name should be accepted: %v", err)
	}

	// A "\x05" name is measured unencoded: 31 wchars.
	if _, err := db.Exec("INSERT INTO _Streams (Name, Data) VALUES (?, ?)", "\x05"+strings.Repeat("a", 30), []byte("y")); err != nil {
		t.Errorf("31-wchar verbatim name should be accepted: %v", err)
	}
}

func TestNullKeyStreamName(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (A INT NOT NULL, B INT, Data OBJECT PRIMARY KEY A, B)")
	mustExec(t, db, "INSERT INTO `T` (A, B) VALUES (?, ?)", 2, nil)
	mustExec(t, db, "UPDATE `T` SET Data = ?", []byte("payload"))

	got := queryRows(t, db, "SELECT Name FROM `_Streams`")
	if diff := cmp.Diff([][]any{{"T.2."}}, got); diff != "" {
		t.Errorf("_Streams (-want +got):\n%s", diff)
	}
}

func TestStreamErrors(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"INSERT INTO `T` (K, B) VALUES ('a/b', 'x')", `msidb: insert "T": stream name "T.a/b" contains invalid character '/'`},
		{"UPDATE `T` SET B = 'x' WHERE K = 'c!d'", `msidb: update "T": stream name "T.c!d" contains invalid character '!'`},
		{"INSERT INTO `_Streams` (Name) VALUES ('n')", `msidb: insert "_Streams": Data is required`},
		{"INSERT INTO `_Streams` (Name, Data) VALUES ('n', NULL)", `msidb: insert "_Streams": Data is required`},
		{"INSERT INTO `_Streams` (Name, Data) VALUES ('s', 'y')", `msidb: insert "_Streams": already exists`},
		{"INSERT INTO `_Streams` (Name, Data) VALUES ('t', 'x') TEMPORARY", `msidb: insert "_Streams.Data": temporary field cannot hold binary data`},
		{"INSERT INTO `_Streams` (Name, Data) VALUES ('\x05FOO', 'x')", `msidb: insert "_Streams": already exists`},
		{"INSERT INTO `_Streams` (Name, Data) VALUES ('a/b', 'x')", `msidb: insert "_Streams": stream name "a/b" contains invalid character '/'`},
		{"INSERT INTO `_Streams` (Name, Data) VALUES ('\x05" + strings.Repeat("a", 31) + "', 'x')", `msidb: insert "_Streams": stream name "\x05aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" is 32 wchars, max 31`},
		{"INSERT INTO `_Streams` (Name, Data) VALUES ('\u3900', 'x')", `msidb: insert "_Streams": stream name "㤀" contains reserved character U+3900`},
		{"INSERT INTO `_Streams` (Name, Data) VALUES ('\u4840Foo', 'x')", `msidb: insert "_Streams": stream name "䡀Foo" contains reserved character U+4840`},
		{"UPDATE `_Streams` SET Data = NULL WHERE Name = 's'", `msidb: update "_Streams": Data is required`},
		{"UPDATE `_Streams` SET Name = 'r' WHERE Name = 's'", `msidb: update "_Streams.Name": cannot update a primary key`},
	}

	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (K CHAR(8) NOT NULL, B OBJECT PRIMARY KEY K)")
	mustExec(t, db, "INSERT INTO `T` (K) VALUES ('c!d')")
	mustExec(t, db, "INSERT INTO `_Streams` (Name, Data) VALUES ('s', 'x')")
	mustExec(t, db, "INSERT INTO `_Streams` (Name, Data) VALUES ('\x05Foo', 'x')")
	for _, test := range tests {
		_, err := db.Exec(test.query)
		if err == nil || err.Error() != test.want {
			t.Errorf("%s: err = %v, want %s", test.query, err, test.want)
		}
	}
}
