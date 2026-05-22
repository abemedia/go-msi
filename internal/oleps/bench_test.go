package oleps_test

import (
	"testing"
	"time"

	"github.com/abemedia/go-msi/internal/oleps"
)

func benchFixture() oleps.PropertySetStream {
	return oleps.PropertySetStream{
		PropertySets: []oleps.PropertySet{{
			FMTID: oleps.FMTIDSummaryInformation,
			Properties: []oleps.Property{
				{ID: 1, Value: oleps.I2(1252)},
				{ID: 2, Value: oleps.LPSTR("Installation Database")},
				{ID: 3, Value: oleps.LPSTR("Acme Application 1.0")},
				{ID: 4, Value: oleps.LPSTR("Acme Inc.")},
				{ID: 5, Value: oleps.LPSTR("Installer, MSI, Database")},
				{ID: 6, Value: oleps.LPSTR("This installer database contains the logic and data required to install Acme Application 1.0.")},
				{ID: 7, Value: oleps.LPSTR("Intel;1033")},
				{ID: 8, Value: oleps.LPSTR("Acme Inc.")},
				{ID: 9, Value: oleps.LPSTR("{12345678-1234-1234-1234-123456789012}")},
				{ID: 12, Value: oleps.FileTime(time.Date(2024, 1, 15, 9, 30, 0, 0, time.UTC))},
				{ID: 13, Value: oleps.FileTime(time.Date(2024, 3, 22, 14, 5, 0, 0, time.UTC))},
				{ID: 14, Value: oleps.I4(200)},
				{ID: 15, Value: oleps.I4(0)},
				{ID: 18, Value: oleps.LPSTR("Windows Installer XML Toolset (5.0.0.0)")},
				{ID: 19, Value: oleps.I4(2)},
			},
		}},
	}
}

func BenchmarkMarshal(b *testing.B) {
	s := benchFixture()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := oleps.Marshal(s); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshal(b *testing.B) {
	data, err := oleps.Marshal(benchFixture())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := oleps.Unmarshal(data); err != nil {
			b.Fatal(err)
		}
	}
}
