package msidb_test

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type (
	myInt    int
	myInt8   int8
	myUint16 uint16
	myString string
	myBytes  []byte
)

type stringer struct{ S string }

func (s stringer) String() string { return s.S }

func (s *stringer) Scan(src any) error {
	s.S, _ = src.(string)
	return nil
}

type valuer struct{ V any }

func (v valuer) Value() (driver.Value, error) { return v.V, nil }

func (v *valuer) Scan(src any) error {
	v.V = src
	return nil
}

type failingValuer struct{}

func (failingValuer) Value() (driver.Value, error) { return nil, errors.New("valuer failed") }

func TestConvert(t *testing.T) {
	tests := []struct {
		name    string
		col     string
		val     any    // inserted, and the expected scan result unless want is set
		want    any    // expected scan result; its type is the scan destination
		wantErr string // message of the first step that fails, Exec or Scan
	}{
		{name: "int", col: "N", val: 7},
		{name: "int8", col: "N", val: int8(7)},
		{name: "int16", col: "N", val: int16(7)},
		{name: "int32", col: "N", val: int32(7)},
		{name: "int64", col: "N", val: int64(7)},
		{name: "uint", col: "N", val: uint(7)},
		{name: "uint8", col: "N", val: uint8(7)},
		{name: "uint16", col: "N", val: uint16(7)},
		{name: "uint32", col: "N", val: uint32(7)},
		{name: "uint64", col: "N", val: uint64(7)},
		{name: "negative", col: "N", val: -7},
		{name: "short minimum", col: "N", val: -32767},
		{name: "long", col: "L", val: 70000},
		{name: "long minimum", col: "L", val: -2147483647},
		{name: "pointer to int", col: "N", val: new(7)},
		{name: "pointer to int8", col: "N", val: new(int8(7))},
		{name: "pointer to int16", col: "N", val: new(int16(7))},
		{name: "pointer to int32", col: "N", val: new(int32(7))},
		{name: "pointer to int64", col: "N", val: new(int64(7))},
		{name: "pointer to uint", col: "N", val: new(uint(7))},
		{name: "pointer to uint8", col: "N", val: new(uint8(7))},
		{name: "pointer to uint16", col: "N", val: new(uint16(7))},
		{name: "pointer to uint32", col: "N", val: new(uint32(7))},
		{name: "pointer to uint64", col: "N", val: new(uint64(7))},
		{name: "nil *int", col: "N", val: (*int)(nil)},
		{name: "nil *uint16", col: "N", val: (*uint16)(nil)},
		{name: "untyped nil", col: "N", val: nil, want: (*int)(nil)},
		{name: "pointer to pointer", col: "N", val: new(new(7))},
		{name: "pointer to pointer to pointer", col: "N", val: new(new(new(7)))},
		{name: "named int", col: "N", val: myInt(7)},
		{name: "named int8", col: "N", val: myInt8(7)},
		{name: "named uint16", col: "N", val: myUint16(7)},
		{name: "pointer to named int", col: "N", val: new(myInt(7))},
		{name: "nil pointer to named int", col: "N", val: (*myInt)(nil)},
		{name: "sql.Null set", col: "N", val: sql.Null[int]{V: 7, Valid: true}},
		{name: "sql.Null unset", col: "N", val: sql.Null[int]{}},
		{name: "sql.NullInt64 set", col: "N", val: sql.NullInt64{Int64: 7, Valid: true}},
		{name: "sql.NullInt64 unset", col: "N", val: sql.NullInt64{}},
		{name: "valuer", col: "N", val: valuer{int64(7)}},
		{name: "valuer nil", col: "N", val: valuer{nil}},
		{name: "nil valuer pointer", col: "N", val: (*valuer)(nil)},
		{name: "numeric string", col: "N", val: "7"},
		{name: "pointer to numeric string", col: "N", val: new("7")},
		{name: "nil *string into int column", col: "N", val: (*string)(nil)},
		{name: "string", col: "S", val: "x"},
		{name: "named string", col: "S", val: myString("x")},
		{name: "pointer to string", col: "S", val: new("x")},
		{name: "nil *string", col: "S", val: (*string)(nil)},
		{name: "bytes into string column", col: "S", val: []byte("x")},
		{name: "named bytes into string column", col: "S", val: myBytes("x")},
		{name: "pointer to bytes", col: "S", val: new([]byte("x"))},
		{name: "sql.NullString set", col: "S", val: sql.NullString{String: "x", Valid: true}},
		{name: "sql.NullString unset", col: "S", val: sql.NullString{}},
		{name: "pointer to Scanner", col: "S", val: "x", want: &sql.NullString{String: "x", Valid: true}},
		{name: "nil pointer to Scanner", col: "S", val: nil, want: (*sql.NullString)(nil)},
		{name: "string valuer", col: "S", val: valuer{"x"}},
		{name: "stringer", col: "S", val: stringer{"x"}},
		{name: "int into string column", col: "S", val: 7},
		{name: "pointer to int into string column", col: "S", val: new(7)},
		{name: "nil *int into string column", col: "S", val: (*int)(nil)},
		{name: "bytes", col: "B", val: []byte("bin")},
		{name: "named bytes", col: "B", val: myBytes("bin")},
		{name: "reader", col: "B", val: bytes.NewReader([]byte("bin")), want: []byte("bin")},
		{name: "nil bytes", col: "B", val: []byte(nil)},
		{name: "nil named bytes", col: "B", val: myBytes(nil)},
		{name: "nil binary", col: "B", val: nil, want: []byte(nil)},
		{name: "string into binary column", col: "B", val: "bin"},
		{name: "bytes valuer", col: "B", val: valuer{[]byte("bin")}},
		{name: "string into int column", col: "N", val: "x", wantErr: `expected integer, got "x"`},
		{name: "huge numeric string", col: "N", val: "99999999999999999999", wantErr: "out of range"},
		{name: "bool is unsupported", col: "N", val: true, wantErr: "expected integer, got bool"},
		{name: "float is unsupported", col: "N", val: 1.5, wantErr: "expected integer, got float64"},
		{name: "short overflow", col: "N", val: 40000, wantErr: "value 40000 out of range"},
		{name: "short NULL encoding", col: "N", val: -32768, wantErr: "value -32768 out of range"},
		{name: "long overflow", col: "L", val: int64(1) << 40, wantErr: "out of range"},
		{name: "long NULL encoding", col: "L", val: -2147483648, wantErr: "value -2147483648 out of range"},
		{name: "valuer error", col: "N", val: failingValuer{}, wantErr: "valuer failed"},
		{name: "bool into string column", col: "S", val: true, wantErr: "expected string, got bool"},
		{name: "int into binary column", col: "B", val: 7, wantErr: "expected io.Reader"},
		{name: "scan overflow int8", col: "N", val: 300, want: int8(0), wantErr: "out of range"},
		{name: "scan negative into uint", col: "N", val: -1, want: uint(0), wantErr: "out of range"},
		{name: "scan NULL into int", col: "N", val: nil, want: 0, wantErr: "cannot scan NULL into *int"},
		{name: "scan into unsupported kind", col: "N", val: 7, want: (chan int)(nil), wantErr: "cannot scan"},
		{name: "scan string into int", col: "S", val: "x", want: 0, wantErr: `cannot scan "x" into *int`},
		{name: "scan numeric string overflow int8", col: "S", val: "300", want: int8(0), wantErr: "value 300 out of range for *int8"},
		{name: "scan NULL string into int", col: "S", val: nil, want: 0, wantErr: "cannot scan NULL into *int"},
	}

	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `V` (Id INT NOT NULL, N INT, L LONG, S CHAR(16), B OBJECT PRIMARY KEY Id)")
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := test.want
			if want == nil {
				want = test.val
			}
			var got any
			_, err := db.Exec("INSERT INTO `V` (Id, "+test.col+") VALUES (?, ?)", i, test.val)
			if err == nil {
				dest := reflect.New(reflect.TypeOf(want))
				if err = db.QueryRow("SELECT "+test.col+" FROM `V` WHERE Id = ?", i).Scan(dest.Interface()); err == nil {
					got = dest.Elem().Interface()
				}
			}
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("scan (-want +got):\n%s", diff)
			}

			if test.col == "B" {
				return
			}
			rows := queryRows(t, db, "SELECT Id FROM `V` WHERE Id = ? AND "+test.col+" = ?", i, test.val)
			if diff := cmp.Diff([][]any{{i}}, rows); diff != "" {
				t.Errorf("WHERE (-want +got):\n%s", diff)
			}
		})
	}
}

