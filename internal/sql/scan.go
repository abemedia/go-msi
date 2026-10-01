package sql

import (
	"fmt"
	"strings"
)

// kind classifies a lexical token.
type kind uint8

const (
	kindError kind = iota
	kindEOF

	kindIdent  // bare or `backtick` identifier
	kindString // 'single-quoted' literal
	kindInt    // integer literal
	kindParam  // ? placeholder

	kindLParen
	kindRParen
	kindComma
	kindDot
	kindStar
	kindEq
	kindNe
	kindLt
	kindLe
	kindGt
	kindGe

	kwADD
	kwALTER
	kwAND
	kwAS
	kwBY
	kwCHAR
	kwCREATE
	kwDELETE
	kwDISTINCT
	kwDROP
	kwFREE
	kwFROM
	kwHOLD
	kwINSERT
	kwINT
	kwINTO
	kwIS
	kwKEY
	kwLOCALIZABLE
	kwLONG
	kwLONGCHAR
	kwNOT
	kwNULL
	kwOBJECT
	kwOR
	kwORDER
	kwPRIMARY
	kwSELECT
	kwSET
	kwSHORT
	kwTABLE
	kwTEMPORARY
	kwUPDATE
	kwVALUES
	kwVARCHAR
	kwWHERE
)

// token is one lexical token. For identifiers and string literals Text is the
// unquoted value; otherwise it is the source slice. Pos is the byte offset of
// the token in the input.
type token struct {
	Kind kind
	Text string
	Pos  int
}

// scanner tokenizes input. err holds the first lexical error, if any.
type scanner struct {
	input string
	pos   int // scan position
	err   *Error
}

func (s *scanner) errorf(pos int, format string, args ...any) token {
	if s.err == nil {
		s.err = &Error{Pos: pos, Msg: fmt.Sprintf(format, args...)}
	}
	return token{Kind: kindError, Pos: pos}
}

// tok builds a token spanning input[start:s.pos].
func (s *scanner) tok(k kind, start int) token {
	return token{Kind: k, Text: s.input[start:s.pos], Pos: start}
}

// scan returns the next token. At end of input it returns kindEOF; on a
// lexical error it sets err and returns a kindError token.
func (s *scanner) scan() token {
	for s.pos < len(s.input) && isSpace(s.input[s.pos]) {
		s.pos++
	}
	start := s.pos
	if s.pos >= len(s.input) {
		return token{Kind: kindEOF, Pos: start}
	}
	switch b := s.input[s.pos]; {
	case b == '(':
		s.pos++
		return s.tok(kindLParen, start)
	case b == ')':
		s.pos++
		return s.tok(kindRParen, start)
	case b == ',':
		s.pos++
		return s.tok(kindComma, start)
	case b == '.':
		s.pos++
		return s.tok(kindDot, start)
	case b == '*':
		s.pos++
		return s.tok(kindStar, start)
	case b == '?':
		s.pos++
		return s.tok(kindParam, start)
	case b == '=':
		s.pos++
		return s.tok(kindEq, start)
	case b == '<':
		return s.scanLess(start)
	case b == '>':
		return s.scanGreater(start)
	case b == '\'':
		return s.scanString(start)
	case b == '`':
		return s.scanQuoted(start)
	case b == '-', isDigit(b):
		return s.scanNumber(start)
	case isIdentStart(b):
		return s.scanIdent(start)
	default:
		t := s.errorf(s.pos, "unexpected character %q", b)
		s.pos++ // consume the byte so repeated scan calls make progress
		return t
	}
}

func (s *scanner) scanLess(start int) token {
	s.pos++ // '<'
	if s.pos < len(s.input) {
		switch s.input[s.pos] {
		case '=':
			s.pos++
			return s.tok(kindLe, start)
		case '>':
			s.pos++
			return s.tok(kindNe, start)
		}
	}
	return s.tok(kindLt, start)
}

func (s *scanner) scanGreater(start int) token {
	s.pos++ // '>'
	if s.pos < len(s.input) && s.input[s.pos] == '=' {
		s.pos++
		return s.tok(kindGe, start)
	}
	return s.tok(kindGt, start)
}

// scanString scans a 'single-quoted' literal.
func (s *scanner) scanString(start int) token {
	from := start + 1
	n := strings.IndexByte(s.input[from:], '\'')
	if n < 0 {
		return s.errorf(start, "unterminated string literal")
	}
	s.pos = from + n + 1
	return token{Kind: kindString, Text: s.input[from : from+n], Pos: start}
}

