//go:build windows

// Command generate writes each entry in fixtures.json as a Windows COM
// property set and saves the raw \x05SummaryInformation bytes as
// <name>.golden.
//
// Run on Windows:
//
//	go run ./internal/oleps/testdata/generate.go
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/abemedia/go-msi/internal/oleps"
	"github.com/abemedia/go-msi/internal/oleps/internal/olepstest"
	"github.com/abemedia/go-msi/internal/structuredstorage"
)

// propidLocale is the OLEPS reserved PID for the locale indicator. COM
// auto-injects this property at create time, so we never write it ourselves.
const propidLocale = 0x80000000

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
}

func run() error {
	dir, err := sourceDir()
	if err != nil {
		return err
	}

	fixtures, err := olepstest.LoadFixtures(filepath.Join(dir, "fixtures.json"))
	if err != nil {
		return err
	}

	// Clean up old .golden files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".golden") {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}

	for name, want := range fixtures {
		if err := build(dir, name, want); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// sourceDir returns this file's directory.
func sourceDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("could not determine source file location")
	}
	return filepath.Dir(file), nil
}

// build writes want via COM and saves the bytes as <name>.golden.
func build(dir, name string, want oleps.PropertySetStream) error {
	scratch, err := os.MkdirTemp("", "oleps")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	path := filepath.Join(scratch, "fixture.cfb")

	if err := writeSet(path, want); err != nil {
		return err
	}

	stg, err := structuredstorage.Open(path)
	if err != nil {
		return fmt.Errorf("Open: %w", err)
	}
	defer stg.Close()
	st, err := stg.OpenStream("\x05SummaryInformation")
	if err != nil {
		return fmt.Errorf("OpenStream: %w", err)
	}
	defer st.Close()
	raw, err := io.ReadAll(st)
	if err != nil {
		return fmt.Errorf("ReadAll: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, name+".golden"), raw, 0o644)
}

// writeSet creates the property set via COM.
func writeSet(path string, s oleps.PropertySetStream) error {
	stg, err := structuredstorage.Create(path, structuredstorage.V3)
	if err != nil {
		return fmt.Errorf("Create: %w", err)
	}
	defer stg.Close()
	pss, err := stg.PropertySetStorage()
	if err != nil {
		return fmt.Errorf("PropertySetStorage: %w", err)
	}
	defer pss.Close()
	set := s.PropertySets[0]
	ps, err := pss.Create(set.FMTID, s.CLSID)
	if err != nil {
		return fmt.Errorf("PropertySetStorage.Create: %w", err)
	}
	defer ps.Close()

	var cp uint16
	for _, p := range set.Properties {
		if p.ID == 1 {
			cp = uint16(p.Value.(oleps.I2))
			break
		}
	}

	// COM rejects PID 1 sharing a WriteMultiple call with other PIDs.
	var codepage, rest []structuredstorage.Prop
	for _, p := range set.Properties {
		if p.ID == propidLocale {
			continue
		}
		pr := toProp(cp, p.ID, p.Value)
		if p.ID == 1 {
			codepage = append(codepage, pr)
		} else {
			rest = append(rest, pr)
		}
	}
	if err := ps.WriteMultiple(codepage); err != nil {
		return fmt.Errorf("WriteMultiple(PID 1): %w", err)
	}
	if err := ps.WriteMultiple(rest); err != nil {
		return fmt.Errorf("WriteMultiple: %w", err)
	}
	if err := ps.Commit(); err != nil {
		return fmt.Errorf("PropertyStorage.Commit: %w", err)
	}
	if err := stg.Commit(); err != nil {
		return fmt.Errorf("Storage.Commit: %w", err)
	}
	return nil
}

// toProp converts v to a COM Prop, encoding LPSTR values to code page cp.
func toProp(cp uint16, id uint32, v oleps.Value) structuredstorage.Prop {
	switch t := v.(type) {
	case oleps.I2:
		return structuredstorage.PropI2(id, int16(t))
	case oleps.I4:
		return structuredstorage.PropI4(id, int32(t))
	case oleps.UI4:
		return structuredstorage.PropUI4(id, uint32(t))
	case oleps.LPSTR:
		return structuredstorage.PropLPSTR(id, string(wideCharToMultiByte(cp, string(t))))
	case oleps.FileTime:
		return structuredstorage.PropFiletime(id, time.Time(t))
	}
	panic(fmt.Sprintf("unknown value type: %T", v))
}

var procWideCharToMultiByte = syscall.NewLazyDLL("kernel32.dll").NewProc("WideCharToMultiByte")

// wideCharToMultiByte encodes s to Windows code page cp via kernel32.
func wideCharToMultiByte(cp uint16, s string) []byte {
	if s == "" {
		return nil
	}
	units := utf16.Encode([]rune(s))
	// WideCharToMultiByte upper bound: 4 bytes per UTF-16 unit (worst case).
	buf := make([]byte, len(units)*4)
	n, _, _ := procWideCharToMultiByte.Call(
		uintptr(cp),                        // CodePage
		0,                                  // dwFlags
		uintptr(unsafe.Pointer(&units[0])), // lpWideCharStr
		uintptr(len(units)),                // cchWideChar
		uintptr(unsafe.Pointer(&buf[0])),   // lpMultiByteStr
		uintptr(len(buf)),                  // cbMultiByte
		0,                                  // lpDefaultChar: NULL
		0,                                  // lpUsedDefaultChar: NULL
	)
	if n == 0 {
		panic(fmt.Sprintf("WideCharToMultiByte cp=%d s=%q failed", cp, s))
	}
	return buf[:n]
}
