package msidb_test

import (
	"testing"

	"github.com/abemedia/go-msi/msidb"
	"github.com/google/go-cmp/cmp"
)

func TestPackUnpackType(t *testing.T) {
	tests := []struct {
		name string
		bits uint16
		col  msidb.Column
	}{
		{"plain int (2-byte)", 0x0502, msidb.Column{Type: msidb.ColumnInteger, Size: 2}},
		{"plain int (4-byte)", 0x0104, msidb.Column{Type: msidb.ColumnInteger, Size: 4}},
		{"int nullable", 0x1502, msidb.Column{Type: msidb.ColumnInteger, Size: 2, Nullable: true}},
		{"int primary key", 0x2502, msidb.Column{Type: msidb.ColumnInteger, Size: 2, PrimaryKey: true}},
		{"string short", 0x0d40, msidb.Column{Type: msidb.ColumnString, Size: 64}},
		{"string nullable", 0x1dff, msidb.Column{Type: msidb.ColumnString, Size: 255, Nullable: true}},
		{"string PK", 0x2d48, msidb.Column{Type: msidb.ColumnString, Size: 72, PrimaryKey: true}},
		{"string localizable", 0x0fff, msidb.Column{Type: msidb.ColumnString, Size: 255, Localizable: true}},
		{"string all flags", 0x3f20, msidb.Column{Type: msidb.ColumnString, Size: 32, PrimaryKey: true, Nullable: true, Localizable: true}},
		{"binary", 0x0900, msidb.Column{Type: msidb.ColumnBinary}},
		{"binary nullable", 0x1900, msidb.Column{Type: msidb.ColumnBinary, Nullable: true}},
		{"temporary int", 0x0402, msidb.Column{Type: msidb.ColumnInteger, Size: 2, Temporary: true}},
		{"temporary string", 0x1c08, msidb.Column{Type: msidb.ColumnString, Size: 8, Nullable: true, Temporary: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if bits := msidb.PackType(test.col); bits != test.bits {
				t.Errorf("PackType = %#04x, want %#04x", bits, test.bits)
			}
			got, err := msidb.UnpackType(test.bits)
			if err != nil {
				t.Fatalf("UnpackType: %v", err)
			}
			if diff := cmp.Diff(test.col, got); diff != "" {
				t.Errorf("UnpackType (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUnpackTypeBinaryWithWidthIsString(t *testing.T) {
	got, err := msidb.UnpackType(0x0914)
	if err != nil {
		t.Fatal(err)
	}
	want := msidb.Column{Type: msidb.ColumnString, Size: 20}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("UnpackType(0x0914) (-want +got):\n%s", diff)
	}
}

func TestUnpackTypeErrors(t *testing.T) {
	// Raw _Columns.Type bit-fields: persistent bit 0x100, type kind in bits
	// 10-11, size in bits 0-7.
	tests := []struct {
		name string
		bits uint16
	}{
		{"long integer size 0", 0x100},
		{"long integer size 2", 0x102},
		{"long integer size 3", 0x103},
		{"long integer size 5", 0x105},
		{"short integer size 0", 0x500},
		{"short integer size 4", 0x504},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := msidb.UnpackType(test.bits); err == nil {
				t.Errorf("UnpackType(%#x): want error, got nil", test.bits)
			}
		})
	}
}
