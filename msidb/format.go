package msidb

import (
	"fmt"

	"github.com/abemedia/go-msi/internal/guid"
	"github.com/abemedia/go-msi/internal/streamname"
)

// installerCLSID identifies a compound file as a Windows Installer database.
var installerCLSID = guid.MustParse("000C1084-0000-0000-C000-000000000046")

// tableMarker prefixes every stream name that holds table content.
const tableMarker = "\u4840"

func tableStreamName(name string) string {
	return tableMarker + streamname.Encode(name)
}

// cfbForbidden holds the characters go-cfb rejects in an entry name.
const cfbForbidden = "/\\:!\x00"

// Reserved names: the system tables and the string-pool streams.
const (
	systemTableTables   = "_Tables"
	systemTableColumns  = "_Columns"
	systemTableStreams  = "_Streams"
	systemTableStorages = "_Storages"
	stringPoolName      = "_StringPool"
	stringDataName      = "_StringData"
)

func isReservedName(name string) bool {
	switch name {
	case systemTableTables, systemTableColumns, systemTableStreams, systemTableStorages, stringPoolName, stringDataName:
		return true
	}
	return false
}

func isSystemTable(name string) bool {
	switch name {
	case systemTableTables, systemTableColumns, systemTableStreams:
		return true
	}
	return false
}

func isReadOnlyTable(name string) bool {
	return name == systemTableTables || name == systemTableColumns
}

func columnWidths(schema []Column, longRefs bool) (widths []int, recordSize int) {
	widths = make([]int, len(schema))
	for i, c := range schema {
		switch c.Type {
		case ColumnInteger:
			widths[i] = c.Size
		case ColumnBinary:
			widths[i] = 2
		case ColumnString:
			if longRefs {
				widths[i] = 3
			} else {
				widths[i] = 2
			}
		}
		recordSize += widths[i]
	}
	return widths, recordSize
}

func encodeInt(v, size int) uint32 {
	if size == 2 {
		return uint32(uint16(v) ^ 0x8000)
	}
	return uint32(v) ^ 0x80000000
}

func decodeInt(raw uint32, size int) int {
	if size == 2 {
		return int(int16(raw ^ 0x8000))
	}
	return int(int32(raw ^ 0x80000000))
}

// Bit positions in the _Columns.Type field.
// See https://learn.microsoft.com/windows/win32/msi/-transformview-table
const (
	typeSizeMask    = 1<<8 - 1 // bits 0-7: column width
	typePersistent  = 1 << 8   // bit 8: persistent column (clear means temporary)
	typeLocalizable = 1 << 9   // bit 9: localizable column

	// Bits 10-11 select the data type.
	typeKindMask = 3 << 10
	typeLongInt  = 0 << 10 // long (4-byte) integer
	typeShortInt = 1 << 10 // short (2-byte) integer
	typeBinary   = 2 << 10 // binary object
	typeString   = 3 << 10 // string

	typeNullable = 1 << 12 // bit 12: nullable column
	typeKey      = 1 << 13 // bit 13: primary-key column
)

func packType(c Column) uint16 {
	var t uint16
	if !c.Temporary {
		t |= typePersistent
	}
	if c.Nullable {
		t |= typeNullable
	}
	if c.PrimaryKey {
		t |= typeKey
	}
	if c.Localizable {
		t |= typeLocalizable
	}
	switch c.Type {
	case ColumnInteger:
		t |= uint16(c.Size) & typeSizeMask
		if c.Size == 2 {
			t |= typeShortInt
		}
	case ColumnBinary:
		t |= typeBinary
	case ColumnString:
		t |= typeString | uint16(c.Size)&typeSizeMask
	}
	return t
}

// unpackType decodes a Type bit-field into a Column with Name unset. It
// returns an error if the bit-field encodes an unsupported column shape.
func unpackType(t uint16) (Column, error) {
	c := Column{
		Size:        int(t & typeSizeMask),
		PrimaryKey:  t&typeKey != 0,
		Nullable:    t&typeNullable != 0,
		Localizable: t&typeLocalizable != 0,
		Temporary:   t&typePersistent == 0,
	}
	switch t & typeKindMask {
	case typeLongInt, typeShortInt:
		c.Type = ColumnInteger
		kind, want := "long", 4
		if t&typeKindMask == typeShortInt {
			kind, want = "short", 2
		}
		if c.Size != want {
			return Column{}, fmt.Errorf("%s integer column size %d, want %d", kind, c.Size, want)
		}
	case typeBinary:
		if c.Size == 0 {
			c.Type = ColumnBinary
		} else {
			c.Type = ColumnString
		}
	case typeString:
		c.Type = ColumnString
	}
	return c, nil
}

const (
	maxColumns           = 32
	maxPersistentColumns = 31
)

const (
	streamNameCol = "Name"
	streamDataCol = "Data"
)

var (
	schemaTables = []Column{
		{Table: systemTableTables, Name: "Name", Type: ColumnString, Size: 64, PrimaryKey: true},
	}
	schemaColumns = []Column{
		{Table: systemTableColumns, Name: "Table", Type: ColumnString, Size: 64, PrimaryKey: true},
		{Table: systemTableColumns, Name: "Number", Type: ColumnInteger, Size: 2, PrimaryKey: true},
		{Table: systemTableColumns, Name: "Name", Type: ColumnString, Size: 64},
		{Table: systemTableColumns, Name: "Type", Type: ColumnInteger, Size: 2},
	}
	schemaStreams = []Column{
		{Table: systemTableStreams, Name: streamNameCol, Type: ColumnString, Size: 62, PrimaryKey: true},
		{Table: systemTableStreams, Name: streamDataCol, Type: ColumnBinary, Nullable: true},
	}
)

// Cell positions of a _Columns record, matching schemaColumns.
const (
	columnsTable = iota
	columnsNumber
	columnsName
	columnsType
)
