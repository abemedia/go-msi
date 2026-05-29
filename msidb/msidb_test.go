package msidb_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abemedia/go-msi/internal/msitest"
	"github.com/abemedia/go-msi/msidb"
	"github.com/google/go-cmp/cmp"
)

func TestDatabase(t *testing.T) {
	msis, err := filepath.Glob("testdata/*.msi")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range msis {
		want, err := msitest.Load(path)
		if err != nil {
			t.Fatalf("%s: snapshot: %v", path, err)
		}

		db, err := msidb.Open(path)
		if err != nil {
			t.Fatalf("%s: open: %v", path, err)
		}
		if diff := cmp.Diff(want, db, msitest.Transform()); diff != "" {
			t.Errorf("%s: open differs from oracle (-want +got):\n%s", path, diff)
		}
		db.Close()

		out := filepath.Join(t.TempDir(), "roundtrip.msi")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: read: %v", path, err)
		}
		if err := os.WriteFile(out, b, 0o600); err != nil {
			t.Fatalf("%s: write copy: %v", path, err)
		}

		mod, err := msidb.Open(out)
		if err != nil {
			t.Fatalf("%s: open copy: %v", path, err)
		}
		if err := msidb.ForcePersist(mod); err != nil {
			t.Fatalf("%s: ForcePersist: %v", path, err)
		}
		if err := mod.Close(); err != nil {
			t.Fatalf("%s: close copy: %v", path, err)
		}

		round, err := msidb.Open(out)
		if err != nil {
			t.Fatalf("%s: reopen: %v", path, err)
		}
		if diff := cmp.Diff(want, round, msitest.Transform()); diff != "" {
			t.Errorf("%s: round-trip differs from golden (-want +got):\n%s", path, diff)
		}
		round.Close()
	}
}
