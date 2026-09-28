package stringpool_test

import (
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/abemedia/go-msi/internal/stringpool"
)

func TestDecodeEmptyLongStringIsFree(t *testing.T) {
	p, err := stringpool.New(1252)
	if err != nil {
		t.Fatal(err)
	}
	p.Intern("AB", true)
	pool, data, err := stringpool.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	entry := binary.LittleEndian.AppendUint16(nil, 0)  // length
	entry = binary.LittleEndian.AppendUint16(entry, 1) // refs
	entry = binary.LittleEndian.AppendUint32(entry, 0) // long-form length
	pool = slices.Insert(pool, 4, entry...)

	p, err = stringpool.Decode(pool, data)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := p.Lookup(1); ok {
		t.Errorf("Lookup(1) = (%q, true), want free", s)
	}
	if s, ok := p.Lookup(2); s != "AB" || !ok {
		t.Errorf(`Lookup(2) = (%q, %v), want ("AB", true)`, s, ok)
	}
}

func TestDecodeErrors(t *testing.T) {
	p, err := stringpool.New(1252)
	if err != nil {
		t.Fatal(err)
	}
	p.Intern("AB", true)
	pool, data, err := stringpool.Encode(p)
	if err != nil {
		t.Fatal(err)
	}

	badCP := slices.Clone(pool)
	binary.LittleEndian.PutUint16(badCP, 437) // 437 is not in our codepage table

	tests := []struct {
		name    string
		pool    []byte
		data    []byte
		want    error
		wantMsg string
	}{
		{"pool shorter than header", pool[:3], nil, io.ErrUnexpectedEOF, ""},
		{"misaligned pool length", pool[:5], nil, stringpool.ErrFormat, "not a multiple of 4"},
		{"unsupported codepage", badCP, data, stringpool.ErrUnsupportedCodePage, "code page 437"},
		{"data shorter than declared", pool, data[:1], io.ErrUnexpectedEOF, ""},
		{"trailing data bytes", pool, slices.Concat(data, []byte{1, 2}), stringpool.ErrFormat, "2 trailing bytes"},
		{"string stored twice", slices.Concat(pool, pool[4:]), slices.Concat(data, data), stringpool.ErrFormat, `string "AB" stored twice`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := stringpool.Decode(test.pool, test.data)
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if !strings.Contains(err.Error(), test.wantMsg) {
				t.Errorf("err = %q, want containing %q", err, test.wantMsg)
			}
		})
	}
}
