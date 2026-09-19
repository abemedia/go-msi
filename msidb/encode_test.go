package msidb_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/msidb"
	"github.com/google/go-cmp/cmp"
)

func TestPoolRefcountParity(t *testing.T) {
	db, path := newDB(t)
	mustExec(t, db, "CREATE TABLE `Base` (Id INT NOT NULL, S CHAR(16) PRIMARY KEY Id)")
	mustExec(t, db, "INSERT INTO `Base` (Id, S) VALUES (?, ?)", 1, "base-value")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := filepath.Join(t.TempDir(), "before.msi")
	if err := os.WriteFile(before, b, 0o600); err != nil {
		t.Fatal(err)
	}

	db2, err := msidb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db2, "CREATE TABLE `Cycle` (Id INT NOT NULL, S CHAR(16) PRIMARY KEY Id)")
	// The repeated column's second value is never interned.
	mustExec(t, db2, "INSERT INTO `Cycle` (Id, S, S) VALUES (?, ?, ?)", 1, "cycle-value", "repeat-value")
	mustExec(t, db2, "DELETE FROM `Cycle`")
	mustExec(t, db2, "DROP TABLE `Cycle`")
	if err := db2.Close(); err != nil {
		t.Fatal(err)
	}

	want, err := cfb.OpenReader(before)
	if err != nil {
		t.Fatal(err)
	}
	defer want.Close()
	got, err := cfb.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if diff := cmp.Diff(want.Storage, got.Storage, cfbOptions()); diff != "" {
		t.Errorf("container changed across a balanced cycle (-before +after):\n%s", diff)
	}
}
