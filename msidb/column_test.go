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
		{"string short", msidb.Column{Type: msidb.ColumnText, Size: 64}},
		{"string nullable", msidb.Column{Type: msidb.ColumnText, Size: 255, Nullable: true}},
		{"string PK", msidb.Column{Type: msidb.ColumnText, Size: 72, PrimaryKey: true}},
		{"string localizable", msidb.Column{Type: msidb.ColumnText, Size: 255, Localizable: true}},
		{"string all flags", msidb.Column{Type: msidb.ColumnText, Size: 32, PrimaryKey: true, Nullable: true, Localizable: true}},
		{"binary", msidb.Column{Type: msidb.ColumnBinary}},
		{"binary nullable", msidb.Column{Type: msidb.ColumnBinary, Nullable: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bits := msidb.PackType(tc.col)
			got, err := msidb.UnpackType(bits)
			if err != nil {
				t.Fatalf("UnpackType: %v", err)
			}
			if !reflect.DeepEqual(got, tc.col) {
				t.Errorf("round-trip mismatch:\n got %+v\nwant %+v\n bits %#x", got, tc.col, bits)
			}
		})
	}
}
