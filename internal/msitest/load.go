package msitest

import (
	"fmt"
	"strconv"

	"github.com/abemedia/go-msi/msidb"
)

const streamsTable = "_Streams"

// Load reads the MSI at path, returning a [Database] for comparison.
func Load(path string) (Database, error) {
	return load(path)
}

// parseColumn converts an MSI column type string into a [msidb.Column].
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
