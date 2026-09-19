package msidb

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
