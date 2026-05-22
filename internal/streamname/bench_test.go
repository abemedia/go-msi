package streamname_test

import (
	"testing"

	"github.com/abemedia/go-msi/internal/streamname"
)

const benchName = "InstallExecuteSequence"

func BenchmarkEncode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = streamname.Encode(benchName)
	}
}

func BenchmarkDecode(b *testing.B) {
	encoded := streamname.Encode(benchName)
	b.ReportAllocs()
	for b.Loop() {
		_ = streamname.Decode(encoded)
	}
}
