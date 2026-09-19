package msidb //nolint:testpackage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abemedia/go-cfb"
	"github.com/abemedia/go-msi/internal/streamname"
)

func TestDecodeTableNameWithEncodingRunes(t *testing.T) {
	name := string(rune(0x3900))
	path := filepath.Join(t.TempDir(), "t.msi")
	db, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetCodepage(65001); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE `" + name + "` (A INT NOT NULL PRIMARY KEY A)",
		"INSERT INTO `" + name + "` (A) VALUES (1)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	re, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	var a int
	if err := re.QueryRow("SELECT A FROM `" + name + "`").Scan(&a); err != nil || a != 1 {
		t.Errorf("SELECT after reopen = %d, %v; want 1", a, err)
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name        string
		editDB      func(db *Database)
		editStreams func(streams map[string][]byte)
		want        string
	}{
		{
			name: "streams decoding to the same name",
			editStreams: func(s map[string][]byte) {
				s["x"], s[streamname.Encode("x")] = nil, nil
			},
			want: fmt.Sprintf(`streams "x" and %q both decode to "x"`, streamname.Encode("x")),
		},
		{
			name: "unencoded table stream name",
			editStreams: func(s map[string][]byte) {
				s[tableMarker+"T"] = s[tableStreamName("T")]
			},
			want: `table stream T has no _Tables record`,
		},
		{
			name: "table stream without _Tables record",
			editStreams: func(s map[string][]byte) {
				s[tableStreamName("X")] = nil
			},
			want: `table stream X has no _Tables record`,
		},
		{
			name: "truncated table stream",
			editStreams: func(s map[string][]byte) {
				s[tableStreamName("T")] = s[tableStreamName("T")][:7]
			},
			want: `table T: stream length 7 not a multiple of record size 4`,
		},
		{
			name: "_Columns records without _Tables record",
			editDB: func(db *Database) {
				db.tables[systemTableTables].records = nil
				delete(db.tables, "T")
			},
			want: `_Columns record for "T" has no _Tables record`,
		},
		{
			name: "_Tables record without _Columns records",
			editDB: func(db *Database) {
				db.tables[systemTableColumns].records = nil
			},
			want: `table T: no _Columns records`,
		},
		{
			name: "repeated _Tables record",
			editDB: func(db *Database) {
				table := db.tables[systemTableTables]
				table.records = append(table.records, table.records[0])
			},
			want: `table T has more than one _Tables record`,
		},
		{
			name: "nameless _Tables record",
			editDB: func(db *Database) {
				table := db.tables[systemTableTables]
				table.records = append(table.records, []uint32{0})
			},
			want: `_Tables record has no name`,
		},
		{
			name: "reserved name in _Tables",
			editDB: func(db *Database) {
				table := db.tables[systemTableTables]
				table.records = append(table.records, []uint32{db.pool.Intern("_Streams", true)})
			},
			want: `_Tables record for reserved name "_Streams"`,
		},
		{
			name: "integer kind and width disagree",
			editDB: func(db *Database) {
				db.tables[systemTableColumns].records[0][columnsType] = encodeInt(typeKey|typePersistent|typeShortInt|4, 2)
			},
			want: `table T: column "K": short integer column size 4, want 2`,
		},
		{
			name: "unknown string id",
			editDB: func(db *Database) {
				db.tables["T"].records[0][1] = 0xffff
			},
			want: `table T: unknown string id 65535 in column "S"`,
		},
		{
			name: "duplicate primary key",
			editDB: func(db *Database) {
				recs := db.tables["T"].records
				recs[1][0] = recs[0][0]
			},
			want: `table T: duplicate primary key`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.msi")
			db, err := Create(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				"CREATE TABLE `T` (K INT NOT NULL, S CHAR(8) PRIMARY KEY K)",
				"INSERT INTO `T` (K, S) VALUES (1, 'a')",
				"INSERT INTO `T` (K, S) VALUES (2, 'b')",
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if test.editDB != nil {
				test.editDB(db)
				db.dirty = true
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			if test.editStreams != nil { //nolint:nestif
				rc, err := cfb.OpenReader(path)
				if err != nil {
					t.Fatal(err)
				}
				streams := map[string][]byte{}
				for _, e := range rc.Entries {
					s := e.(*cfb.Stream)
					if streams[s.Name], err = io.ReadAll(s.Open()); err != nil {
						t.Fatal(err)
					}
				}
				if err := rc.Close(); err != nil {
					t.Fatal(err)
				}
				test.editStreams(streams)

				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				w := cfb.NewWriterV4(f)
				for name, data := range streams {
					sw, err := w.CreateStream(name)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := sw.Write(data); err != nil {
						t.Fatal(err)
					}
					if err := sw.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}

			_, err = Open(path)
			if !errors.Is(err, ErrFormat) || !strings.HasSuffix(err.Error(), ": "+test.want) {
				t.Errorf("Open = %v, want ErrFormat: %s", err, test.want)
			}
		})
	}
}
