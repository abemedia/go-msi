package stringpool_test

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/abemedia/go-msi/internal/stringpool"
)

func TestPool(t *testing.T) {
	p, err := stringpool.New(1252)
	if err != nil {
		t.Fatal(err)
	}

	if s, ok := p.Lookup(0); s != "" || !ok {
		t.Errorf("Lookup(0) = (%q, %v), want (\"\", true)", s, ok)
	}
	if _, ok := p.Lookup(1); ok {
		t.Error("Lookup(1) ok = true, want false (out of range)")
	}
	if id := p.Intern(""); id != 0 {
		t.Errorf("Intern(\"\") = %d, want 0", id)
	}

	a, b := p.Intern("a"), p.Intern("b")
	if a == 0 || b == 0 || a == b {
		t.Fatalf("Intern returned a=%d, b=%d", a, b)
	}
	if p.Intern("a") != a {
		t.Error("Intern(\"a\") second call returned different ID")
	}
	if s, ok := p.Lookup(a); s != "a" || !ok {
		t.Errorf("Lookup(a) = (%q, %v), want (\"a\", true)", s, ok)
	}

	p.Release(a) // refcount 2 -> 1
	if s, ok := p.Lookup(a); s != "a" || !ok {
		t.Errorf("Lookup after partial Release = (%q, %v), want (\"a\", true)", s, ok)
	}
	p.Release(a) // refcount 1 -> 0
	if _, ok := p.Lookup(a); ok {
		t.Error("Lookup after full Release ok = true, want false")
	}
	if c := p.Intern("c"); c != a {
		t.Errorf("Intern after Release: got %d, want reused %d", c, a)
	}

	p.Release(99999) // out-of-range must not panic
}

func TestRoundTrip(t *testing.T) {
	p, err := stringpool.New(1252)
	if err != nil {
		t.Fatal(err)
	}
	strs := []string{"File", strings.Repeat("A", 100_000), "Component", "middle", "Feature"}
	ids := make([]uint32, len(strs))
	for i, s := range strs {
		ids[i] = p.Intern(s)
	}
	p.Release(ids[3]) // create a gap
	pool, data, err := stringpool.Encode(p)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	p2, err := stringpool.Decode(pool, data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p2.Codepage() != 1252 {
		t.Errorf("Codepage = %d, want 1252", p2.Codepage())
	}
	for i, s := range strs {
		want, wantOK := s, true
		if i == 3 {
			want, wantOK = "", false
		}
		if got, ok := p2.Lookup(ids[i]); got != want || ok != wantOK {
			t.Errorf("Lookup(ids[%d]) = (%q, %v), want (%q, %v)", i, got, ok, want, wantOK)
		}
	}
}

func TestRoundTripLongRefs(t *testing.T) {
	b1 := binary.LittleEndian.AppendUint32(nil, 1252|0x80000000) // cp=1252 + long-refs flag
	p1, err := stringpool.Decode(b1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p1.LongRefs() {
		t.Fatal("LongRefs() = false after decoding flag-set header")
	}

	b2, _, err := stringpool.Encode(p1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := stringpool.Decode(b2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p2.LongRefs() {
		t.Error("LongRefs() = false after round-trip")
	}
}
