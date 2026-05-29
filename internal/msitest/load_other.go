//go:build !windows

package msitest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abemedia/go-cfb/oleps"
	"github.com/abemedia/go-msi/msidb"
)

// load reads the MSI at path using the msitools msidump CLI.
func load(path string) (Database, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Database{}, err
	}
	dir, err := os.MkdirTemp("", "msidump")
	if err != nil {
		return Database{}, err
	}
	defer os.RemoveAll(dir)
	if out, err := exec.Command("msidump", "-t", "-s", "-d", dir, abs).CombinedOutput(); err != nil {
		return Database{}, fmt.Errorf("msidump: %w: %s", err, out)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return Database{}, err
	}
	tables := make(map[string]Table, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".idt") {
			continue
		}
		// Pseudo-tables, not real tables: the code page comes from _ForceCodepage
		// below and the summary from the extracted _Streams.
		switch strings.TrimSuffix(e.Name(), ".idt") {
		case "_ForceCodepage", "_SummaryInformation":
			continue
		}
		name, tbl, err := parseIDT(filepath.Join(dir, e.Name()), dir)
		if err != nil {
			return Database{}, fmt.Errorf("%s: %w", e.Name(), err)
		}
		tables[name] = tbl
	}

	streams, err := readStreamFiles(filepath.Join(dir, streamsTable))
	if err != nil {
		return Database{}, err
	}
	tables[streamsTable] = streams

	cp, err := forceCodepage(dir)
	if err != nil {
		return Database{}, err
	}
	return Database{Codepage: cp, Tables: tables}, nil
}

// parseIDT reads one msidump .idt archive into a [Table], returning its name.
func parseIDT(path, dir string) (string, Table, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", Table{}, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 3 {
		return "", Table{}, fmt.Errorf("malformed .idt: %d lines", len(lines))
	}
	names := strings.Split(lines[0], "\t")
	types := strings.Split(lines[1], "\t")
	header := strings.Split(lines[2], "\t")
	if len(names) != len(types) {
		return "", Table{}, fmt.Errorf("%d column names, %d types", len(names), len(types))
	}
	pk := make(map[string]bool, len(header)-1)
	for _, k := range header[1:] {
		pk[k] = true
	}
	cols := make([]msidb.Column, len(names))
	for i := range names {
		c, err := parseColumn(names[i], types[i], pk[names[i]])
		if err != nil {
			return "", Table{}, err
		}
		cols[i] = c
	}

	var records []map[string]any
	for _, line := range lines[3:] {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		rec := make(map[string]any, len(cols))
		for i, c := range cols {
			var raw string
			if i < len(fields) {
				raw = fields[i]
			}
			v, err := idtValue(c, raw, dir)
			if err != nil {
				return "", Table{}, fmt.Errorf("column %s: %w", c.Name, err)
			}
			rec[c.Name] = v
		}
		records = append(records, rec)
	}
	return header[0], Table{Columns: cols, Records: records}, nil
}

// idtValue converts one .idt field to its [msidb.Record.Field] representation:
// nil, int, []byte for a referenced stream file, or the raw string.
func idtValue(c msidb.Column, raw, dir string) (any, error) {
	if raw == "" {
		return nil, nil //nolint:nilnil
	}
	switch c.Type {
	case msidb.ColumnInteger:
		n, err := strconv.Atoi(raw)
		return n, err
	case msidb.ColumnBinary:
		b, err := os.ReadFile(filepath.Join(dir, raw))
		return b, err
	default:
		return raw, nil
	}
}

// readStreamFiles builds the _Streams table from msidump's extracted streams.
func readStreamFiles(dir string) (Table, error) {
	cols := []msidb.Column{
		{Name: "Name", Type: msidb.ColumnString, Size: 62, PrimaryKey: true},
		{Name: "Data", Type: msidb.ColumnBinary, Nullable: true},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Table{Columns: cols}, nil
		}
		return Table{}, err
	}
	var records []map[string]any
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return Table{}, err
		}
		var value any = data
		if strings.HasPrefix(e.Name(), "\x05") {
			pss, err := oleps.Decode(bytes.NewReader(data))
			if err != nil {
				return Table{}, fmt.Errorf("decode %q: %w", e.Name(), err)
			}
			value = pss
		}
		records = append(records, map[string]any{"Name": e.Name(), "Data": value})
	}
	return Table{Columns: cols, Records: records}, nil
}

// forceCodepage reads the database code page from the _ForceCodepage archive.
func forceCodepage(dir string) (uint16, error) {
	data, err := os.ReadFile(filepath.Join(dir, "_ForceCodepage.idt"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		field, rest, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && rest == "_ForceCodepage" {
			n, err := strconv.Atoi(field)
			return uint16(n), err
		}
	}
	return 0, nil
}
