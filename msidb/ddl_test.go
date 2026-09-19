package msidb_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/streamname"
	"github.com/abemedia/go-msi/msidb"
	"github.com/google/go-cmp/cmp"
)

func TestCreateAlterDrop(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL PRIMARY KEY Id)")

	got := queryRows(t, db, "SELECT Name FROM _Tables")
	if diff := cmp.Diff([][]any{{"T"}}, got); diff != "" {
		t.Errorf("_Tables (-want +got):\n%s", diff)
	}

	// ALTER ADD adds a column with NULL for existing records.
	mustExec(t, db, "INSERT INTO `T` (Id) VALUES (?)", 1)
	mustExec(t, db, "ALTER TABLE `T` ADD Extra CHAR(8)")
	rows, _ := db.Query("SELECT Id, Extra FROM `T`")
	cols := rows.Columns()
	rows.Close()
	if len(cols) != 2 || cols[1].Name != "Extra" {
		t.Fatalf("ALTER ADD columns = %+v", cols)
	}
	res := queryRows(t, db, "SELECT Extra FROM `T`")
	if diff := cmp.Diff([][]any{{nil}}, res); diff != "" {
		t.Errorf("new column value (-want +got):\n%s", diff)
	}

	// DROP removes the table and cascades through the catalog.
	mustExec(t, db, "DROP TABLE `T`")
	if got := queryRows(t, db, "SELECT Name FROM _Tables"); len(got) != 0 {
		t.Errorf("_Tables after DROP = %v", got)
	}
	if got := queryRows(t, db, "SELECT Name FROM _Columns"); len(got) != 0 {
		t.Errorf("_Columns after DROP = %v", got)
	}
	if _, err := db.Query("SELECT Id FROM `T`"); err == nil {
		t.Error("expected error querying dropped table")
	}
}

func TestAlterAddNotNull(t *testing.T) {
	db, path := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL PRIMARY KEY Id)")
	mustExec(t, db, "INSERT INTO `T` (Id) VALUES (?)", 1)
	mustExec(t, db, "ALTER TABLE `T` ADD R CHAR(8) NOT NULL")

	// NOT NULL binds only new writes; existing records read NULL.
	got := queryRows(t, db, "SELECT R FROM `T`")
	if diff := cmp.Diff([][]any{{nil}}, got); diff != "" {
		t.Errorf("existing record (-want +got):\n%s", diff)
	}
	if _, err := db.Exec("INSERT INTO `T` (Id) VALUES (?)", 2); err == nil {
		t.Error("INSERT omitting the new NOT NULL column: want rejection")
	}
	mustExec(t, db, "INSERT INTO `T` (Id, R) VALUES (?, ?)", 2, "ok")

	re := reopen(t, db, path)
	got = queryRows(t, re, "SELECT Id, R FROM `T`")
	if diff := cmp.Diff([][]any{{1, nil}, {2, "ok"}}, got); diff != "" {
		t.Errorf("round-trip (-want +got):\n%s", diff)
	}
}

