package msidb

import (
	"path/filepath"
	"testing"
)

// TestRecordWidths checks that every record keeps one cell per column, which
// the release and binary-scan paths index on without bounds checks.
func TestRecordWidths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.msi")
	db, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(sql, args...); err != nil {
			t.Fatalf("Exec(%q): %v", sql, err)
		}
	}
	check := func(step string) {
		t.Helper()
		for name, tbl := range db.tables {
			for i, rec := range tbl.records {
				if len(rec) != len(tbl.cols) {
					t.Fatalf("after %s: %s record %d: %d cells, %d columns", step, name, i, len(rec), len(tbl.cols))
				}
			}
		}
	}

	exec("CREATE TABLE `T` (Id INT NOT NULL, N INT, S CHAR(32) PRIMARY KEY Id)")
	for i := range 3 {
		exec("INSERT INTO `T` (Id, N, S) VALUES (?, ?, ?)", i, i, "v")
	}
	check("create and insert")

	exec("ALTER TABLE `T` ADD Extra CHAR(8)")
	check("alter add")

	exec("INSERT INTO `T` (Id, N, S, Extra) VALUES (?, ?, ?, ?)", 9, 9, "v", "x")
	check("insert after widening")

	exec("CREATE TABLE `Tmp` (Id INT NOT NULL, V CHAR(8) TEMPORARY PRIMARY KEY Id) HOLD")
	exec("INSERT INTO `Tmp` (Id, V) VALUES (?, ?)", 1, "t")
	check("temporary column create")

	exec("ALTER TABLE `Tmp` FREE")
	check("free of temporary columns")

	exec("CREATE TABLE `B` (Id INT NOT NULL, Data OBJECT PRIMARY KEY Id)")
	exec("INSERT INTO `B` (Id, Data) VALUES (?, ?)", 1, []byte("payload"))
	exec("INSERT INTO _Streams (Name, Data) VALUES (?, ?)", "loose", []byte("s"))
	check("binary and stream inserts")

	exec("DROP TABLE `B`")
	check("dropping a table")

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check("reopen")

	if db, err = Open("../testdata/hello.msi"); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check("open fixture")
}
