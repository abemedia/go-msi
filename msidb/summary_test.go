package msidb_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/abemedia/go-msi/msidb"
)

func TestSummaryInformation(t *testing.T) {
	want := msidb.SummaryInformation{
		Codepage:            1252,
		Title:               "Installation Database",
		Subject:             "Acme Widget 1.0",
		Author:              "Acme Corp",
		Keywords:            "MSI Installer Acme",
		Comments:            "This installer is for Acme Widget.",
		Template:            "Intel;1033",
		LastSavedBy:         "Acme Build Agent",
		RevisionNumber:      "{12345678-1234-1234-1234-123456789012}",
		LastPrinted:         time.Date(2024, 3, 13, 9, 15, 0, 0, time.UTC),
		CreateTime:          time.Date(2024, 3, 14, 10, 30, 0, 0, time.UTC),
		LastSavedTime:       time.Date(2024, 3, 14, 12, 45, 30, 0, time.UTC),
		PageCount:           200,
		WordCount:           msidb.SourceCompressed | msidb.SourceShortFileNames,
		CharCount:           0x00040002,
		CreatingApplication: "go-msi tests",
		Security:            msidb.SecurityReadOnlyRecommended,
	}

	db, err := msidb.Create(filepath.Join(t.TempDir(), "summary.msi"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer db.Close()
	if err := db.SetSummaryInformation(want); err != nil {
		t.Fatalf("SummaryInformation: %v", err)
	}
	got, err := db.SummaryInformation()
	if err != nil {
		t.Fatalf("SummaryInformation: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("in-memory round-trip (-want +got):\n%s", diff)
	}
}
