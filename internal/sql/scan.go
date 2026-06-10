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

	kindIdent  // bare, `backtick`, or [bracket] identifier
	kindString // 'single-quoted' literal (payload decoded)
	kindInt    // unsigned integer literal
	kindParam  // ? placeholder

	kindLParen
	kindRParen
	kindComma
	kindDot
	kindStar
	kindMinus
	kindEq
	kindNe
	kindLt
	kindLe
	kindGt
	kindGe

	kwADD
	kwALTER
	kwAND
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
	kwLIKE
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
	kwWHERE
)

// token is one lexical token. For identifiers and string literals Text is the
// decoded value; otherwise it is the source slice. Pos is the byte offset of
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
	case b == '-':
		s.pos++
		return s.tok(kindMinus, start)
	case b == '=':
		s.pos++
		return s.tok(kindEq, start)
	case b == '<':
		return s.scanLess(start)
	case b == '>':
		return s.scanGreater(start)
	case b == '!':
		return s.scanBang(start)
	case b == '\'':
		return s.scanString(start)
	case b == '`':
		return s.scanQuoted(start, '`')
	case b == '[':
		return s.scanQuoted(start, ']')
	case isDigit(b):
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

func (s *scanner) scanBang(start int) token {
	s.pos++ // '!'
	if s.pos < len(s.input) && s.input[s.pos] == '=' {
		s.pos++
		return s.tok(kindNe, start)
	}
	return s.errorf(start, "expected '!='")
}

// scanString scans a 'single-quoted' literal. Two single quotes escape a quote.
func (s *scanner) scanString(start int) token {
	s.pos++ // opening quote
	from := s.pos
	escaped := false
	for s.pos < len(s.input) {
		if s.input[s.pos] == '\'' {
			if s.pos+1 < len(s.input) && s.input[s.pos+1] == '\'' {
				escaped = true
				s.pos += 2
				continue
			}
			text := s.input[from:s.pos]
			s.pos++ // closing quote
			if escaped {
				text = strings.ReplaceAll(text, "''", "'")
			}
			return token{Kind: kindString, Text: text, Pos: start}
		}
		s.pos++
	}
	return s.errorf(start, "unterminated string literal")
}

// scanQuoted scans a delimited identifier ending at delim (a backtick or ']').
func (s *scanner) scanQuoted(start int, delim byte) token {
	s.pos++ // opening delimiter
	from := s.pos
	for s.pos < len(s.input) {
		if s.input[s.pos] == delim {
			text := s.input[from:s.pos]
			s.pos++ // closing delimiter
			return token{Kind: kindIdent, Text: text, Pos: start}
		}
		s.pos++
	}
	return s.errorf(start, "unterminated quoted identifier")
}

func (s *scanner) scanNumber(start int) token {
	for s.pos < len(s.input) && isDigit(s.input[s.pos]) {
		s.pos++
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

// isSpace reports whether b is whitespace: space, tab, newline, or form feed.
// Carriage return is not, matching the MSI tokenizer.
func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\f' }

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// isIdentStart reports whether b can begin an identifier: a letter, '_', '$',
// or any byte >= 0x80. High bytes are admitted so UTF-8 identifiers scan
// without decoding.
func isIdentStart(b byte) bool {
	return b == '_' || b == '$' || b >= 0x80 || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdent(b byte) bool { return isIdentStart(b) || isDigit(b) }

// keywordKind reports the keyword kind for word, matched case-insensitively.
func keywordKind(word string) (kind, bool) { //nolint:funlen
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
	case "LIKE":
		return kwLIKE, true
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
	case "WHERE":
		return kwWHERE, true
	}
	return 0, false
}
