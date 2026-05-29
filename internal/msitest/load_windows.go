//go:build windows

package msitest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abemedia/go-cfb/oleps"
	"github.com/abemedia/go-msi/internal/msiquery"
	"github.com/abemedia/go-msi/msidb"
)

// load reads the MSI at path using msi.dll.
func load(path string) (Database, error) {
	d, err := msiquery.OpenDatabase(path, msiquery.ReadOnly)
	if err != nil {
		return Database{}, err
	}
	defer d.Close()

	cp, err := codepage(d)
	if err != nil {
		return Database{}, err
	}
	names, err := tableNames(d)
	if err != nil {
		return Database{}, err
	}
	tables := make(map[string]Table, len(names))
	for _, name := range names {
		read := readTable
		if name == streamsTable {
			read = readStreams
		}
		tbl, err := read(d, name)
		if err != nil {
			return Database{}, fmt.Errorf("table %s: %w", name, err)
		}
		tables[name] = tbl
	}
	return Database{Codepage: cp, Tables: tables}, nil
}

// readStreams reads the _Streams table.
func readStreams(d msiquery.Database, _ string) (Table, error) {
	cols := []msidb.Column{
		{Name: "Name", Type: msidb.ColumnString, Size: 62, PrimaryKey: true},
		{Name: "Data", Type: msidb.ColumnBinary, Nullable: true},
	}
	v, err := d.OpenView("SELECT `Name`, `Data` FROM `_Streams`")
	if err != nil {
		return Table{}, err
	}
	defer v.Close()
	if err := v.Execute(0); err != nil {
		return Table{}, err
	}

	var records []map[string]any
	for {
		rec, err := v.Fetch()
		if err != nil {
			return Table{}, err
		}
		if rec == 0 {
			break
		}
		row, err := streamRecord(rec)
		rec.Close()
		if err != nil {
			return Table{}, err
		}
		records = append(records, row)
	}
	return Table{Columns: cols, Records: records}, nil
}

// streamRecord reads one _Streams record, decoding property-set streams.
func streamRecord(rec msiquery.Record) (map[string]any, error) {
	name, err := rec.GetString(1)
	if err != nil {
		return nil, err
	}
	row := map[string]any{"Name": name, "Data": nil}
	if rec.IsNull(2) {
		return row, nil
	}
	data, err := readStream(rec, 2)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(name, "\x05") {
		pss, err := oleps.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decode %q: %w", name, err)
		}
		row["Data"] = pss
		return row, nil
	}
	row["Data"] = data
	return row, nil
}

// codepage returns the database code page.
func codepage(d msiquery.Database) (uint16, error) {
	dir, err := os.MkdirTemp("", "msicp")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)

	if err := d.Export("_ForceCodepage", dir, "cp.idt"); err != nil {
		return 0, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "cp.idt"))
	if err != nil {
		return 0, err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		field, _, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(field); err == nil {
			return uint16(n), nil
		}
	}
	return 0, errors.New("code page not found in _ForceCodepage export")
}

// tableNames lists the user tables plus _Streams.
func tableNames(d msiquery.Database) ([]string, error) {
	v, err := d.OpenView("SELECT `Name` FROM `_Tables`")
	if err != nil {
		return nil, err
	}
	defer v.Close()
	if err := v.Execute(0); err != nil {
		return nil, err
	}
	names := []string{streamsTable}
	for {
		rec, err := v.Fetch()
		if err != nil {
			return nil, err
		}
		if rec == 0 {
			break
		}
		name, err := rec.GetString(1)
		rec.Close()
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

func readTable(d msiquery.Database, name string) (Table, error) {
	v, err := d.OpenView("SELECT * FROM `" + name + "`")
	if err != nil {
		return Table{}, err
	}
	defer v.Close()
	if err := v.Execute(0); err != nil {
		return Table{}, err
	}
	cols, err := columns(d, v, name)
	if err != nil {
		return Table{}, err
	}

	var records []map[string]any
	for {
		rec, err := v.Fetch()
		if err != nil {
			return Table{}, err
		}
		if rec == 0 {
			break
		}
		row, err := readRecord(rec, cols)
		rec.Close()
		if err != nil {
			return Table{}, err
		}
		records = append(records, row)
	}
	return Table{Columns: cols, Records: records}, nil
}

func columns(d msiquery.Database, v msiquery.View, table string) ([]msidb.Column, error) {
	names, err := v.ColumnInfo(msiquery.ColumnNames)
	if err != nil {
		return nil, err
	}
	defer names.Close()
	types, err := v.ColumnInfo(msiquery.ColumnTypes)
	if err != nil {
		return nil, err
	}
	defer types.Close()
	keys, err := primaryKeys(d, table)
	if err != nil {
		return nil, err
	}

	n := names.FieldCount()
	cols := make([]msidb.Column, n)
	for i := uint32(1); i <= n; i++ {
		name, err := names.GetString(i)
		if err != nil {
			return nil, err
		}
		typ, err := types.GetString(i)
		if err != nil {
			return nil, err
		}
		col, err := parseColumn(name, typ, keys[name])
		if err != nil {
			return nil, err
		}
		cols[i-1] = col
	}
	return cols, nil
}

func primaryKeys(d msiquery.Database, table string) (map[string]bool, error) {
	rec, err := d.PrimaryKeys(table)
	if err != nil {
		return nil, err
	}
	defer rec.Close()
	keys := make(map[string]bool)
	for i := uint32(1); i <= rec.FieldCount(); i++ {
		name, err := rec.GetString(i)
		if err != nil {
			return nil, err
		}
		keys[name] = true
	}
	return keys, nil
}

func readRecord(rec msiquery.Record, cols []msidb.Column) (map[string]any, error) {
	row := make(map[string]any, len(cols))
	for i, c := range cols {
		field := uint32(i + 1)
		if rec.IsNull(field) {
			row[c.Name] = nil
			continue
		}
		switch c.Type {
		case msidb.ColumnInteger:
			row[c.Name] = int(rec.GetInteger(field))
		case msidb.ColumnBinary:
			b, err := readStream(rec, field)
			if err != nil {
				return nil, err
			}
			row[c.Name] = b
		default:
			s, err := rec.GetString(field)
			if err != nil {
				return nil, err
			}
			row[c.Name] = s
		}
	}
	return row, nil
}

func readStream(rec msiquery.Record, field uint32) ([]byte, error) {
	buf := make([]byte, rec.DataSize(field))
	for off := 0; off < len(buf); {
		n, err := rec.ReadStream(field, buf[off:])
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
		off += n
	}
	return buf, nil
}
