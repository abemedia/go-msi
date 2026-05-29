//go:build windows

// Package msiquery wraps the Windows Installer database API (msi.dll).
package msiquery

import (
	"syscall"
	"unsafe"
)

// Persist controls how [OpenDatabase] opens the file.
type Persist uintptr

// [Persist] values, matching the MSIDBOPEN_* macros in msiquery.h.
const (
	ReadOnly     Persist = 0 // open existing, read-only
	Transact     Persist = 1 // open existing, changes buffered until Commit
	Direct       Persist = 2 // open existing, changes written immediately
	Create       Persist = 3 // create new, buffered
	CreateDirect Persist = 4 // create new, immediate
)

// Modify controls how [View.Modify] alters the current row.
type Modify int32

// [Modify] values, matching the MSIMODIFY_* macros in msiquery.h.
const (
	ModifySeek    Modify = -1 // refresh by primary key from record
	ModifyRefresh Modify = 0  // refresh record from current row
	ModifyInsert  Modify = 1  // insert; fails if PK already exists
	ModifyUpdate  Modify = 2  // update the current fetched row
	ModifyAssign  Modify = 3  // insert or update existing (overwrite)
	ModifyReplace Modify = 4  // update; delete+insert if PK changed
	ModifyMerge   Modify = 5  // insert or validate equality with existing
	ModifyDelete  Modify = 6  // delete the current fetched row
)

// ColumnInfo selects what [View.ColumnInfo] reports about each column.
type ColumnInfo uint32

// [ColumnInfo] values, matching the MSICOLINFO_* macros in msiquery.h.
const (
	ColumnNames ColumnInfo = 0 // column names
	ColumnTypes ColumnInfo = 1 // column type strings
)

// NullInteger is what [Record.GetInteger] returns for a null or
// non-integer field (MSI_NULL_INTEGER).
const NullInteger int32 = -2147483648

// Windows status codes returned by the iteration APIs.
const (
	errorMoreData    = 234 // ERROR_MORE_DATA: buffer too small; required size returned
	errorNoMoreItems = 259 // ERROR_NO_MORE_ITEMS: view exhausted
)

var (
	msi = syscall.NewLazyDLL("msi.dll")

	procOpenDatabase           = msi.NewProc("MsiOpenDatabaseW")
	procCloseHandle            = msi.NewProc("MsiCloseHandle")
	procDatabaseCommit         = msi.NewProc("MsiDatabaseCommit")
	procDatabaseOpenView       = msi.NewProc("MsiDatabaseOpenViewW")
	procDatabaseGetPrimaryKeys = msi.NewProc("MsiDatabaseGetPrimaryKeysW")
	procDatabaseExport         = msi.NewProc("MsiDatabaseExportW")
	procViewExecute            = msi.NewProc("MsiViewExecute")
	procViewModify             = msi.NewProc("MsiViewModify")
	procViewFetch              = msi.NewProc("MsiViewFetch")
	procViewGetColumnInfo      = msi.NewProc("MsiViewGetColumnInfo")
	procCreateRecord           = msi.NewProc("MsiCreateRecord")
	procRecordSetString        = msi.NewProc("MsiRecordSetStringW")
	procRecordSetStream        = msi.NewProc("MsiRecordSetStreamW")
	procRecordGetFieldCount    = msi.NewProc("MsiRecordGetFieldCount")
	procRecordGetString        = msi.NewProc("MsiRecordGetStringW")
	procRecordGetInteger       = msi.NewProc("MsiRecordGetInteger")
	procRecordIsNull           = msi.NewProc("MsiRecordIsNull")
	procRecordDataSize         = msi.NewProc("MsiRecordDataSize")
	procRecordReadStream       = msi.NewProc("MsiRecordReadStream")
)

// Database is an MSI database handle (MSIHANDLE).
type Database uintptr

// OpenDatabase opens the MSI at path with the given persistence mode.
func OpenDatabase(path string, persist Persist) (Database, error) {
	pathW, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var h Database
	ret, _, _ := procOpenDatabase.Call(
		uintptr(unsafe.Pointer(pathW)),
		uintptr(persist),
		uintptr(unsafe.Pointer(&h)),
	)
	if ret != 0 {
		return 0, syscall.Errno(ret)
	}
	return h, nil
}

