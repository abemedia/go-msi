package stringpool_test

import (
	"testing"

	"github.com/abemedia/go-msi/internal/stringpool"
)

func TestInternStringNotInCodepage(t *testing.T) {
	p, err := stringpool.New(1252)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Intern("日本語", true); err == nil { // not representable in Windows-1252
		t.Error("Intern succeeded, want error")
	}
}
