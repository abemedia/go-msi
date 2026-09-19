package msidb

import "fmt"

// Column describes one column of a table.
type Column struct {
	Name        string
	Type        ColumnType
	Size        int // byte width for ColumnInteger (2 or 4); max chars for ColumnString
	PrimaryKey  bool
	Nullable    bool
	Localizable bool

	// Temporary marks an in-handle column that never reaches the file.
	Temporary bool

	// Table is the source table of a column returned by [Rows.Columns].
	// It is empty for a Column used as a DDL definition.
	Table string
}

// ColumnType is the on-disk type of a column.
type ColumnType uint8

// Column types.
const (
	columnInvalid ColumnType = iota // zero value; an unset or invalid column
	ColumnString
	ColumnInteger
	ColumnBinary
)

// String returns the column type's name.
func (t ColumnType) String() string {
	switch t {
	case ColumnString:
		return "string"
	case ColumnInteger:
		return "integer"
	case ColumnBinary:
		return "binary"
	default:
		return "invalid"
	}
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
