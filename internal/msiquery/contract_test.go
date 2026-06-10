//go:build windows

package msiquery

// Experiments for the two pending REFACTOR.md section-3 rows on multi-table
// DML: the observable effect of UPDATE across a join (which table's records
// change, qualified and unqualified SET, both-table SET, PK immutability,
// no WHERE), and whether DELETE accepts a multi-table FROM at all — and if
// so, which table loses records.
//
// Run on Windows: go test -run TestMSI -v ./internal/msiquery/

import (
	"fmt"
	"path/filepath"
	"testing"
)

// fetchCap bounds every fetch loop so a view that never reaches
// ERROR_NO_MORE_ITEMS fails fast instead of hanging CI.
const fetchCap = 1000

// openAt creates a fresh, buffered MSI database and returns it with its path
// so a test can commit, close, and reopen or patch it.
func openAt(t *testing.T) (Database, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "experiment.msi")
	db, err := OpenDatabase(path, Create)
	if err != nil {
		t.Fatalf("OpenDatabase(%q, Create): %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// run executes a parameterless statement and fatals on error.
func run(t *testing.T, db Database, sql string) {
	t.Helper()
	if err := tryRun(db, sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// tryRun executes a parameterless statement and returns its error.
func tryRun(db Database, sql string) error {
	v, err := db.OpenView(sql)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer v.Close()
	if err := v.Execute(0); err != nil {
		return fmt.Errorf("execute: %w", err)
	}
	return nil
}

// rows runs a SELECT and returns every record's fields as strings: NULL
// fields render as NULL, everything else single-quoted (msi.dll coerces
// integers to decimal strings).
func rows(t *testing.T, db Database, sql string) [][]string {
	t.Helper()
	v, err := db.OpenView(sql)
	if err != nil {
		t.Fatalf("OpenView(%q): %v", sql, err)
	}
	defer v.Close()
	if err := v.Execute(0); err != nil {
		t.Fatalf("Execute(%q): %v", sql, err)
	}
	var out [][]string
	for i := 0; ; i++ {
		if i >= fetchCap {
			t.Fatalf("Fetch(%q): exceeded %d records without ERROR_NO_MORE_ITEMS", sql, fetchCap)
		}
		rec, err := v.Fetch()
		if err != nil {
			t.Fatalf("Fetch(%q): %v", sql, err)
		}
		if rec == 0 {
			return out
		}
		out = append(out, fields(t, rec))
		_ = rec.Close()
	}
}

// fields renders every field of rec, marking NULLs.
func fields(t *testing.T, rec Record) []string {
	t.Helper()
	n := rec.FieldCount()
	out := make([]string, n)
	for f := uint32(1); f <= n; f++ {
		if rec.IsNull(f) {
			out[f-1] = "NULL"
			continue
		}
		s, err := rec.GetString(f)
		if err != nil {
			t.Fatalf("GetString(%d): %v", f, err)
		}
		out[f-1] = "'" + s + "'"
	}
	return out
}

// TestMSIJoinUpdate captures what UPDATE across a join actually does:
// A holds x=1, y=2, z=3 and B holds x=10, y=20, so z is unmatched
// throughout.
func TestMSIJoinUpdate(t *testing.T) {
	db, _ := openAt(t)
	run(t, db, "CREATE TABLE `A` (`K` CHAR(8) NOT NULL, `V` SHORT NOT NULL PRIMARY KEY `K`)")
	run(t, db, "CREATE TABLE `B` (`K` CHAR(8) NOT NULL, `W` SHORT NOT NULL PRIMARY KEY `K`)")
	run(t, db, "INSERT INTO `A` (`K`, `V`) VALUES ('x', 1)")
	run(t, db, "INSERT INTO `A` (`K`, `V`) VALUES ('y', 2)")
	run(t, db, "INSERT INTO `A` (`K`, `V`) VALUES ('z', 3)")
	run(t, db, "INSERT INTO `B` (`K`, `W`) VALUES ('x', 10)")
	run(t, db, "INSERT INTO `B` (`K`, `W`) VALUES ('y', 20)")

	// Effect on the first table: only joined records?
	t.Logf("SET A.V = 9 over join: %v",
		tryRun(db, "UPDATE `A`, `B` SET `A`.`V` = 9 WHERE `A`.`K` = `B`.`K`"))
	t.Logf("A: %v", rows(t, db, "SELECT `K`, `V` FROM `A`"))

	// SET on the second table.
	t.Logf("SET B.W = 7 over join: %v",
		tryRun(db, "UPDATE `A`, `B` SET `B`.`W` = 7 WHERE `A`.`K` = `B`.`K`"))
	t.Logf("B: %v", rows(t, db, "SELECT `K`, `W` FROM `B`"))

	// SET on both tables in one statement.
	t.Logf("SET A.V = 5, B.W = 6 over join: %v",
		tryRun(db, "UPDATE `A`, `B` SET `A`.`V` = 5, `B`.`W` = 6 WHERE `A`.`K` = `B`.`K`"))
	t.Logf("A: %v", rows(t, db, "SELECT `K`, `V` FROM `A`"))
	t.Logf("B: %v", rows(t, db, "SELECT `K`, `W` FROM `B`"))

	// Unqualified SET column (V exists only in A).
	t.Logf("SET unqualified V = 4 over join: %v",
		tryRun(db, "UPDATE `A`, `B` SET `V` = 4 WHERE `A`.`K` = `B`.`K`"))
	t.Logf("A: %v", rows(t, db, "SELECT `K`, `V` FROM `A`"))

	// A column reference as the SET value.
	t.Logf("SET A.V = B.W over join: %v",
		tryRun(db, "UPDATE `A`, `B` SET `A`.`V` = `B`.`W` WHERE `A`.`K` = `B`.`K`"))

	// PK immutability through a join.
	t.Logf("SET A.K = 'q' over join: %v",
		tryRun(db, "UPDATE `A`, `B` SET `A`.`K` = 'q' WHERE `A`.`K` = `B`.`K`"))

	// No WHERE: the cross product matches every A record.
	t.Logf("SET A.V = 1, no WHERE: %v",
		tryRun(db, "UPDATE `A`, `B` SET `A`.`V` = 1"))
	t.Logf("A: %v", rows(t, db, "SELECT `K`, `V` FROM `A`"))
}

// TestMSIJoinDelete probes whether DELETE accepts a multi-table FROM, and
// which table loses records if it does. C and D overlap on G = 'g' only.
func TestMSIJoinDelete(t *testing.T) {
	db, _ := openAt(t)
	run(t, db, "CREATE TABLE `C` (`Id` SHORT NOT NULL, `G` CHAR(8) PRIMARY KEY `Id`)")
	run(t, db, "CREATE TABLE `D` (`Id` SHORT NOT NULL, `G` CHAR(8) PRIMARY KEY `Id`)")
	run(t, db, "INSERT INTO `C` (`Id`, `G`) VALUES (1, 'g')")
	run(t, db, "INSERT INTO `C` (`Id`, `G`) VALUES (2, 'h')")
	run(t, db, "INSERT INTO `D` (`Id`, `G`) VALUES (1, 'g')")
	run(t, db, "INSERT INTO `D` (`Id`, `G`) VALUES (2, 'i')")

	t.Logf("DELETE FROM C, D over join: %v",
		tryRun(db, "DELETE FROM `C`, `D` WHERE `C`.`G` = `D`.`G`"))
	t.Logf("C: %v", rows(t, db, "SELECT `Id`, `G` FROM `C`"))
	t.Logf("D: %v", rows(t, db, "SELECT `Id`, `G` FROM `D`"))

	t.Logf("DELETE FROM C, D, no WHERE: %v",
		tryRun(db, "DELETE FROM `C`, `D`"))
	t.Logf("C: %v", rows(t, db, "SELECT `Id`, `G` FROM `C`"))
	t.Logf("D: %v", rows(t, db, "SELECT `Id`, `G` FROM `D`"))
}
