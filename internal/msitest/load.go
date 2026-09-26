package msitest

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/abemedia/go-cfb/oleps"
	"github.com/abemedia/go-msi/msidb"
)

// Load reads the MSI at path, returning a [Database] for comparison.
func Load(path string) (Database, error) {
	return load(path)
}

func parseColumn(name, typ string, primaryKey bool) (msidb.Column, error) {
	if typ == "" {
		return msidb.Column{}, fmt.Errorf("column %q: empty type", name)
	}
	c := msidb.Column{Name: name, PrimaryKey: primaryKey}
	first := typ[0]
	c.Nullable = first >= 'A' && first <= 'Z'
	switch first | 0x20 {
	case 'i', 'j':
		c.Type = msidb.ColumnInteger
	case 'v':
		c.Type = msidb.ColumnBinary
	case 's', 'g':
		c.Type = msidb.ColumnString
	case 'l':
		c.Type = msidb.ColumnString
		c.Localizable = true
	default:
		return msidb.Column{}, fmt.Errorf("column %q: unknown type %q", name, typ)
	}
	n, err := strconv.Atoi(typ[1:])
	if err != nil {
		return msidb.Column{}, fmt.Errorf("column %q: invalid width %q", name, typ[1:])
	}
	c.Size = n
	return c, nil
}

const streamsTable = "_Streams"

var streamsColumns = []msidb.Column{
	{Name: "Name", Type: msidb.ColumnString, Size: 62, PrimaryKey: true},
	{Name: "Data", Type: msidb.ColumnBinary, Nullable: true},
}

func streamValue(name string, data []byte) (any, error) {
	if !strings.HasPrefix(name, "\x05") {
		return data, nil
	}
	pss, err := oleps.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode %q: %w", name, err)
	}
	return pss, nil
}
