package stringpool_test

import (
	"fmt"
	"testing"

	"github.com/abemedia/go-msi/internal/stringpool"
)

var benchSizes = []int{100, 10_000}

// benchCodepage is UTF-16LE: the harder case for the encoder.
const benchCodepage = 1200

// realisticStrings returns n MSI-shaped identifier strings.
func realisticStrings(n int) []string {
	bases := []string{
		"File", "Component", "Feature", "Directory", "Media", "Property",
		"Registry", "Binary", "Icon", "Shortcut", "Class", "ProgId",
		"InstallExecuteSequence", "InstallUISequence", "AdminExecuteSequence",
		"CustomAction", "CreateFolder", "RemoveFile", "ServiceInstall",
		"ServiceControl", "Upgrade", "LaunchCondition", "AppSearch",
	}
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s_%d", bases[i%len(bases)], i)
	}
	return out
}

func BenchmarkSetCodepage(b *testing.B) {
	for _, n := range benchSizes {
		strs := realisticStrings(n)
		for i := range strs {
			strs[i] += "é" // every string has one non-ASCII rune to exercise the encoder
		}
		p, _ := stringpool.New(1252)
		for _, s := range strs {
			_, _ = p.Intern(s, true)
		}
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := p.SetCodepage(1250); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkIntern(b *testing.B) {
	for _, n := range benchSizes {
		strs := realisticStrings(n)
		for i := range strs {
			strs[i] += "é" // every string has one non-ASCII rune to exercise validation
		}
		b.Run(fmt.Sprintf("new/n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				p, _ := stringpool.New(benchCodepage)
				for _, s := range strs {
					_, _ = p.Intern(s, true)
				}
			}
		})
	}
	for _, n := range benchSizes {
		strs := realisticStrings(n)
		for i := range strs {
			strs[i] += "é"
		}
		b.Run(fmt.Sprintf("dedup/n=%d", n), func(b *testing.B) {
			p, _ := stringpool.New(benchCodepage)
			for _, s := range strs {
				_, _ = p.Intern(s, true)
			}
			b.ReportAllocs()
			for b.Loop() {
				for _, s := range strs {
					_, _ = p.Intern(s, true)
				}
			}
		})
	}
}

func BenchmarkLookup(b *testing.B) {
	for _, n := range benchSizes {
		strs := realisticStrings(n)
		p, _ := stringpool.New(benchCodepage)
		ids := make([]uint32, len(strs))
		for i, s := range strs {
			ids[i], _ = p.Intern(s, true)
		}
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, id := range ids {
					_, _ = p.Lookup(id)
				}
			}
		})
	}
}

func BenchmarkEncode(b *testing.B) {
	for _, n := range benchSizes {
		strs := realisticStrings(n)
		p, _ := stringpool.New(benchCodepage)
		for _, s := range strs {
			_, _ = p.Intern(s, true)
		}
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _, _ = stringpool.Encode(p)
			}
		})
	}
}

func BenchmarkDecode(b *testing.B) {
	for _, n := range benchSizes {
		strs := realisticStrings(n)
		p, _ := stringpool.New(benchCodepage)
		for _, s := range strs {
			_, _ = p.Intern(s, true)
		}
		pool, data, err := stringpool.Encode(p)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = stringpool.Decode(pool, data)
			}
		})
	}
}
