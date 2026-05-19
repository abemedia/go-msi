//go:build amd64 || arm64

// Package structuredstorage wraps Windows' IStorage / IStream / IPropertySetStorage
// COM API from ole32.dll.
package structuredstorage

import (
	"fmt"
	"io"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// Version selects v3 (512-byte) or v4 (4096-byte) sectors when creating.
type Version int

// Compound-file format versions.
const (
	V3 Version = iota
	V4
)

const (
	stgmRead           = 0x00000000
	stgmReadWrite      = 0x00000002
	stgmShareExcl      = 0x00000010
	stgmShareDenyWrite = 0x00000020
	stgmCreate         = 0x00001000
	stgmDirect         = 0x00000000

	stgfmtStorage = 0
	stgfmtDocfile = 5

	propsetflagAnsi = 0x2

	prspecPropID    = 1 // PROPSPEC.ulKind
	propidNameFirst = 2 // WriteMultiple's first usable PROPID
)

// Supported PROPVARIANT type tags.
const (
	vtI2       = 0x02
	vtI4       = 0x03
	vtUI4      = 0x13
	vtLPSTR    = 0x1E
	vtFiletime = 0x40
)

var (
	ole32                  = syscall.NewLazyDLL("ole32.dll")
	procStgCreateStorageEx = ole32.NewProc("StgCreateStorageEx")
	procStgOpenStorageEx   = ole32.NewProc("StgOpenStorageEx")
	procCoInitialize       = ole32.NewProc("CoInitialize")

	iidIStorage = syscall.GUID{
		Data1: 0x0000000B,
		Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
	}
	iidIPropertySetStorage = syscall.GUID{
		Data1: 0x0000013A,
		Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
	}
)

func init() {
	_, _, _ = procCoInitialize.Call(0)
}

// stgOptions mirrors the STGOPTIONS struct from objbase.h. Pass to
// StgCreateStorageEx to select v4 (4 KiB sectors).
type stgOptions struct {
	usVersion        uint16
	reserved         uint16
	ulSectorSize     uint32
	pwcsTemplateFile *uint16
}

// propspec mirrors the PROPSPEC struct from propidl.h.
type propspec struct {
	ulKind uint32
	_      uint32 // pad: union is 8-byte aligned on amd64
	propid uint32 // union arm (PRSPEC_PROPID); lpwstr arm unused
	_      uint32 // upper half of the 8-byte union
}

// propvariant mirrors the PROPVARIANT struct from propidl.h.
type propvariant struct {
	vt  uint16
	_   [3]uint16 // wReserved1..3
	val [2]uint64 // union (only scalar arms used)
}

type iStorageVtbl struct {
	queryInterface uintptr
	_              uintptr // addRef
	release        uintptr
	_              uintptr // createStream
	openStream     uintptr
	_              uintptr // createStorage
	_              uintptr // openStorage
	_              uintptr // copyTo
	_              uintptr // moveElementTo
	commit         uintptr
}

// Storage wraps an IStorage* COM pointer.
type Storage struct {
	vtbl *iStorageVtbl
}

type iStreamVtbl struct {
	_       uintptr // queryInterface
	_       uintptr // addRef
	release uintptr
	read    uintptr
}

// Stream wraps an IStream* COM pointer.
type Stream struct {
	vtbl *iStreamVtbl
}

type iPropertySetStorageVtbl struct {
	_       uintptr // queryInterface
	_       uintptr // addRef
	release uintptr
	create  uintptr
}

// PropertySetStorage wraps an IPropertySetStorage* COM pointer.
type PropertySetStorage struct {
	vtbl *iPropertySetStorageVtbl
}

type iPropertyStorageVtbl struct {
	_             uintptr // queryInterface
	_             uintptr // addRef
	release       uintptr
	_             uintptr // readMultiple
	writeMultiple uintptr
	_             uintptr // deleteMultiple
	_             uintptr // readPropertyNames
	_             uintptr // writePropertyNames
	_             uintptr // deletePropNames
	commit        uintptr
}

// PropertyStorage wraps an IPropertyStorage* COM pointer.
type PropertyStorage struct {
	vtbl *iPropertyStorageVtbl
}

// Prop is one (PROPID, value) pair for WriteMultiple.
type Prop struct {
	id  uint32
	pv  propvariant
	pin any // keeps an LPSTR buffer alive across the call
}

// Create creates a new compound file at path with the given version.
// Existing files are overwritten.
func Create(path string, v Version) (*Storage, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	mode := uint32(stgmReadWrite | stgmShareExcl | stgmCreate | stgmDirect)
	var opts *stgOptions
	switch v {
	case V3:
	case V4:
		opts = &stgOptions{
			usVersion:    1,
			ulSectorSize: 4096,
		}
	default:
		return nil, fmt.Errorf("structuredstorage: unsupported version %d", v)
	}
	var stg *Storage
	r, _, _ := procStgCreateStorageEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(mode),
		uintptr(stgfmtDocfile),
		0, // grfAttrs: reserved, must be 0
		uintptr(unsafe.Pointer(opts)),
		0, // pSecurityDescriptor: NULL
		uintptr(unsafe.Pointer(&iidIStorage)),
		uintptr(unsafe.Pointer(&stg)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return stg, nil
}

// Open opens an existing compound file at path read-only.
func Open(path string) (*Storage, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	mode := uint32(stgmRead | stgmShareDenyWrite)
	var stg *Storage
	r, _, _ := procStgOpenStorageEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(mode),
		uintptr(stgfmtStorage),
		0, // grfAttrs: reserved, must be 0
		0, // pStgOptions: NULL (use defaults)
		0, // pSecurityDescriptor: NULL
		uintptr(unsafe.Pointer(&iidIStorage)),
		uintptr(unsafe.Pointer(&stg)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return stg, nil
}

// Close releases the storage.
func (s *Storage) Close() {
	_, _, _ = syscall.SyscallN(s.vtbl.release, uintptr(unsafe.Pointer(s)))
}

// Commit flushes buffered changes.
func (s *Storage) Commit() error {
	r, _, _ := syscall.SyscallN(s.vtbl.commit,
		uintptr(unsafe.Pointer(s)),
		0, // grfCommitFlags: STGC_DEFAULT
	)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// OpenStream opens an existing stream child named name read-only.
func (s *Storage) OpenStream(name string) (*Stream, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	mode := uint32(stgmRead | stgmShareExcl)
	var stm *Stream
	r, _, _ := syscall.SyscallN(s.vtbl.openStream,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(namePtr)),
		0, // reserved1: must be NULL
		uintptr(mode),
		0, // reserved2: must be 0
		uintptr(unsafe.Pointer(&stm)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return stm, nil
}

// PropertySetStorage queries the root storage for its IPropertySetStorage.
func (s *Storage) PropertySetStorage() (*PropertySetStorage, error) {
	var pss *PropertySetStorage
	r, _, _ := syscall.SyscallN(s.vtbl.queryInterface,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(&iidIPropertySetStorage)),
		uintptr(unsafe.Pointer(&pss)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return pss, nil
}

// Close releases the stream.
func (s *Stream) Close() {
	_, _, _ = syscall.SyscallN(s.vtbl.release, uintptr(unsafe.Pointer(s)))
}

// Read implements [io.Reader].
func (s *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var n uint32
	r, _, _ := syscall.SyscallN(s.vtbl.read,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(unsafe.SliceData(p))),
		uintptr(len(p)),
		uintptr(unsafe.Pointer(&n)),
	)
	if r != 0 && r != 1 { // 1 = S_FALSE: short read at EOF
		return int(n), syscall.Errno(r)
	}
	if n == 0 {
		return 0, io.EOF
	}
	return int(n), nil
}

// Close releases the property set storage.
func (p *PropertySetStorage) Close() {
	_, _, _ = syscall.SyscallN(p.vtbl.release, uintptr(unsafe.Pointer(p)))
}

// Create creates a property set with fmtid and clsid.
func (p *PropertySetStorage) Create(fmtid, clsid [16]byte) (*PropertyStorage, error) {
	var ps *PropertyStorage
	r, _, _ := syscall.SyscallN(p.vtbl.create,
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&fmtid)),
		uintptr(unsafe.Pointer(&clsid)),
		uintptr(propsetflagAnsi),
		uintptr(stgmCreate|stgmReadWrite|stgmShareExcl),
		uintptr(unsafe.Pointer(&ps)),
	)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return ps, nil
}

// Close releases the property storage.
func (p *PropertyStorage) Close() {
	_, _, _ = syscall.SyscallN(p.vtbl.release, uintptr(unsafe.Pointer(p)))
}

// Commit flushes the property storage.
func (p *PropertyStorage) Commit() error {
	r, _, _ := syscall.SyscallN(p.vtbl.commit,
		uintptr(unsafe.Pointer(p)),
		0, // STGC_DEFAULT
	)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// WriteMultiple writes props.
func (p *PropertyStorage) WriteMultiple(props []Prop) error {
	if len(props) == 0 {
		return nil
	}
	specs := make([]propspec, len(props))
	vars := make([]propvariant, len(props))
	for i, pr := range props {
		specs[i] = propspec{ulKind: prspecPropID, propid: pr.id}
		vars[i] = pr.pv
	}
	r, _, _ := syscall.SyscallN(p.vtbl.writeMultiple,
		uintptr(unsafe.Pointer(p)),
		uintptr(len(props)),
		uintptr(unsafe.Pointer(&specs[0])),
		uintptr(unsafe.Pointer(&vars[0])),
		uintptr(propidNameFirst),
	)
	runtime.KeepAlive(props)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// PropI2 builds a VT_I2 property.
func PropI2(id uint32, v int16) Prop {
	p := Prop{id: id, pv: propvariant{vt: vtI2}}
	*(*int16)(unsafe.Pointer(&p.pv.val[0])) = v
	return p
}

// PropI4 builds a VT_I4 property.
func PropI4(id uint32, v int32) Prop {
	p := Prop{id: id, pv: propvariant{vt: vtI4}}
	*(*int32)(unsafe.Pointer(&p.pv.val[0])) = v
	return p
}

// PropUI4 builds a VT_UI4 property.
func PropUI4(id, v uint32) Prop {
	p := Prop{id: id, pv: propvariant{vt: vtUI4}}
	*(*uint32)(unsafe.Pointer(&p.pv.val[0])) = v
	return p
}

// PropFiletime builds a VT_FILETIME property from t. A zero t produces
// FILETIME{0,0}.
func PropFiletime(id uint32, t time.Time) Prop {
	p := Prop{id: id, pv: propvariant{vt: vtFiletime}}
	if !t.IsZero() {
		ft := syscall.NsecToFiletime(t.UnixNano())
		p.pv.val[0] = uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
	}
	return p
}

// PropLPSTR builds a VT_LPSTR property. s must already be code-page bytes,
// not UTF-8.
func PropLPSTR(id uint32, s string) Prop {
	cstr := append([]byte(s), 0)
	p := Prop{id: id, pin: cstr, pv: propvariant{vt: vtLPSTR}}
	p.pv.val[0] = uint64(uintptr(unsafe.Pointer(&cstr[0])))
	return p
}
