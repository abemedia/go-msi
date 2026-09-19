// Package msitest compares a live [msidb.Database] against a snapshot built
// from a real-MSI oracle.
package msitest

import (
	"io"
	"strings"
	"time"

	"github.com/abemedia/go-cfb/oleps"
	"github.com/abemedia/go-msi/msidb"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// Transform returns a [cmp.Option] that converts each [msidb.Database] to a [Database] snapshot.
func Transform() cmp.Option {
	return cmp.Options{
		cmp.FilterValues(
			func(x, y any) bool {
				_, xLive := x.(*msidb.Database)
				_, yLive := y.(*msidb.Database)
				return xLive || yLive
			},
			cmp.Transformer("msidb.Database", func(v any) any {
				if db, ok := v.(*msidb.Database); ok {
					return transformDatabase(db)
				}
				return v
			}),
		),
		cmp.FilterValues(
			func(x, y any) bool {
				_, xRS := x.(io.ReadSeeker)
				_, yRS := y.(io.ReadSeeker)
				return xRS || yRS
			},
			cmp.Transformer("io.ReadSeeker", func(v any) any {
				if rs, ok := v.(io.ReadSeeker); ok {
					return transformReadSeeker(rs)
				}
				return v
			}),
		),
		// _Streams order is undefined and differs between msi.dll, msidump and msidb.
		cmp.FilterPath(func(p cmp.Path) bool {
			last, ok := p.Last().(cmp.StructField)
			if !ok || last.Name() != "Records" {
				return false
			}
			for _, step := range p {
				if mi, ok := step.(cmp.MapIndex); ok && mi.Key().String() == streamsTable {
					return true
				}
			}
			return false
		}, cmpopts.SortSlices(func(a, b map[string]any) bool {
			return a["Name"].(string) < b["Name"].(string)
		})),
		cmp.Transformer("oleps.FileTime", transformFileTime),
		cmpopts.IgnoreFields(oleps.PropertySetStream{}, "SystemIdentifier"), // implementation-specific
		cmpopts.IgnoreFields(msidb.Column{}, "Table"),                       // query-result metadata, not in the oracle
	}
}

// Database is a comparable snapshot of an [msidb.Database].
type Database struct {
	Codepage uint16
	Tables   map[string]Table
}

// Table is a comparable snapshot of one table of an [msidb.Database].
type Table struct {
	Columns []msidb.Column
	Records []map[string]any
}

func transformDatabase(db *msidb.Database) Database {
	s := Database{
		Codepage: db.Codepage(),
		Tables:   make(map[string]Table),
	}
	// _Tables lists only user tables; _Streams is queried explicitly.
	for _, name := range append(userTables(db), streamsTable) {
		s.Tables[name] = transformTable(db, name)
	}
	return s
}

func userTables(db *msidb.Database) []string {
	rows, err := db.Query("SELECT Name FROM _Tables")
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			panic(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
	return names
}

func transformTable(db *msidb.Database, name string) Table {
	rows, err := db.Query("SELECT * FROM `" + name + "`")
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	cols := rows.Columns()
	var records []map[string]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			panic(err)
		}
		rec := make(map[string]any, len(cols))
		for i, c := range cols {
			rec[c.Name] = vals[i]
		}

		if name == streamsTable && strings.HasPrefix(rec["Name"].(string), "\x05") {
			if rs, ok := rec["Data"].(io.ReadSeeker); ok {
				pss, err := oleps.Decode(rs)
				if err != nil {
					panic(err)
				}
				rec["Data"] = pss
			}
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
	return Table{Columns: cols, Records: records}
}

func transformReadSeeker(rs io.ReadSeeker) []byte {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		panic(err)
	}
	b, err := io.ReadAll(rs)
	if err != nil {
		panic(err)
	}
	return b
}

func transformFileTime(t oleps.FileTime) time.Time { return time.Time(t) }
