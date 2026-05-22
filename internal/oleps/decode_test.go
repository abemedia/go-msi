package oleps_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/abemedia/go-msi/internal/oleps"
)

func TestUnmarshal_Errors(t *testing.T) {
	src, err := oleps.Marshal(oleps.PropertySetStream{
		PropertySets: []oleps.PropertySet{{
			FMTID: oleps.FMTIDSummaryInformation,
			Properties: []oleps.Property{
				{ID: 1, Value: oleps.I2(1252)},
				{ID: 2, Value: oleps.I4(0x01020304)},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	tests := []struct {
		name    string
		offset  int
		data    any
		want    error
		wantMsg string
	}{
		{"bad byte order", 0, uint16(0), oleps.ErrFormat, "invalid byte order"},
		{"zero sets", 24, uint32(0), oleps.ErrFormat, "invalid property set count"},
		{"three sets", 24, uint32(3), oleps.ErrFormat, "invalid property set count"},
		{"two sets unsupported", 24, uint32(2), oleps.ErrUnsupported, "more than one property set"},
		{"property set offset into header", 44, uint32(0), oleps.ErrFormat, "invalid property set offset"},
		{"property set offset past end", 44, uint32(0xFFFFFF), io.ErrUnexpectedEOF, ""},
		{"property set size past end", 48, uint32(0xFFFFFF), io.ErrUnexpectedEOF, ""},
		{"dictionary unsupported", 64, uint32(0), oleps.ErrUnsupported, "named properties"},
		{"overlapping offsets", 68, uint32(20), oleps.ErrFormat, "invalid property offset"},
		{"code page wrong type", 72, uint16(0x03), oleps.ErrFormat, "invalid code page property"},
		{"unknown code page", 76, uint16(12000), oleps.ErrUnsupported, "code page 12000"},
		{"unregistered VT", 80, uint16(0x0099), oleps.ErrUnsupported, "property type 0x0099"},
		{"nonzero property padding", 82, uint16(0xFFFF), oleps.ErrFormat, "property padding is not zero"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := bytes.Clone(src)
			if _, err := binary.Encode(data[test.offset:], binary.LittleEndian, test.data); err != nil {
				t.Fatal(err)
			}
			_, err := oleps.Unmarshal(data)
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if !strings.Contains(err.Error(), test.wantMsg) {
				t.Errorf("err = %q, want message containing %q", err, test.wantMsg)
			}
		})
	}
}

func TestUnmarshal_Truncated(t *testing.T) {
	s := oleps.PropertySetStream{
		PropertySets: []oleps.PropertySet{{
			FMTID: oleps.FMTIDSummaryInformation,
			Properties: []oleps.Property{
				{ID: 1, Value: oleps.I2(1252)},
				{ID: 2, Value: oleps.LPSTR("hello world")},
				{ID: 3, Value: oleps.I4(0x01020304)},
				{ID: 4, Value: oleps.FileTime(time.Now())},
				{ID: 5, Value: oleps.UI4(0xDEADBEEF)},
			},
		}},
	}
	full, err := oleps.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for i := range full {
		if _, err := oleps.Unmarshal(full[:i:i]); err != io.ErrUnexpectedEOF {
			t.Fatalf("Unmarshal(full[:%d]) = %v, want io.ErrUnexpectedEOF", i, err)
		}
	}
}