func TestScanIntoReader(t *testing.T) {
	tests := []struct {
		name string
		data []byte // nil inserts NULL
		dest any
	}{
		{"io.ReadSeeker", []byte("bin"), new(io.ReadSeeker)},
		{"pointer to io.ReadSeeker", []byte("bin"), new(*io.ReadSeeker)},
		{"io.Reader", []byte("bin"), new(io.Reader)},
		{"NULL into io.Reader", nil, new(io.Reader(strings.NewReader("stale")))},
	}

	db, _ := newDB(t)
	mustExec(t, db, "CREATE TABLE `V` (Id INT NOT NULL, B OBJECT PRIMARY KEY Id)")
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mustExec(t, db, "INSERT INTO `V` (Id, B) VALUES (?, ?)", i, test.data)
			if err := db.QueryRow("SELECT B FROM `V` WHERE Id = ?", i).Scan(test.dest); err != nil {
				t.Fatal(err)
			}
			v := reflect.ValueOf(test.dest).Elem()
			for v.Kind() == reflect.Pointer && !v.IsNil() {
				v = v.Elem()
			}
			var got []byte
			if r, _ := reflect.TypeAssert[io.Reader](v); r != nil {
				got, _ = io.ReadAll(r)
			}
			if !bytes.Equal(got, test.data) {
				t.Errorf("got %q, want %q", got, test.data)
			}
		})
	}
}
