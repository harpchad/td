package query

import (
	"errors"
	"strings"
	"unicode"
)

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokLParen
	tokRParen
	tokPipe
	tokMinus
	tokPhrase
	tokAtom
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

// lex splits a filter string into tokens. Parentheses, the pipe, and a
// leading minus are punctuation; everything else is either a quoted phrase or
// a bare atom running to the next space or punctuation character. A minus
// inside an atom stays part of it, so OPS-1421 is one word.
func lex(s string) ([]token, error) {
	var out []token
	runes := []rune(s)
	i := 0

	for i < len(runes) {
		c := runes[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '(':
			out = append(out, token{kind: tokLParen, text: "(", pos: i})
			i++
		case c == ')':
			out = append(out, token{kind: tokRParen, text: ")", pos: i})
			i++
		case c == '|':
			out = append(out, token{kind: tokPipe, text: "|", pos: i})
			i++
		case c == '-' && atTermStart(runes, i):
			out = append(out, token{kind: tokMinus, text: "-", pos: i})
			i++
		case c == '"':
			start := i
			i++
			var sb strings.Builder
			closed := false
			for i < len(runes) {
				if runes[i] == '"' {
					closed = true
					i++
					break
				}
				sb.WriteRune(runes[i])
				i++
			}
			if !closed {
				return nil, errors.New("unclosed quote")
			}
			out = append(out, token{kind: tokPhrase, text: sb.String(), pos: start})
		default:
			start := i
			var sb strings.Builder
			for i < len(runes) {
				r := runes[i]
				if unicode.IsSpace(r) || r == '(' || r == ')' || r == '|' || r == '"' {
					break
				}
				sb.WriteRune(r)
				i++
			}
			out = append(out, token{kind: tokAtom, text: sb.String(), pos: start})
		}
	}

	out = append(out, token{kind: tokEOF, pos: len(runes)})
	return out, nil
}

// atTermStart reports whether position i begins a term, which is what decides
// that a minus negates rather than being part of a word. A minus opens a term
// at the start of the string, after whitespace, or straight after an opening
// paren or a pipe. Anywhere else it is an ordinary character, so OPS-1421
// stays one word.
func atTermStart(runes []rune, i int) bool {
	if i == 0 {
		return true
	}
	prev := runes[i-1]
	return unicode.IsSpace(prev) || prev == '(' || prev == '|'
}
