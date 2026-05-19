package oleps_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/abemedia/go-msi/internal/guid"
	"github.com/abemedia/go-msi/internal/oleps"
	"github.com/google/go-cmp/cmp"
)

var cmpOpts = cmp.Options{
	cmp.Transformer("FileTime", func(ft oleps.FileTime) time.Time { return time.Time(ft) }),
	cmp.Transformer("GUID", guid.Format),
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		cp   uint16
		prop oleps.Property
	}{
		{"I2 negative", 1252, oleps.Property{ID: 10, Value: oleps.I2(-12345)}},
		{"I4", 1252, oleps.Property{ID: 11, Value: oleps.I4(0x01020304)}},
		{"FileTime zero", 1252, oleps.Property{ID: 12, Value: oleps.FileTime(time.Time{})}},
		{"FileTime min", 1252, oleps.Property{ID: 13, Value: oleps.FileTime(time.Date(1601, 1, 1, 0, 0, 0, 100, time.UTC))}},
		{"FileTime max", 1252, oleps.Property{ID: 14, Value: oleps.FileTime(time.Unix(1833029933769, 999999900))}},
		{"LPSTR empty", 1252, oleps.Property{ID: 2, Value: oleps.LPSTR("")}},
		{"LPSTR ascii", 1252, oleps.Property{ID: 2, Value: oleps.LPSTR("Installation Database")}},
		{"LPSTR 1252", 1252, oleps.Property{ID: 3, Value: oleps.LPSTR("café Ünïcode")}},
		{"LPSTR utf16", 0x04B0, oleps.Property{ID: 4, Value: oleps.LPSTR("日本語テスト")}},
		{"LPSTR utf8", 65001, oleps.Property{ID: 5, Value: oleps.LPSTR("日本語テスト")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := oleps.PropertySetStream{
				PropertySets: []oleps.PropertySet{{
					FMTID:      oleps.FMTIDSummaryInformation,
					Properties: []oleps.Property{{ID: 1, Value: oleps.I2(test.cp)}, test.prop},
				}},
			}
			b1, err := oleps.Marshal(input)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			got, err := oleps.Unmarshal(b1)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			b2, err := oleps.Marshal(got)
			if err != nil {
				t.Fatalf("Marshal (re-encode): %v", err)
			}
			if !bytes.Equal(b1, b2) {
				t.Errorf("round-trip not byte-identical\n first: %x\nsecond: %x", b1, b2)
			}
			if diff := cmp.Diff(input, got, cmpOpts); diff != "" {
				t.Errorf("decoded mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEncodeDecode(t *testing.T) {
	input := oleps.PropertySetStream{
		SystemIdentifier: 0x00020006,
		PropertySets: []oleps.PropertySet{{
			FMTID:      oleps.FMTIDSummaryInformation,
			Properties: []oleps.Property{{ID: 1, Value: oleps.I2(1252)}, {ID: 2, Value: oleps.LPSTR("hello")}},
		}},
	}
	want, err := oleps.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var buf bytes.Buffer
	if err := oleps.Encode(&buf, input); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("Encode bytes differ from Marshal\n got: %x\nwant: %x", buf.Bytes(), want)
	}
	got, err := oleps.Decode(&buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if diff := cmp.Diff(input, got, cmpOpts); diff != "" {
		t.Errorf("decoded mismatch (-want +got):\n%s", diff)
	}
}
