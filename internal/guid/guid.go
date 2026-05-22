// Package guid converts COM GUIDs between the canonical 8-4-4-4-12 string
// form and the mixed-endian 16-byte COM layout (Data1/2/3 little-endian,
// Data4 as-is) used by FMTID and CLSID values.
package guid

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// Parse converts a GUID string into its COM byte layout. Surrounding braces
// and dashes are optional.
func Parse(s string) ([16]byte, error) {
	var b [16]byte
	src := s
	if strings.HasPrefix(src, "{") && strings.HasSuffix(src, "}") {
		src = src[1 : len(src)-1]
	}
	switch {
	case len(src) == 36 && src[8] == '-' && src[13] == '-' && src[18] == '-' && src[23] == '-':
		src = src[:8] + src[9:13] + src[14:18] + src[19:23] + src[24:]
	case len(src) == 32:
	default:
		return b, fmt.Errorf("guid: invalid GUID %q", s)
	}
	if _, err := hex.Decode(b[:], []byte(src)); err != nil {
		return [16]byte{}, fmt.Errorf("guid: invalid GUID %q", s)
	}
	slices.Reverse(b[0:4])
	slices.Reverse(b[4:6])
	slices.Reverse(b[6:8])
	return b, nil
}

// MustParse is like [Parse] but panics on malformed input.
func MustParse(s string) [16]byte {
	b, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return b
}

// Format returns b as an uppercase 8-4-4-4-12 GUID string.
func Format(b [16]byte) string {
	slices.Reverse(b[0:4])
	slices.Reverse(b[4:6])
	slices.Reverse(b[6:8])
	h := strings.ToUpper(hex.EncodeToString(b[:]))
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
