package msidb_test

import (
	"reflect"
	"testing"

	"github.com/abemedia/go-msi/msidb"
)

func TestPackUnpackType(t *testing.T) {
	tests := []struct {
		name string
		col  msidb.Column
	}{
		{"plain int (2-byte)", msidb.Column{Type: msidb.ColumnInteger, Size: 2}},
		{"plain int (4-byte)", msidb.Column{Type: msidb.ColumnInteger, Size: 4}},
		{"int nullable", msidb.Column{Type: msidb.ColumnInteger, Size: 2, Nullable: true}},
		{"int primary key", msidb.Column{Type: msidb.ColumnInteger, Size: 2, PrimaryKey: true}},
		{"string short", msidb.Column{Type: msidb.ColumnString, Size: 64}},
		{"string nullable", msidb.Column{Type: msidb.ColumnString, Size: 255, Nullable: true}},
		{"string PK", msidb.Column{Type: msidb.ColumnString, Size: 72, PrimaryKey: true}},
		{"string localizable", msidb.Column{Type: msidb.ColumnString, Size: 255, Localizable: true}},
		{"string all flags", msidb.Column{Type: msidb.ColumnString, Size: 32, PrimaryKey: true, Nullable: true, Localizable: true}},
		{"binary", msidb.Column{Type: msidb.ColumnBinary}},
		{"binary nullable", msidb.Column{Type: msidb.ColumnBinary, Nullable: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bits := msidb.PackType(test.col)
			got, err := msidb.UnpackType(bits)
			if err != nil {
				t.Fatalf("UnpackType: %v", err)
			}
			if !reflect.DeepEqual(got, test.col) {
				t.Errorf("round-trip mismatch:\n got %+v\nwant %+v\n bits %#x", got, test.col, bits)
			}
		})
	}
}

func TestUnpackTypeErrors(t *testing.T) {
	// Raw _Columns.Type bit-fields: persistent bit 0x100, type kind in bits
	// 10-11, size in bits 0-7.
	tests := []struct {
		name string
		bits uint32
	}{
		{"missing persistent bit", 0x000},
		{"kind bits without persistent bit", 0x800},
		{"integer size 0", 0x100},
		{"integer size 3", 0x103},
		{"integer size 5", 0x105},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := msidb.UnpackType(test.bits); err == nil {
				t.Errorf("UnpackType(%#x): want error, got nil", test.bits)
			}
		})
	}
}
