package sql_test

import (
	"testing"

	"github.com/abemedia/go-msi/internal/sql"
)

func BenchmarkParse(b *testing.B) {
	cases := []struct{ name, query string }{
		{"select_join", "SELECT A.Id, B.Label FROM A, B WHERE A.B = B.Id AND A.Id >= 5 ORDER BY A.Id"},
		{"select_string", "SELECT Name FROM File WHERE Component = 'WindowsFolder' AND Sequence >= 5 ORDER BY Sequence"},
		{"create_table", "CREATE TABLE File (File CHAR(72) NOT NULL, Component CHAR(72) NOT NULL, FileName CHAR(255) NOT NULL LOCALIZABLE, FileSize LONG NOT NULL, Version CHAR(72), Language CHAR(20), Attributes SHORT, Sequence LONG NOT NULL, PRIMARY KEY File)"},
		{"insert", "INSERT INTO File (File, Component, FileName) VALUES ('f1', 'c1', 'setup.exe') TEMPORARY"},
		{"update", "UPDATE File SET Attributes = 512, Version = '1.0' WHERE File = 'f1'"},
		{"string_escaped", "SELECT Name FROM File WHERE Name = 'it''s a test'"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := sql.Parse(c.query); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
