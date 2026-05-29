// Package msitest compares a live [msidb.Database] against a snapshot built
// from a real-MSI oracle.
package msitest

import (
	"io"
	"runtime"
	"slices"
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
		// msidump doesn't sort streams, so sort by name when not on Windows.
		cmp.FilterPath(func(p cmp.Path) bool {
			if runtime.GOOS == "windows" {
				return false
			}
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
		cmpopts.EquateEmpty(),
	}
}

// Database is a comparable snapshot of an [msidb.Database].
type Database struct {
	Codepage uint16
	Tables   map[string]Table
}

// Table is a comparable snapshot of an [msidb.Table].
type Table struct {
	Columns []msidb.Column
	Records []map[string]any
}

func transformDatabase(db *msidb.Database) Database {
	s := Database{
		Codepage: db.Codepage(),
		Tables:   make(map[string]Table),
	}
	for t := range db.Tables() {
		cols := slices.Collect(t.Columns())
		records := make([]map[string]any, 0, t.Len())
		for r := range t.Records() {
			rec := make(map[string]any, len(cols))
			for _, c := range cols {
				v, err := r.Field(c.Name)
				if err != nil {
					panic(err)
				}
				rec[c.Name] = v
			}

			// Parse streams with CDFV2 property-set stream prefix.
			if t.Name() == "_Streams" && strings.HasPrefix(rec["Name"].(string), "\x05") {
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
		s.Tables[t.Name()] = Table{Columns: cols, Records: records}
	}
	return s
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
