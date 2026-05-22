package streamname_test

import (
	"testing"

	"github.com/abemedia/go-msi/internal/streamname"
)

func TestEncodeDecode(t *testing.T) {
	tests := []struct{ decoded, encoded string }{
		{"", ""},
		{"A", "\u480a"},
		{"AB", "\u3aca"},
		{"File", "\u430f\u422f"},
		{"\u4840_Tables", "\u4840\u3f7f\u4164\u422f\u4836"}, // U+4840 table marker, passes through unchanged
	}
	for _, test := range tests {
		if got := streamname.Encode(test.decoded); got != test.encoded {
			t.Errorf("Encode(%q) = %q, want %q", test.decoded, got, test.encoded)
		}
		if got := streamname.Decode(test.encoded); got != test.decoded {
			t.Errorf("Decode(%q) = %q, want %q", test.encoded, got, test.decoded)
		}
	}
}

// TestAlphabetRoundTrip exercises every alphabet value on both sides of the
// codec: all 64 single symbols and all 4096 ordered pairs.
func TestAlphabetRoundTrip(t *testing.T) {
	alphabet := []rune("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz._")
	for _, c1 := range alphabet {
		single := string(c1)
		if got := streamname.Decode(streamname.Encode(single)); got != single {
			t.Errorf("round trip %q = %q", single, got)
		}
		for _, c2 := range alphabet {
			pair := string([]rune{c1, c2})
			if got := streamname.Decode(streamname.Encode(pair)); got != pair {
				t.Errorf("round trip %q = %q", pair, got)
			}
		}
	}
}
