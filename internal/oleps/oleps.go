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

	"github.com/abemedia/go-msi/internal/guid"
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
