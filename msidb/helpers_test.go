package msidb_test

import (
	"crypto/sha256"
	"io"
	"path/filepath"
	"testing"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/msidb"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// newDB returns an empty database and the path it persists to.
func newDB(t *testing.T) (*msidb.Database, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.msi")
	db, err := msidb.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

// reopen saves db to its path and opens the result.
func reopen(t *testing.T, db *msidb.Database, path string) *msidb.Database {
	t.Helper()
	msidb.ForcePersist(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	re, err := msidb.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { re.Close() })
	return re
}

func mustExec(tb testing.TB, db *msidb.Database, sql string, args ...any) msidb.Result {
	tb.Helper()
	r, err := db.Exec(sql, args...)
	if err != nil {
		tb.Fatalf("Exec(%q): %v", sql, err)
	}
	return r
}

func queryRows(t *testing.T, db *msidb.Database, sql string, args ...any) [][]any {
	t.Helper()
	rows, err := db.Query(sql, args...)
	if err != nil {
		t.Fatalf("Query(%q): %v", sql, err)
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func cfbOptions() cmp.Options {
	return cmp.Options{
		cmp.Transformer("stream", func(s *cfb.Stream) struct {
			Name      string
			StateBits uint32
			Size      int64
			SHA256    [32]byte
		} {
			h := sha256.New()
			if _, err := io.Copy(h, s.Open()); err != nil {
				panic(err)
			}
			return struct {
				Name      string
				StateBits uint32
				Size      int64
				SHA256    [32]byte
			}{s.Name, s.StateBits, s.Size, [32]byte(h.Sum(nil))}
		}),
		cmpopts.IgnoreFields(cfb.Storage{}, "Modified"),
	}
}
