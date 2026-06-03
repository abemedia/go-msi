package stringpool_test

import (
	"encoding/binary"
	"errors"
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
	if id := p.Intern("", true); id != 0 {
		t.Errorf("Intern(\"\") = %d, want 0", id)
	}

	a := p.Intern("a", true)
	b := p.Intern("b", true)
	if a == 0 || b == 0 || a == b {
		t.Fatalf("Intern returned a=%d, b=%d", a, b)
	}
	if id := p.Intern("a", true); id != a {
		t.Error("Intern(\"a\") second call returned different ID")
	}
	if s, ok := p.Lookup(a); s != "a" || !ok {
		t.Errorf("Lookup(a) = (%q, %v), want (\"a\", true)", s, ok)
	}

	p.Release(a, true) // refcount 2 -> 1
	if s, ok := p.Lookup(a); s != "a" || !ok {
		t.Errorf("Lookup after partial Release = (%q, %v), want (\"a\", true)", s, ok)
	}
	p.Release(a, true) // refcount 1 -> 0
	if _, ok := p.Lookup(a); ok {
		t.Error("Lookup after full Release ok = true, want false")
	}
	if c := p.Intern("c", true); c != a {
		t.Errorf("Intern after Release: got %d, want reused %d", c, a)
	}

	x := p.Intern("x", false)
	if x == 0 {
		t.Fatal("Intern(\"x\", false) returned 0")
	}
	if s, ok := p.Lookup(x); s != "x" || !ok {
		t.Errorf("Lookup(x) = (%q, %v), want (\"x\", true)", s, ok)
	}
	p.Release(x, false)
	if _, ok := p.Lookup(x); ok {
		t.Error("Lookup after nonpersistent Release ok = true, want false")
	}

	yP := p.Intern("y", true)
	if yN := p.Intern("y", false); yN != yP {
		t.Fatalf("same string got different IDs: persistent %d, nonpersistent %d", yP, yN)
	}
	p.Release(yP, true)
	if _, ok := p.Lookup(yP); !ok {
		t.Error("slot freed after persistent release; nonpersistent ref should keep it alive")
	}
	p.Release(yP, false)
	if _, ok := p.Lookup(yP); ok {
		t.Error("slot still live after both refs released")
	}

	p.Release(99999, true) // out-of-range must not panic
}

func TestRoundTrip(t *testing.T) {
	p, err := stringpool.New(1252)
	if err != nil {
		t.Fatal(err)
	}
	strs := []string{"File", strings.Repeat("A", 100_000), "Component", "middle", "Feature"}
	ids := make([]uint32, len(strs))
	for i, s := range strs {
		ids[i] = p.Intern(s, true)
	}
	p.Release(ids[3], true) // create a gap
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

func TestPool_Validate(t *testing.T) {
	p, err := stringpool.New(1252)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate("English"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := p.Validate("日本語"); err == nil { // not representable in Windows-1252
		t.Error("Validate succeeded, want error")
	}
}

func TestPool_SetCodepage(t *testing.T) {
	t.Run("ascii pool switches cleanly", func(t *testing.T) {
		p, err := stringpool.New(1252)
		if err != nil {
			t.Fatal(err)
		}
		p.Intern("hello", true)
		if err := p.SetCodepage(1250); err != nil {
			t.Fatalf("SetCodepage: %v", err)
		}
		if got := p.Codepage(); got != 1250 {
			t.Errorf("Codepage = %d, want 1250", got)
		}
	})

	t.Run("persistent unencodable fails and preserves original codepage", func(t *testing.T) {
		p, err := stringpool.New(1252)
		if err != nil {
			t.Fatal(err)
		}
		p.Intern("日本語", true)
		if err := p.SetCodepage(1250); err == nil {
			t.Fatal("SetCodepage succeeded, want error")
		}
		if got := p.Codepage(); got != 1252 {
			t.Errorf("Codepage = %d, want 1252 (preserved on failure)", got)
		}
	})

	t.Run("nonpersistent unencodable is skipped", func(t *testing.T) {
		p, err := stringpool.New(1252)
		if err != nil {
			t.Fatal(err)
		}
		p.Intern("日本語", false) // accepted; nonpersistent isn't validated by SetCodepage
		if err := p.SetCodepage(1250); err != nil {
			t.Fatalf("SetCodepage: %v (should skip nonpersistent entries)", err)
		}
		if got := p.Codepage(); got != 1250 {
			t.Errorf("Codepage = %d, want 1250", got)
		}
	})

	t.Run("unsupported codepage fails", func(t *testing.T) {
		p, err := stringpool.New(1252)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.SetCodepage(437); !errors.Is(err, stringpool.ErrUnsupportedCodePage) {
			t.Errorf("SetCodepage(437) error = %v, want ErrUnsupportedCodePage", err)
		}
	})
}