func TestTemporaryContentNeverPersists(t *testing.T) {
	db, path := newDB(t)

	// An all-temporary CREATE without HOLD is a successful no-op.
	mustExec(t, db, "CREATE TABLE `Tmp` (A INT NOT NULL TEMPORARY PRIMARY KEY A)")
	if _, err := db.Query("SELECT A FROM `Tmp`"); err == nil {
		t.Error("all-temp CREATE without HOLD should create nothing")
	}

	mustExec(t, db, "CREATE TABLE `Tmp` (A INT NOT NULL TEMPORARY PRIMARY KEY A) HOLD")
	mustExec(t, db, "INSERT INTO `Tmp` (A) VALUES (?)", 1)
	if got := queryRows(t, db, "SELECT Name FROM `_Tables`"); !cmp.Equal([][]any{{"Tmp"}}, got) {
		t.Errorf("_Tables with held temp table = %v", got)
	}

	// The hold is a counter.
	mustExec(t, db, "ALTER TABLE `Tmp` HOLD")
	mustExec(t, db, "ALTER TABLE `Tmp` FREE")
	if _, err := db.Query("SELECT A FROM `Tmp`"); err != nil {
		t.Errorf("temp table dropped after one FREE of two HOLDs: %v", err)
	}
	mustExec(t, db, "ALTER TABLE `Tmp` FREE")
	if _, err := db.Query("SELECT A FROM `Tmp`"); err == nil {
		t.Error("temp table should disappear at zero holds")
	}

	mustExec(t, db, "CREATE TABLE `M` (Id INT NOT NULL, S CHAR(16) TEMPORARY PRIMARY KEY Id) HOLD")
	mustExec(t, db, "INSERT INTO `M` (Id, S) VALUES (?, ?)", 1, "scratchvalue")
	rows, _ := db.Query("SELECT * FROM `M`")
	cols := rows.Columns()
	rows.Close()
	if len(cols) != 2 || !cols[1].Temporary {
		t.Fatalf("mixed table columns = %+v", cols)
	}
	// FREE without temporary content is an accepted no-op.
	mustExec(t, db, "ALTER TABLE `M` FREE")
	mustExec(t, db, "ALTER TABLE `M` FREE")

	// ADD TEMPORARY without a hold is a no-op.
	mustExec(t, db, "ALTER TABLE `M` ADD X INT TEMPORARY")
	rows, _ = db.Query("SELECT * FROM `M`")
	if cols := rows.Columns(); len(cols) != 1 {
		t.Errorf("ADD TEMPORARY without HOLD created a column: %+v", cols)
	}
	rows.Close()

	re := reopen(t, db, path)
	rows, err := re.Query("SELECT * FROM `M`")
	if err != nil {
		t.Fatalf("M after round-trip: %v", err)
	}
	if cols := rows.Columns(); len(cols) != 1 || cols[0].Name != "Id" {
		t.Errorf("M reloaded as %+v, want the persistent prefix [Id]", cols)
	}
	rows.Close()
	if got := queryRows(t, re, "SELECT Name FROM `_Tables`"); !cmp.Equal([][]any{{"M"}}, got) {
		t.Errorf("_Tables after round-trip = %v", got)
	}
	rc, err := cfb.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := fs.ReadFile(rc, "\u4840"+streamname.Encode("_StringData"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "scratchvalue") {
		t.Errorf("_StringData = %q: a temporary column's value leaked into the saved pool", data)
	}
}

func TestHoldOnPersistentColumnCounts(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL PRIMARY KEY Id)")

	mustExec(t, db, "ALTER TABLE `T` ADD C INT HOLD")
	mustExec(t, db, "ALTER TABLE `T` ADD X INT TEMPORARY")
	mustExec(t, db, "INSERT INTO `T` (Id, C, X) VALUES (?, ?, ?)", 1, 1, 2)
	mustExec(t, db, "ALTER TABLE `T` FREE")
	if _, err := db.Exec("INSERT INTO `T` (Id, C, X) VALUES (?, ?, ?)", 2, 1, 2); !errors.Is(err, msidb.ErrNotExist) {
		t.Errorf("INSERT naming X after FREE err = %v, want ErrNotExist", err)
	}
}

func TestDDLErrors(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"CREATE TABLE `X` (A INT NOT NULL, K INT NOT NULL TEMPORARY PRIMARY KEY K)", `msidb: create table "X": temporary primary key in a persistent table`},
		{"CREATE TABLE `X` (A INT NOT NULL, K INT NOT NULL TEMPORARY PRIMARY KEY K) HOLD", `msidb: create table "X": temporary primary key in a persistent table`},
		{"CREATE TABLE `x:y` (Id INT NOT NULL PRIMARY KEY Id)", `msidb: create table "x:y": name contains invalid character ':'`},
		{"CREATE TABLE `_Tables` (Id INT NOT NULL PRIMARY KEY Id)", `msidb: create table "_Tables": reserved name`},
		{"CREATE TABLE `_Columns` (Id INT NOT NULL PRIMARY KEY Id)", `msidb: create table "_Columns": reserved name`},
		{"CREATE TABLE `_Streams` (Id INT NOT NULL PRIMARY KEY Id)", `msidb: create table "_Streams": reserved name`},
		{"CREATE TABLE `_Storages` (Id INT NOT NULL PRIMARY KEY Id)", `msidb: create table "_Storages": reserved name`},
		{"CREATE TABLE `_StringPool` (Id INT NOT NULL PRIMARY KEY Id)", `msidb: create table "_StringPool": reserved name`},
		{"CREATE TABLE `_StringData` (Id INT NOT NULL PRIMARY KEY Id)", `msidb: create table "_StringData": reserved name`},
		{"ALTER TABLE `_Tables` ADD Extra CHAR(8)", `msidb: alter table "_Tables": cannot alter a system table`},
		{"ALTER TABLE `_Columns` HOLD", `msidb: alter table "_Columns": cannot alter a system table`},
		{"ALTER TABLE `_Streams` FREE", `msidb: alter table "_Streams": cannot alter a system table`},
		{"DROP TABLE `_Tables`", `msidb: drop table "_Tables": cannot drop a system table`},
		{"DROP TABLE `_Columns`", `msidb: drop table "_Columns": cannot drop a system table`},
		{"DROP TABLE `_Streams`", `msidb: drop table "_Streams": cannot drop a system table`},
	}

	db, _ := newDB(t)
	for _, test := range tests {
		_, err := db.Exec(test.query)
		if err == nil || err.Error() != test.want {
			t.Errorf("%s: err = %v, want %s", test.query, err, test.want)
		}
	}

	if got := queryRows(t, db, "SELECT Name FROM `_Tables`"); len(got) != 0 {
		t.Errorf("_Tables after rejected CREATEs = %v", got)
	}
}
