package msiquery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abemedia/go-cfb"
)

func TestStreamNameCase(t *testing.T) {
	cases := []struct {
		name   string
		object bool
		a, b   string
	}{
		{"control distinct", false, "a", "b"},
		{"ascii case", false, "a", "A"},
		{"non-ascii case", false, "é", "É"},
		{"x05 case", false, "\x05x", "\x05X"},
		{"object control distinct", true, "a", "b"},
		{"object ascii case", true, "a", "A"},
		{"object non-ascii case", true, "é", "É"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "s.msi")
			payload := filepath.Join(dir, "payload")
			if err := os.WriteFile(payload, []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}

			db, err := OpenDatabase(path, Create)
			if err != nil {
				t.Fatal(err)
			}
			insert := "INSERT INTO `_Streams` (`Name`, `Data`) VALUES (?, ?)"
			if c.object {
				if err := execCase(db, "CREATE TABLE `Bin` (`K` CHAR(8) NOT NULL, `Data` OBJECT PRIMARY KEY `K`)", 0); err != nil {
					t.Fatal(err)
				}
				insert = "INSERT INTO `Bin` (`K`, `Data`) VALUES (?, ?)"
			}
			for _, name := range []string{c.a, c.b} {
				rec, err := CreateRecord(2)
				if err != nil {
					t.Fatal(err)
				}
				if err := rec.SetString(1, name); err != nil {
					t.Fatal(err)
				}
				if err := rec.SetStream(2, payload); err != nil {
					t.Fatal(err)
				}
				t.Logf("insert %q: err=%v", name, execCase(db, insert, rec))
				rec.Close()
			}
			t.Logf("streams before commit: %v", queryCase(db, "SELECT `Name` FROM `_Streams`"))
			t.Logf("commit: err=%v", db.Commit())
			t.Logf("close: err=%v", db.Close())

			rc, err := cfb.OpenReader(path)
			t.Logf("cfb open: err=%v", err)
			if err == nil {
				var names []string
				for _, e := range rc.Entries {
					if s, ok := e.(*cfb.Stream); ok {
						names = append(names, fmt.Sprintf("%q", s.Name))
					}
				}
				t.Logf("cfb streams: %s", strings.Join(names, " "))
				rc.Close()
			}

			db, err = OpenDatabase(path, ReadOnly)
			t.Logf("reopen: err=%v", err)
			if err != nil {
				return
			}
			defer db.Close()
			t.Logf("streams after reopen: %v", queryCase(db, "SELECT `Name` FROM `_Streams`"))
			if c.object {
				t.Logf("Bin after reopen: %v", queryCase(db, "SELECT `K`, `Data` FROM `Bin`"))
			}
		})
	}
}

func execCase(db Database, q string, rec Record) error {
	v, err := db.OpenView(q)
	if err != nil {
		return fmt.Errorf("OpenView: %w", err)
	}
	defer v.Close()
	if err := v.Execute(rec); err != nil {
		return fmt.Errorf("Execute: %w", err)
	}
	return nil
}

func queryCase(db Database, q string) []string {
	v, err := db.OpenView(q)
	if err != nil {
		return []string{"OpenView: " + err.Error()}
	}
	defer v.Close()
	if err := v.Execute(0); err != nil {
		return []string{"Execute: " + err.Error()}
	}
	var out []string
	for {
		r, err := v.Fetch()
		if err != nil {
			return append(out, "Fetch: "+err.Error())
		}
		if r == 0 {
			return out
		}
		var fields []string
		for i := uint32(1); i <= r.FieldCount(); i++ {
			if r.IsNull(i) {
				fields = append(fields, "NULL")
				continue
			}
			if r.DataSize(i) > 0 && i == 2 {
				buf := make([]byte, r.DataSize(i))
				n, err := r.ReadStream(i, buf)
				fields = append(fields, fmt.Sprintf("%q/%v", buf[:n], err))
				continue
			}
			s, err := r.GetString(i)
			if err != nil {
				s = "err:" + err.Error()
			}
			fields = append(fields, fmt.Sprintf("%q", s))
		}
		r.Close()
		out = append(out, "["+strings.Join(fields, " ")+"]")
	}
}
