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
	p.Intern("日本語") // not representable in Windows-1252
	if _, _, err := stringpool.Encode(p); err == nil {
		t.Error("Encode succeeded, want error")
	}
}
