package codepage_test

import (
	"testing"

	"github.com/abemedia/go-msi/internal/codepage"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

func TestEncoding(t *testing.T) {
	tests := []struct {
		cp   uint16
		want any
	}{
		{1200, unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)},
		{65001, unicode.UTF8},
		{1252, charmap.Windows1252},
		{28591, charmap.ISO8859_1},
		{0, nil},   // CP_ACP - not supported
		{437, nil}, // OEM US - not supported
	}

	for _, tc := range tests {
		got := codepage.Encoding(tc.cp)
		if got != tc.want {
			t.Errorf("Encoding(%d) = %v, want %v", tc.cp, got, tc.want)
		}
	}
}
