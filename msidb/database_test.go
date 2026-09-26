package msidb_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/msitest"
	"github.com/abemedia/go-msi/msidb"
	"github.com/google/go-cmp/cmp"
)

func TestFixtures(t *testing.T) {
	fixtures, err := filepath.Glob("../testdata/*.ms?")
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no MSI fixtures in testdata")
	}

	for _, path := range fixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			want, err := msitest.Load(path)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}

			db, err := msidb.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if diff := cmp.Diff(want, db, msitest.Transform()); diff != "" {
				t.Errorf("open differs from oracle (-want +got):\n%s", diff)
			}
			db.Close()

			out := filepath.Join(t.TempDir(), "roundtrip.msi")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if err := os.WriteFile(out, b, 0o600); err != nil {
				t.Fatalf("write copy: %v", err)
			}

			mod, err := msidb.Open(out)
			if err != nil {
				t.Fatalf("open copy: %v", err)
			}
			msidb.ForcePersist(mod)
			if err := mod.Close(); err != nil {
				t.Fatalf("close copy: %v", err)
			}

			round, err := msidb.Open(out)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			if diff := cmp.Diff(want, round, msitest.Transform()); diff != "" {
				t.Errorf("round-trip differs from oracle (-want +got):\n%s", diff)
			}
			round.Close()

			wantCFB, err := cfb.OpenReader(path)
			if err != nil {
				t.Fatal(err)
			}
			defer wantCFB.Close()
			gotCFB, err := cfb.OpenReader(out)
			if err != nil {
				t.Fatal(err)
			}
			defer gotCFB.Close()
			if diff := cmp.Diff(wantCFB.Storage, gotCFB.Storage, cfbOptions()); diff != "" {
				t.Errorf("container differs after re-encode (-want +got):\n%s", diff)
			}
		})
	}
}

func TestClose(t *testing.T) {
	db, _ := newDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	if cp := db.Codepage(); cp != 0 {
		t.Errorf("Codepage = %d, want 0", cp)
	}
	if _, err := db.Exec("CREATE TABLE `T` (Id INT NOT NULL PRIMARY KEY Id)"); !errors.Is(err, msidb.ErrClosed) {
		t.Errorf("Exec = %v, want ErrClosed", err)
	}
	if _, err := db.Query("SELECT Name FROM `_Tables`"); !errors.Is(err, msidb.ErrClosed) {
		t.Errorf("Query = %v, want ErrClosed", err)
	}
	if err := db.QueryRow("SELECT Name FROM `_Tables`").Scan(new(string)); !errors.Is(err, msidb.ErrClosed) {
		t.Errorf("QueryRow = %v, want ErrClosed", err)
	}
	if err := db.SetCodepage(65001); !errors.Is(err, msidb.ErrClosed) {
		t.Errorf("SetCodepage = %v, want ErrClosed", err)
	}
	if _, err := db.SummaryInformation(); !errors.Is(err, msidb.ErrClosed) {
		t.Errorf("SummaryInformation = %v, want ErrClosed", err)
	}
	if err := db.SetSummaryInformation(msidb.SummaryInformation{}); !errors.Is(err, msidb.ErrClosed) {
		t.Errorf("SetSummaryInformation = %v, want ErrClosed", err)
	}
}

func TestCodepage(t *testing.T) {
	db, _ := newDB(t)
	if cp := db.Codepage(); cp != 1252 {
		t.Errorf("default Codepage = %d, want 1252", cp)
	}

	if err := db.SetCodepage(65001); err != nil {
		t.Fatal(err)
	}
	if cp := db.Codepage(); cp != 65001 {
		t.Errorf("Codepage after set = %d, want 65001", cp)
	}

	if err := db.SetCodepage(1); err == nil || err.Error() != "msidb: set codepage: unsupported code page 1" {
		t.Errorf("SetCodepage(1) = %v, want unsupported code page", err)
	}
	if cp := db.Codepage(); cp != 65001 {
		t.Errorf("Codepage after failed set = %d, want 65001", cp)
	}
}

func TestQueryExecCrossover(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL PRIMARY KEY Id)")

	rows, err := db.Query("INSERT INTO `T` (Id) VALUES (?)", 1)
	if err != nil {
		t.Fatalf("Query(INSERT): %v", err)
	}
	if rows.Next() {
		t.Error("non-SELECT Rows should be empty")
	}
	if diff := cmp.Diff([][]any{{1}}, queryRows(t, db, "SELECT Id FROM `T`")); diff != "" {
		t.Errorf("INSERT via Query (-want +got):\n%s", diff)
	}
	if r, err := db.Exec("SELECT Id FROM `T`"); err != nil || r.RowsAffected() != 0 {
		t.Errorf("Exec(SELECT) = %v, %v; want zero Result", r, err)
	}
}

func TestParseError(t *testing.T) {
	db, _ := newDB(t)
	const query = "SELECT FROM `T`"
	const want = "msidb: parse error at offset 7: expected column name"

	_, err := db.Exec(query)
	if pe, ok := errors.AsType[*msidb.ParseError](err); !ok || pe.SQL != query || err.Error() != want {
		t.Errorf("Exec = %v, want %s", err, want)
	}
	_, err = db.Query(query)
	if pe, ok := errors.AsType[*msidb.ParseError](err); !ok || pe.SQL != query || err.Error() != want {
		t.Errorf("Query = %v, want %s", err, want)
	}
	err = db.QueryRow(query).Scan(new(int))
	if pe, ok := errors.AsType[*msidb.ParseError](err); !ok || pe.SQL != query || err.Error() != want {
		t.Errorf("QueryRow = %v, want %s", err, want)
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `T` (Id INT NOT NULL, V CHAR(8) PRIMARY KEY Id)")
	for i := 1; i <= 50; i++ {
		mustExec(t, db, "INSERT INTO `T` (Id, V) VALUES (?, ?)", i, "x")
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 30 {
				rows, err := db.Query("SELECT Id, V FROM `T` WHERE Id >= ?", 10)
				if err != nil {
					t.Error(err)
					return
				}
				for rows.Next() {
					if _, err := rows.Values(); err != nil {
						t.Error(err)
						break
					}
				}
				rows.Close()
			}
		})
	}
	wg.Go(func() {
		for n := range 60 {
			if _, err := db.Exec("UPDATE `T` SET V = ? WHERE Id = ?", "y", (n%50)+1); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()
}
