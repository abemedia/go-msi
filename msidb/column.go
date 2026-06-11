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

// columnWidths returns the on-disk byte width of each column and the
// per-record total.
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

// Bit positions in the _Columns.Type field, as documented for the
// _TransformView table:
// https://learn.microsoft.com/windows/win32/msi/-transformview-table
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

// packType encodes a Column into its on-disk Type bit-field.
func packType(c Column) uint32 {
	t := uint32(typePersistent)
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
		t |= uint32(c.Size) & typeSizeMask
		if c.Size == 2 {
			t |= typeShortInt
		}
	case ColumnBinary:
		t |= typeBinary
	case ColumnString:
		t |= typeString | uint32(c.Size)&typeSizeMask
	}
	return t
}

// unpackType decodes a Type bit-field into a Column with Name unset.
// Returns an error if the bit-field encodes an unsupported column shape.
func unpackType(t uint32) (Column, error) {
	if t&typePersistent == 0 {
		return Column{}, fmt.Errorf("column type %#x is not persistent", t)
	}
	c := Column{
		Size:        int(t & typeSizeMask),
		PrimaryKey:  t&typeKey != 0,
		Nullable:    t&typeNullable != 0,
		Localizable: t&typeLocalizable != 0,
	}
	switch t & typeKindMask {
	case typeLongInt, typeShortInt:
		c.Type = ColumnInteger
		if c.Size != 2 && c.Size != 4 {
			return Column{}, fmt.Errorf("integer column size %d, want 2 or 4", c.Size)
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
