package query

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// knownKeys is the closed set of key:value prefixes. An unrecognized key is a
// parse error rather than free text, so a typo surfaces instead of silently
// becoming a search term that matches nothing.
var knownKeys = []string{"is", "p", "due", "start", "src", "has", "notify", "grp"}

var knownIs = []string{
	"open", "done", "todo", "doing", "waiting", "inbox",
	"dropped", "orphan", "snoozed", "deferred",
}

var knownHas = []string{"attachment", "notes", "sub"}

var knownRoles = []string{"assigner", "assignee", "involved", "waiting"}

// ParseError is what every failure out of ParseAt is wrapped in. It exists so
// a caller can tell a user's typo from a server fault without matching on
// message text: the API answers 400 for one and 500 for the other.
type ParseError struct {
	// Query is the string that failed to parse.
	Query string
	// Msg names the problem, in the wording testdata/filter_cases.json fixes.
	Msg string
}

// Error implements the error interface.
func (e *ParseError) Error() string { return e.Msg }

// Parse parses a filter string against the current wall clock. Date keywords
// resolve in the clock's location.
func Parse(s string) (Node, error) {
	return ParseAt(s, time.Now())
}

// ParseAt parses a filter string, resolving date keywords against now. The
// server passes its configured timezone here so that "today" means the user's
// today rather than the container's.
//
// An empty or whitespace-only string parses to a nil Node, which matches
// every task.
func ParseAt(s string, now time.Time) (Node, error) {
	n, err := parseAt(s, now)
	if err != nil {
		return nil, &ParseError{Query: s, Msg: err.Error()}
	}
	return n, nil
}

func parseAt(s string, now time.Time) (Node, error) {
	toks, err := lex(s)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, now: now}
	if p.peek().kind == tokEOF {
		return nil, nil
	}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		if p.peek().kind == tokRParen {
			return nil, errors.New("unexpected )")
		}
		return nil, fmt.Errorf("unexpected %q", p.peek().text)
	}
	return n, nil
}

type parser struct {
	toks []token
	i    int
	now  time.Time
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tokEOF {
		p.i++
	}
	return t
}

func (p *parser) parseOr() (Node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokPipe {
		return left, nil
	}
	nodes := []Node{left}
	for p.peek().kind == tokPipe {
		p.next()
		if !startsTerm(p.peek().kind) {
			return nil, errors.New("expected a term after |")
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, right)
	}
	return &Or{Nodes: nodes}, nil
}

func (p *parser) parseAnd() (Node, error) {
	var nodes []Node
	for startsTerm(p.peek().kind) {
		n, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	switch len(nodes) {
	case 0:
		return nil, errors.New("expected a term")
	case 1:
		return nodes[0], nil
	default:
		return &And{Nodes: nodes}, nil
	}
}

func (p *parser) parseTerm() (Node, error) {
	if p.peek().kind == tokMinus {
		p.next()
		if !startsTerm(p.peek().kind) || p.peek().kind == tokMinus {
			return nil, errors.New("expected a term after -")
		}
		inner, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return &Not{Node: inner}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Node, error) {
	t := p.next()
	switch t.kind {
	case tokLParen:
		if !startsTerm(p.peek().kind) {
			return nil, errors.New("unclosed group")
		}
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokRParen {
			return nil, errors.New("unclosed group")
		}
		p.next()
		return inner, nil
	case tokPhrase:
		return &Phrase{Text: t.text}, nil
	case tokAtom:
		return p.atom(t.text)
	default:
		return nil, errors.New("expected a term")
	}
}

func startsTerm(k tokenKind) bool {
	switch k {
	case tokMinus, tokLParen, tokPhrase, tokAtom:
		return true
	default:
		return false
	}
}

// atom classifies a bare token into a predicate or free text.
func (p *parser) atom(raw string) (Node, error) {
	switch {
	case strings.HasPrefix(raw, "#"):
		name := strings.ToLower(raw[1:])
		if name == "" {
			return nil, errors.New("expected a tag after #")
		}
		return &Tag{Name: name}, nil

	case strings.HasPrefix(raw, "@"):
		rest := raw[1:]
		if rest == "" {
			return nil, errors.New("expected a person handle after @")
		}
		handle, roleTxt, hasRole := strings.Cut(rest, ":")
		handle = strings.ToLower(handle)
		if handle == "" {
			return nil, errors.New("expected a person handle after @")
		}
		if !hasRole {
			return &Person{Handle: handle}, nil
		}
		role := strings.ToLower(roleTxt)
		if !contains(knownRoles, role) {
			return nil, fmt.Errorf("unknown person role %q (try: %s)", roleTxt, strings.Join(knownRoles, ", "))
		}
		return &Person{Handle: handle, Role: &role}, nil
	}

	key, value, hasColon := strings.Cut(raw, ":")
	if !hasColon {
		return &Word{Text: strings.ToLower(raw)}, nil
	}

	lkey := strings.ToLower(key)
	if !contains(knownKeys, lkey) {
		return nil, fmt.Errorf("unknown filter key %q (try: %s)", key, strings.Join(knownKeys, ", "))
	}
	return p.predicate(lkey, value)
}

func (p *parser) predicate(key, value string) (Node, error) {
	lval := strings.ToLower(value)

	switch key {
	case "is":
		if !contains(knownIs, lval) {
			return nil, fmt.Errorf("unknown is: value %q (try: %s)", value, strings.Join(knownIs, ", "))
		}
		return &Is{Value: lval}, nil

	case "p":
		op, rest := splitOp(lval)
		n, err := strconv.Atoi(rest)
		if err != nil || n < 1 || n > 4 {
			return nil, errors.New("priority must be 1-4")
		}
		return &Priority{Op: op, Value: n}, nil

	case "due", "start":
		op, rest := splitOp(lval)
		switch rest {
		case "none":
			return &Date{Field: key, Op: op, Special: "none"}, nil
		case "overdue":
			return &Date{Field: key, Op: op, Special: "overdue"}, nil
		}
		resolved, err := ResolveDate(rest, p.now)
		if err != nil {
			return nil, err
		}
		return &Date{Field: key, Op: op, Value: resolved}, nil

	case "src":
		if lval == "" {
			return nil, errors.New("src: expects a source name")
		}
		return &Src{Name: lval}, nil

	case "has":
		if !contains(knownHas, lval) {
			return nil, fmt.Errorf("unknown has: value %q (try: %s)", value, strings.Join(knownHas, ", "))
		}
		return &Has{What: lval}, nil

	case "notify":
		if lval != "auto" && lval != "on" && lval != "off" {
			return nil, fmt.Errorf("notify must be auto, on, or off, got %q", value)
		}
		return &Notify{Mode: lval}, nil

	case "grp":
		if lval == "" {
			return nil, errors.New("grp: expects a group name")
		}
		return &Grp{Name: lval}, nil
	}

	return nil, fmt.Errorf("unknown filter key %q (try: %s)", key, strings.Join(knownKeys, ", "))
}

// splitOp peels a leading comparison operator off a value. A value with no
// operator compares for equality.
func splitOp(v string) (op, rest string) {
	for _, cand := range []string{"<=", ">=", "<", ">", "="} {
		if strings.HasPrefix(v, cand) {
			return cand, v[len(cand):]
		}
	}
	return "=", v
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
