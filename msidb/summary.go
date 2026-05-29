package msidb

import (
	"fmt"
	"io"
	"time"

	"github.com/abemedia/go-cfb/oleps"
	"github.com/abemedia/go-msi/internal/guid"
)

// summaryStreamName is the stream name of the summary-information
// property set.
const summaryStreamName = "\x05SummaryInformation"

// fmtidSummaryInformation is the FMTID of the summary-information
// property set.
var fmtidSummaryInformation = guid.MustParse("F29F85E0-4FF9-1068-AB91-08002B27B3D9")

// defaultSystemIdentifier is the OSKind/OSVersion value written into the
// property-set wrapper: Win32 (0x0002) and Windows NT 10 (0x000A).
const defaultSystemIdentifier = 0x0002000A

// SummaryInformation is the summary-information property set of a
// Windows Installer database.
type SummaryInformation struct {
	// Codepage is the numeric value of the ANSI code page used to display
	// the summary information.
	Codepage uint16

	// Title describes the file. The recommended phrase identifies the
	// package kind: "Installation Database", "Transform", or "Patch".
	Title string

	// Subject is the product name.
	//
	// For installation packages and transforms, Subject is the name of the
	// product installed by the package.
	//
	// For patches, Subject is a description of the patch that includes the
	// product name.
	Subject string

	// Author is the name of the manufacturer of the package.
	Author string

	// Keywords aids file-browser searches.
	//
	// For installation packages and transforms, Keywords is a list of
	// search terms that should include "Installer".
	//
	// For patches, Keywords is a semicolon-delimited list of sources of
	// the patch.
	Keywords string

	// Comments is a free-form description of the package.
	Comments string

	// Template lists the platforms and languages compatible with the
	// package.
	//
	// For installation packages, Template has the form
	// "Platform;Language[,Language...]".
	//
	// For transforms, Template has the same form but allows only one
	// language; it may be empty to indicate no restrictions.
	//
	// For patches, Template is a semicolon-delimited list of product codes
	// that can accept the patch.
	Template string

	// LastSavedBy varies by package kind.
	//
	// For installation packages, LastSavedBy records the last user to
	// modify the database; the installer sets it to the LogonUser property
	// during an administrative installation.
	//
	// For transforms, LastSavedBy is the platform and language IDs that
	// the target database has after the transform is applied.
	//
	// For patches, LastSavedBy is a semicolon-delimited list of transform
	// substorage names in the order the patch applies them.
	LastSavedBy string

	// RevisionNumber holds GUIDs identifying the package.
	//
	// For installation packages, RevisionNumber is the package code.
	//
	// For transforms, it is the original and new product codes with
	// versions plus the upgrade code, semicolon-separated.
	//
	// For patches, it is the patch code, optionally followed by codes of
	// patches that this patch obsoletes.
	RevisionNumber string

	// LastPrinted records when an administrative image was created.
	// Unused in transforms and patches.
	LastPrinted time.Time

	// CreateTime is the time and date the package was created.
	CreateTime time.Time

	// LastSavedTime is the system time and date at which the package was
	// last saved.
	LastSavedTime time.Time

	// PageCount identifies the minimum installer version required by the
	// package, encoded as major*100 + minor (e.g. 200 for 2.0). Unused in
	// patches.
	PageCount int32

	// WordCount varies by package kind.
	//
	// For installation packages, WordCount is a bit field describing the
	// source file image; see the Source* constants.
	//
	// For patches, WordCount identifies the minimum installer version
	// required to install the patch: 4 for installer 3.0+, 3 for 2.0+,
	// 2 for 1.2+.
	//
	// Unused in transforms.
	WordCount int32

	// CharCount is split into two 16-bit words for transforms: the upper
	// word holds transform validation flags, the lower word holds error
	// condition flags. Unused in installation packages and patches.
	CharCount int32

	// CreatingApplication is the name of the software used to author the
	// package.
	CreatingApplication string

	// Security is the editability constraint advertised to installer
	// tooling.
	Security Security
}

// Security is the editability constraint a database advertises to
// installer tooling.
type Security int32

// [Security] values.
const (
	SecurityNone                Security = 0
	SecurityReadOnlyRecommended Security = 2
	SecurityReadOnlyEnforced    Security = 4
)

// Source flag bits for [SummaryInformation.WordCount].
const (
	SourceShortFileNames    = 0x01 // package uses short filenames only
	SourceCompressed        = 0x02 // files stored compressed in cabinets
	SourceAdmin             = 0x04 // package is an administrative install image
	SourcePasswordProtected = 0x08
)

// Property identifiers within the summary-information property set.
const (
	pidCodepage            = 1
	pidTitle               = 2
	pidSubject             = 3
	pidAuthor              = 4
	pidKeywords            = 5
	pidComments            = 6
	pidTemplate            = 7
	pidLastSavedBy         = 8
	pidRevisionNumber      = 9
	pidLastPrinted         = 11
	pidCreateTime          = 12
	pidLastSavedTime       = 13
	pidPageCount           = 14
	pidWordCount           = 15
	pidCharCount           = 16
	pidCreatingApplication = 18
	pidSecurity            = 19
)