// Close releases the database handle.
func (d Database) Close() error {
	ret, _, _ := procCloseHandle.Call(uintptr(d))
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// Commit persists pending changes to the on-disk database.
func (d Database) Commit() error {
	ret, _, _ := procDatabaseCommit.Call(uintptr(d))
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// OpenView prepares an SQL view against the database.
func (d Database) OpenView(sql string) (View, error) {
	sqlW, err := syscall.UTF16PtrFromString(sql)
	if err != nil {
		return 0, err
	}
	var h View
	ret, _, _ := procDatabaseOpenView.Call(
		uintptr(d),
		uintptr(unsafe.Pointer(sqlW)),
		uintptr(unsafe.Pointer(&h)),
	)
	if ret != 0 {
		return 0, syscall.Errno(ret)
	}
	return h, nil
}

// PrimaryKeys returns a record whose fields are the primary-key column
// names of table, in key order.
func (d Database) PrimaryKeys(table string) (Record, error) {
	tableW, err := syscall.UTF16PtrFromString(table)
	if err != nil {
		return 0, err
	}
	var h Record
	ret, _, _ := procDatabaseGetPrimaryKeys.Call(
		uintptr(d),
		uintptr(unsafe.Pointer(tableW)),
		uintptr(unsafe.Pointer(&h)),
	)
	if ret != 0 {
		return 0, syscall.Errno(ret)
	}
	return h, nil
}

// Export writes table to an archive file named file in folder.
func (d Database) Export(table, folder, file string) error {
	tableW, err := syscall.UTF16PtrFromString(table)
	if err != nil {
		return err
	}
	folderW, err := syscall.UTF16PtrFromString(folder)
	if err != nil {
		return err
	}
	fileW, err := syscall.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	ret, _, _ := procDatabaseExport.Call(
		uintptr(d),
		uintptr(unsafe.Pointer(tableW)),
		uintptr(unsafe.Pointer(folderW)),
		uintptr(unsafe.Pointer(fileW)),
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// View is an MSI view handle (MSIHANDLE).
type View uintptr

// Execute runs the view, optionally binding parameters from rec. Pass
// 0 for parameterless queries.
func (v View) Execute(rec Record) error {
	ret, _, _ := procViewExecute.Call(uintptr(v), uintptr(rec))
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// Modify mutates the view's current row according to mode.
func (v View) Modify(mode Modify, rec Record) error {
	ret, _, _ := procViewModify.Call(uintptr(v), uintptr(mode), uintptr(rec))
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// Fetch returns the next record from an executed view, or (0, nil) once
// the view is exhausted.
func (v View) Fetch() (Record, error) {
	var h Record
	ret, _, _ := procViewFetch.Call(uintptr(v), uintptr(unsafe.Pointer(&h)))
	if ret == errorNoMoreItems {
		return 0, nil //nolint:nilnil
	}
	if ret != 0 {
		return 0, syscall.Errno(ret)
	}
	return h, nil
}

// ColumnInfo returns a record of the view's column names or type strings.
func (v View) ColumnInfo(kind ColumnInfo) (Record, error) {
	var h Record
	ret, _, _ := procViewGetColumnInfo.Call(
		uintptr(v),
		uintptr(kind),
		uintptr(unsafe.Pointer(&h)),
	)
	if ret != 0 {
		return 0, syscall.Errno(ret)
	}
	return h, nil
}

// Close releases the view handle.
func (v View) Close() error {
	ret, _, _ := procCloseHandle.Call(uintptr(v))
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// Record is an MSI record handle (MSIHANDLE). Fields are 1-indexed.
type Record uintptr

// CreateRecord allocates a record with the given number of fields.
func CreateRecord(fields uint32) (Record, error) {
	ret, _, err := procCreateRecord.Call(uintptr(fields))
	if ret == 0 {
		return 0, err
	}
	return Record(ret), nil
}

// SetString stores value in field.
func (r Record) SetString(field uint32, value string) error {
	valW, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		return err
	}
	ret, _, _ := procRecordSetString.Call(
		uintptr(r),
		uintptr(field),
		uintptr(unsafe.Pointer(valW)),
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// SetStream binds field to the contents of the file at path.
func (r Record) SetStream(field uint32, path string) error {
	pathW, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	ret, _, _ := procRecordSetStream.Call(
		uintptr(r),
		uintptr(field),
		uintptr(unsafe.Pointer(pathW)),
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// FieldCount returns the number of fields in the record.
func (r Record) FieldCount() uint32 {
	ret, _, _ := procRecordGetFieldCount.Call(uintptr(r))
	return uint32(ret)
}

// IsNull reports whether field holds a null value.
func (r Record) IsNull(field uint32) bool {
	ret, _, _ := procRecordIsNull.Call(uintptr(r), uintptr(field))
	return ret != 0
}

// GetInteger returns field as an integer, or [NullInteger] if field is
// null or not an integer.
func (r Record) GetInteger(field uint32) int32 {
	ret, _, _ := procRecordGetInteger.Call(uintptr(r), uintptr(field))
	return int32(ret)
}

// GetString returns field as a string. A null field returns "".
func (r Record) GetString(field uint32) (string, error) {
	var scratch uint16
	n := uint32(0)
	ret, _, _ := procRecordGetString.Call(
		uintptr(r),
		uintptr(field),
		uintptr(unsafe.Pointer(&scratch)),
		uintptr(unsafe.Pointer(&n)),
	)
	if ret != 0 && ret != errorMoreData {
		return "", syscall.Errno(ret)
	}
	if n == 0 {
		return "", nil
	}
	buf := make([]uint16, n+1)
	n = uint32(len(buf))
	ret, _, _ = procRecordGetString.Call(
		uintptr(r),
		uintptr(field),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&n)),
	)
	if ret != 0 {
		return "", syscall.Errno(ret)
	}
	return syscall.UTF16ToString(buf[:n]), nil
}

// DataSize returns the byte length of field's stream data.
func (r Record) DataSize(field uint32) uint32 {
	ret, _, _ := procRecordDataSize.Call(uintptr(r), uintptr(field))
	return uint32(ret)
}

// ReadStream copies the next bytes of field's stream into buf and returns
// the number copied, continuing from the previous read. A return of 0 with
// a nil error means end of stream.
func (r Record) ReadStream(field uint32, buf []byte) (int, error) {
	n := uint32(len(buf))
	var p *byte
	if len(buf) > 0 {
		p = &buf[0]
	}
	ret, _, _ := procRecordReadStream.Call(
		uintptr(r),
		uintptr(field),
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&n)),
	)
	if ret != 0 {
		return 0, syscall.Errno(ret)
	}
	return int(n), nil
}

// Close releases the record handle.
func (r Record) Close() error {
	ret, _, _ := procCloseHandle.Call(uintptr(r))
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}
