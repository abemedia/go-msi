// Package oleps reads and writes OLE property sets, the format used by the
// summary information of MSI files and other COM Structured Storage
// documents such as .doc, .xls and .msg.
//
// The format is specified in [MS-OLEPS]: "Object Linking and Embedding (OLE)
// Property Set Data Structures"
//
// [MS-OLEPS]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-oleps/
package oleps

import (
	"errors"
	"fmt"

	"github.com/abemedia/go-msi/internal/guid"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

const (
	cpWinUnicode uint16 = 0x04B0 // CP1200 (UTF-16LE)
	byteOrder    uint16 = 0xFFFE // fixed sentinel, not a BOM
)

// Well-known format identifiers for [PropertySet.FMTID].
var (
	FMTIDSummaryInformation    = guid.MustParse("F29F85E0-4FF9-1068-AB91-08002B27B3D9")
	FMTIDDocSummaryInformation = guid.MustParse("D5CDD502-2E9C-101B-9397-08002B2CF9AE")
	FMTIDUserDefinedProperties = guid.MustParse("D5CDD505-2E9C-101B-9397-08002B2CF9AE")
)

// ErrUnsupported is returned when a property set stream uses a feature this
// package does not implement.
var ErrUnsupported = errors.New("oleps: unsupported feature")

// PropertySetStream is a property set stream containing one or two
// property sets.
type PropertySetStream struct {
	// CLSID is the class identifier associated with the property set,
	// typically identifying the application that created it.
	CLSID [16]byte

	// Version is the property set version (0 or 1).
	Version uint16

	// SystemIdentifier is an implementation-specific value; readers
	// should ignore it.
	SystemIdentifier uint32

	// PropertySets are the property sets in the stream.
	PropertySets []PropertySet
}

// PropertySet is a set of properties identified by an FMTID.
type PropertySet struct {
	// FMTID identifies the property set format, e.g. [FMTIDSummaryInformation].
	FMTID [16]byte

	// Properties are the properties in the set.
	Properties []Property
}

// Property is a typed value associated with a property identifier.
type Property struct {
	// ID is the property identifier (PID).
	ID uint32

	// Value is the typed property value.
	Value Value
}

// Value is a typed property value.
type Value interface{ propertyType() uint16 }

// resolveEncoding maps a Windows code page to its text encoding.
// Returns [ErrUnsupported] for an unknown code page.
func resolveEncoding(cp uint16) (encoding.Encoding, error) {
	switch cp {
	case cpWinUnicode:
		return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM), nil
	case 65001:
		return unicode.UTF8, nil
	case 1250:
		return charmap.Windows1250, nil
	case 1251:
		return charmap.Windows1251, nil
	case 1252:
		return charmap.Windows1252, nil
	case 1253:
		return charmap.Windows1253, nil
	case 1254:
		return charmap.Windows1254, nil
	case 1255:
		return charmap.Windows1255, nil
	case 1256:
		return charmap.Windows1256, nil
	case 1257:
		return charmap.Windows1257, nil
	case 1258:
		return charmap.Windows1258, nil
	case 874:
		return charmap.Windows874, nil
	case 932:
		return japanese.ShiftJIS, nil
	case 936:
		return simplifiedchinese.GBK, nil
	case 949:
		return korean.EUCKR, nil
	case 950:
		return traditionalchinese.Big5, nil
	case 10000:
		return charmap.Macintosh, nil
	case 28591:
		return charmap.ISO8859_1, nil
	case 28592:
		return charmap.ISO8859_2, nil
	case 28593:
		return charmap.ISO8859_3, nil
	case 28594:
		return charmap.ISO8859_4, nil
	case 28595:
		return charmap.ISO8859_5, nil
	case 28596:
		return charmap.ISO8859_6, nil
	case 28597:
		return charmap.ISO8859_7, nil
	case 28598:
		return charmap.ISO8859_8, nil
	case 28599:
		return charmap.ISO8859_9, nil
	case 28603:
		return charmap.ISO8859_13, nil
	case 28605:
		return charmap.ISO8859_15, nil
	}
	return nil, fmt.Errorf("%w: code page %d", ErrUnsupported, cp)
}