// SummaryInformation returns the database's summary-information property set.
func (db *Database) SummaryInformation() (SummaryInformation, error) { //nolint:funlen
	streams, err := db.Table(systemTableStreams)
	if err != nil {
		return SummaryInformation{}, err
	}
	summary, err := streams.Record(summaryStreamName)
	if err != nil {
		return SummaryInformation{}, err
	}
	data, _ := summary.Field("Data")
	pss, err := oleps.Decode(data.(io.ReadSeeker))
	if err != nil {
		return SummaryInformation{}, newError("read stream", summaryStreamName, err)
	}
	ps := pss.PropertySets[0]
	if ps.FMTID != fmtidSummaryInformation {
		return SummaryInformation{}, newError("read stream", summaryStreamName,
			fmt.Errorf("unexpected FMTID {%s}", guid.Format(ps.FMTID)))
	}

	var s SummaryInformation
	var firstErr error
	typeErr := func(p oleps.Property) error {
		return newError("read stream", summaryStreamName, fmt.Errorf("PID %d has unexpected type %T", p.ID, p.Value))
	}
	asLPSTR := func(p oleps.Property) string {
		v, ok := p.Value.(oleps.LPSTR)
		if !ok && firstErr == nil {
			firstErr = typeErr(p)
		}
		return string(v)
	}
	asI4 := func(p oleps.Property) int32 {
		v, ok := p.Value.(oleps.I4)
		if !ok && firstErr == nil {
			firstErr = typeErr(p)
		}
		return int32(v)
	}
	asFileTime := func(p oleps.Property) time.Time {
		v, ok := p.Value.(oleps.FileTime)
		if !ok && firstErr == nil {
			firstErr = typeErr(p)
		}
		return time.Time(v)
	}

	for _, p := range ps.Properties {
		switch p.ID {
		case pidCodepage:
			v, ok := p.Value.(oleps.I2)
			if !ok && firstErr == nil {
				firstErr = typeErr(p)
			}
			s.Codepage = uint16(v)
		case pidTitle:
			s.Title = asLPSTR(p)
		case pidSubject:
			s.Subject = asLPSTR(p)
		case pidAuthor:
			s.Author = asLPSTR(p)
		case pidKeywords:
			s.Keywords = asLPSTR(p)
		case pidComments:
			s.Comments = asLPSTR(p)
		case pidTemplate:
			s.Template = asLPSTR(p)
		case pidLastSavedBy:
			s.LastSavedBy = asLPSTR(p)
		case pidRevisionNumber:
			s.RevisionNumber = asLPSTR(p)
		case pidLastPrinted:
			s.LastPrinted = asFileTime(p)
		case pidCreateTime:
			s.CreateTime = asFileTime(p)
		case pidLastSavedTime:
			s.LastSavedTime = asFileTime(p)
		case pidPageCount:
			s.PageCount = asI4(p)
		case pidWordCount:
			s.WordCount = asI4(p)
		case pidCharCount:
			s.CharCount = asI4(p)
		case pidCreatingApplication:
			s.CreatingApplication = asLPSTR(p)
		case pidSecurity:
			s.Security = Security(asI4(p))
		}
	}
	if firstErr != nil {
		return SummaryInformation{}, firstErr
	}
	return s, nil
}

// SetSummaryInformation replaces the database's summary-information property set.
func (db *Database) SetSummaryInformation(s SummaryInformation) error {
	properties := make([]oleps.Property, 0, 17)
	add := func(present bool, pid uint32, v oleps.Value) {
		if present {
			properties = append(properties, oleps.Property{ID: pid, Value: v})
		}
	}
	add(true, pidCodepage, oleps.I2(s.Codepage))
	add(s.Title != "", pidTitle, oleps.LPSTR(s.Title))
	add(s.Subject != "", pidSubject, oleps.LPSTR(s.Subject))
	add(s.Author != "", pidAuthor, oleps.LPSTR(s.Author))
	add(s.Keywords != "", pidKeywords, oleps.LPSTR(s.Keywords))
	add(s.Comments != "", pidComments, oleps.LPSTR(s.Comments))
	add(s.Template != "", pidTemplate, oleps.LPSTR(s.Template))
	add(s.LastSavedBy != "", pidLastSavedBy, oleps.LPSTR(s.LastSavedBy))
	add(s.RevisionNumber != "", pidRevisionNumber, oleps.LPSTR(s.RevisionNumber))
	add(!s.LastPrinted.IsZero(), pidLastPrinted, oleps.FileTime(s.LastPrinted))
	add(!s.CreateTime.IsZero(), pidCreateTime, oleps.FileTime(s.CreateTime))
	add(!s.LastSavedTime.IsZero(), pidLastSavedTime, oleps.FileTime(s.LastSavedTime))
	add(s.PageCount != 0, pidPageCount, oleps.I4(s.PageCount))
	add(s.WordCount != 0, pidWordCount, oleps.I4(s.WordCount))
	add(s.CharCount != 0, pidCharCount, oleps.I4(s.CharCount))
	add(s.CreatingApplication != "", pidCreatingApplication, oleps.LPSTR(s.CreatingApplication))
	add(s.Security != 0, pidSecurity, oleps.I4(s.Security))

	data, err := oleps.Marshal(oleps.PropertySetStream{
		SystemIdentifier: defaultSystemIdentifier,
		PropertySets: []oleps.PropertySet{{
			FMTID:      fmtidSummaryInformation,
			Properties: properties,
		}},
	})
	if err != nil {
		return newError("write stream", summaryStreamName, err)
	}

	streams, err := db.Table(systemTableStreams)
	if err != nil {
		return err
	}
	if record, err := streams.Record(summaryStreamName); err == nil {
		return record.Set("Data", data)
	}
	_, err = streams.Insert(summaryStreamName, data)
	return err
}
