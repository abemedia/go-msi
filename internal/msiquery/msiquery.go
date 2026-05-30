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

var (
	msi = syscall.NewLazyDLL("msi.dll")

	procOpenDatabase     = msi.NewProc("MsiOpenDatabaseW")
	procCloseHandle      = msi.NewProc("MsiCloseHandle")
	procDatabaseCommit   = msi.NewProc("MsiDatabaseCommit")
	procDatabaseOpenView = msi.NewProc("MsiDatabaseOpenViewW")
	procViewExecute      = msi.NewProc("MsiViewExecute")
	procViewModify       = msi.NewProc("MsiViewModify")
	procCreateRecord     = msi.NewProc("MsiCreateRecord")
	procRecordSetString  = msi.NewProc("MsiRecordSetStringW")
	procRecordSetStream  = msi.NewProc("MsiRecordSetStreamW")
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
func CreateRecord(fields uint32) Record {
	ret, _, _ := procCreateRecord.Call(uintptr(fields))
	return Record(ret)
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

// Close releases the record handle.
func (r Record) Close() error {
	ret, _, _ := procCloseHandle.Call(uintptr(r))
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}
