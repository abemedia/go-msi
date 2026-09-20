package streamname_test

import (
	"testing"
	"unicode/utf16"

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

func TestEncodedLen(t *testing.T) {
	inputs := []string{
		"",
		"A",           // single
		"AB",          // pair
		"ABC",         // pair + single
		"_Tables",     // multiple pairs
		"!",           // non-alphabet ASCII
		"é",           // non-ASCII BMP
		"䡀_Tables",    // pass-through marker + alphabet
		"\U0001F600",  // non-BMP, occupies a UTF-16 surrogate pair
		"A\U0001F600", // mixed
	}
	for _, in := range inputs {
		got := streamname.EncodedLen(in)
		want := len(utf16.Encode([]rune(streamname.Encode(in))))
		if got != want {
			t.Errorf("EncodedLen(%q) = %d, want %d", in, got, want)
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
