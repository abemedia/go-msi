// Package streamname encodes and decodes the names under which an MSI
// database stores its tables and other internal streams.
package streamname

import (
	"unicode/utf8"
	"unsafe"
)

const (
	symbolBits  = 6
	symbolCount = 1 << symbolBits
	symbolMask  = symbolCount - 1

	singleBase = 0x4800 // one symbol: singleBase + symbol
	pairBase   = 0x3800 // two symbols: pairBase + symbol1 + symbol2<<symbolBits
)

// Encode returns the compound-file stream name for the MSI name s.
// Any character other than an ASCII letter, digit, '.', or '_' is passed
// through unchanged.
func Encode(s string) string {
	buf := make([]byte, 0, 3*len(s))
	for i := 0; i < len(s); {
		r1 := encodeSymbol(rune(s[i]))
		if r1 < 0 {
			// non-alphabet runes may be multi-byte
			_, n := utf8.DecodeRuneInString(s[i:])
			buf = append(buf, s[i:i+n]...)
			i += n
			continue
		}
		if i+1 < len(s) {
			if r2 := encodeSymbol(rune(s[i+1])); r2 >= 0 {
				buf = utf8.AppendRune(buf, pairBase+r1+r2<<symbolBits)
				i += 2
				continue
			}
		}
		buf = utf8.AppendRune(buf, singleBase+r1)
		i++
	}
	return unsafe.String(unsafe.SliceData(buf), len(buf))
}

// Decode returns the MSI name stored under the compound-file stream name s.
// Any code point outside the range U+3800 to U+483F is passed through unchanged.
func Decode(s string) string {
	buf := make([]byte, 0, len(s))
	for _, c := range s {
		switch {
		case c >= pairBase && c < singleBase:
			pair := c - pairBase
			buf = utf8.AppendRune(buf, decodeSymbol(pair&symbolMask))
			buf = utf8.AppendRune(buf, decodeSymbol(pair>>symbolBits))
		case c >= singleBase && c < singleBase+symbolCount:
			buf = utf8.AppendRune(buf, decodeSymbol(c-singleBase))
		default:
			buf = utf8.AppendRune(buf, c)
		}
	}
	return unsafe.String(unsafe.SliceData(buf), len(buf))
}

// encodeSymbol returns the 6-bit alphabet value of r, or -1 if r is not an
// alphabet character.
func encodeSymbol(r rune) rune {
	switch {
	case r >= '0' && r <= '9':
		return r - '0'
	case r >= 'A' && r <= 'Z':
		return r - 'A' + 10
	case r >= 'a' && r <= 'z':
		return r - 'a' + 36
	case r == '.':
		return 62
	case r == '_':
		return 63
	default:
		return -1
	}
}

// decodeSymbol returns the alphabet character for the 6-bit value r.
func decodeSymbol(r rune) rune {
	switch {
	case r < 10:
		return '0' + r
	case r < 36:
		return 'A' + r - 10
	case r < 62:
		return 'a' + r - 36
	case r == 62:
		return '.'
	default: // r == 63
		return '_'
	}
}
