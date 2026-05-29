// Package msicmp provides MSI specific options for the
// [github.com/google/go-cmp/cmp] package.
package msicmp

import (
	"io"
	"slices"
	"strings"
	"time"

	"github.com/abemedia/go-cfb/oleps"
	"github.com/abemedia/go-msi/msidb"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// Transform returns a [cmp.Option] that converts each [msidb.Database] to a snapshot.
func Transform() cmp.Option {
	return cmp.Options{
		cmp.Transformer("msidb.Database", transformDatabase),
		cmp.Transformer("io.ReadSeeker", transformReadSeeker),
		cmp.Transformer("oleps.FileTime", transformFileTime),
		cmpopts.IgnoreFields(oleps.PropertySetStream{}, "SystemIdentifier"), // implementation-specific
	}
}

type database struct {
	Codepage uint16
	Tables   map[string]table
}

type table struct {
	Columns []msidb.Column
	Records []map[string]any
}

func transformDatabase(db *msidb.Database) database {
	s := database{
		Codepage: db.Codepage(),
		Tables:   make(map[string]table),
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
		s.Tables[t.Name()] = table{Columns: cols, Records: records}
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
