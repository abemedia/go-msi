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

// Bit positions in the _Columns.Type field.
const (
	typeSizeMask    = 1<<8 - 1 // bits 0-7: size
	typeValid       = 1 << 8   // always set on a real column
	typeLocalizable = 1 << 9
	typeString      = 1 << 11
	typeNullable    = 1 << 12
	typeKey         = 1 << 13
)

// packType encodes a Column into its on-disk Type bit-field.
func packType(c Column) uint32 {
	t := uint32(typeValid)
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
	case ColumnString:
		t |= typeString | uint32(c.Size)&typeSizeMask
	case ColumnBinary:
		t |= typeString // size stays 0
	case ColumnInteger:
		t |= uint32(c.Size) & typeSizeMask
	}
	return t
}

// unpackType decodes a Type bit-field into a Column with Name unset.
// Returns an error if the bit-field encodes an unsupported column shape.
func unpackType(t uint32) (Column, error) {
	if t&typeValid == 0 {
		return Column{}, fmt.Errorf("column type %#x missing valid bit", t)
	}
	c := Column{
		Size:        int(t & typeSizeMask),
		PrimaryKey:  t&typeKey != 0,
		Nullable:    t&typeNullable != 0,
		Localizable: t&typeLocalizable != 0,
	}
	switch {
	case t&typeString == 0:
		c.Type = ColumnInteger
		if c.Size != 2 && c.Size != 4 {
			return Column{}, fmt.Errorf("int column size %d, want 2 or 4", c.Size)
		}
	case t&^typeNullable == typeString|typeValid:
		// Binary: exactly STRING|VALID, optionally NULLABLE; size bits are zero.
		c.Type = ColumnBinary
	default:
		c.Type = ColumnString
	}
	return c, nil
}