// scanQuoted scans a `backtick-quoted` identifier.
func (s *scanner) scanQuoted(start int) token {
	from := start + 1
	n := strings.IndexByte(s.input[from:], '`')
	if n < 0 {
		return s.errorf(start, "unterminated quoted identifier")
	}
	if n == 0 {
		return s.errorf(start, "empty quoted identifier")
	}
	s.pos = from + n + 1
	return token{Kind: kindIdent, Text: s.input[from : from+n], Pos: start}
}

func (s *scanner) scanNumber(start int) token {
	s.pos++ // sign or first digit
	for s.pos < len(s.input) && isDigit(s.input[s.pos]) {
		s.pos++
	}
	if s.input[start:s.pos] == "-" {
		return s.errorf(start, "expected digit after '-'")
	}
	if s.pos < len(s.input) && isIdentStart(s.input[s.pos]) {
		for s.pos < len(s.input) && isIdent(s.input[s.pos]) {
			s.pos++
		}
		return s.errorf(start, "unrecognized token %q", s.input[start:s.pos])
	}
	return s.tok(kindInt, start)
}

func (s *scanner) scanIdent(start int) token {
	for s.pos < len(s.input) && isIdent(s.input[s.pos]) {
		s.pos++
	}
	if k, ok := keywordKind(s.input[start:s.pos]); ok {
		return s.tok(k, start)
	}
	return s.tok(kindIdent, start)
}

// isSpace reports whether b is whitespace: space, tab, newline, carriage
// return, or form feed.
func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// isIdentStart reports whether b can begin an identifier: a letter, '_', '$',
// or any byte >= 0x80. High bytes are admitted so UTF-8 identifiers scan
// without decoding.
func isIdentStart(b byte) bool {
	return b == '_' || b == '$' || b >= 0x80 || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdent(b byte) bool { return isIdentStart(b) || isDigit(b) }

// keywordKind reports the keyword kind for word, matched case-insensitively.
func keywordKind(word string) (kind, bool) {
	const minKeywordLen = len("IS")
	const maxKeywordLen = len("LOCALIZABLE")
	if len(word) < minKeywordLen || len(word) > maxKeywordLen {
		return 0, false // outside any keyword's length range
	}
	var buf [maxKeywordLen]byte
	for i, c := range []byte(word) {
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		buf[i] = c
	}
	switch string(buf[:len(word)]) {
	case "ADD":
		return kwADD, true
	case "ALTER":
		return kwALTER, true
	case "AND":
		return kwAND, true
	case "AS":
		return kwAS, true
	case "BY":
		return kwBY, true
	case "CHAR", "CHARACTER":
		return kwCHAR, true
	case "CREATE":
		return kwCREATE, true
	case "DELETE":
		return kwDELETE, true
	case "DISTINCT":
		return kwDISTINCT, true
	case "DROP":
		return kwDROP, true
	case "FREE":
		return kwFREE, true
	case "FROM":
		return kwFROM, true
	case "HOLD":
		return kwHOLD, true
	case "INSERT":
		return kwINSERT, true
	case "INT", "INTEGER":
		return kwINT, true
	case "INTO":
		return kwINTO, true
	case "IS":
		return kwIS, true
	case "KEY":
		return kwKEY, true
	case "LOCALIZABLE":
		return kwLOCALIZABLE, true
	case "LONG":
		return kwLONG, true
	case "LONGCHAR":
		return kwLONGCHAR, true
	case "NOT":
		return kwNOT, true
	case "NULL":
		return kwNULL, true
	case "OBJECT":
		return kwOBJECT, true
	case "OR":
		return kwOR, true
	case "ORDER":
		return kwORDER, true
	case "PRIMARY":
		return kwPRIMARY, true
	case "SELECT":
		return kwSELECT, true
	case "SET":
		return kwSET, true
	case "SHORT":
		return kwSHORT, true
	case "TABLE":
		return kwTABLE, true
	case "TEMPORARY":
		return kwTEMPORARY, true
	case "UPDATE":
		return kwUPDATE, true
	case "VALUES":
		return kwVALUES, true
	case "VARCHAR":
		return kwVARCHAR, true
	case "WHERE":
		return kwWHERE, true
	}
	return 0, false
}
