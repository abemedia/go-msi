package stringpool_test

import (
	"testing"

	"github.com/abemedia/go-msi/internal/stringpool"
)

func TestEncodeStringNotInCodepage(t *testing.T) {
	p, err := stringpool.New(1252)
	if err != nil {
		t.Fatal(err)
	}
	p.Intern("日本語", true) // not representable in Windows-1252
	if _, _, err := stringpool.Encode(p); err == nil {
		t.Error("Encode succeeded, want error")
	}
}

func TestEncodeNonpersistent(t *testing.T) {
	p, err := stringpool.New(1252)
	if err != nil {
		t.Fatal(err)
	}
	p.Intern("persist1", true)                // ID 1
	idInterior := p.Intern("interior", false) // ID 2: dead, but persist2 follows
	idP2 := p.Intern("persist2", true)        // ID 3
	p.Intern("trailing1", false)              // ID 4: dead and last
	p.Intern("trailing2", false)              // ID 5: dead and last

	pool, data, err := stringpool.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := stringpool.Decode(pool, data)
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range []string{"persist1", "persist2"} {
		if _, ok := p2.LookupID(s); !ok {
			t.Errorf("%s missing from decoded pool", s)
		}
	}
	for _, s := range []string{"interior", "trailing1", "trailing2"} {
		if _, ok := p2.LookupID(s); ok {
			t.Errorf("%s present in decoded pool, want absent", s)
		}
	}

	// The interior dead entry stays as a placeholder, so persist2 keeps its ID.
	if s, ok := p2.Lookup(idP2); s != "persist2" || !ok {
		t.Errorf("persist2 ID shifted: Lookup(%d) = (%q, %v)", idP2, s, ok)
	}

	// Trailing dead entries are dropped, not parked on the free list: a fresh
	// intern reuses the interior placeholder, not a trailing slot.
	if id := p2.Intern("reuse", true); id != idInterior {
		t.Errorf("Intern after decode = %d, want %d; trailing entries not trimmed", id, idInterior)
	}
}
