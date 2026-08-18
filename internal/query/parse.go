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
var knownKeys = []string{"is", "p", "due", "start", "src", "has", "notify", "grp", "sort"}

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
	q, err := ParseQueryAt(s, now)
	return q.Node, err
}

// ParseQueryAt parses a filter and the order it asks for.
//
// The richer entry point, used by anything that returns a list. ParseAt stays
// for the callers that only ever needed the predicate, which is most of them.
func ParseQueryAt(s string, now time.Time) (Query, error) {
	q, err := parseAt(s, now)
	if err != nil {
		return Query{}, &ParseError{Query: s, Msg: err.Error()}
	}
	return q, nil
}

func parseAt(s string, now time.Time) (Query, error) {
	toks, err := lex(s)
	if err != nil {
		return Query{}, err
	}
	p := &parser{toks: toks, now: now}
	if p.peek().kind == tokEOF {
		return Query{}, nil
	}
	n, err := p.parseOr()
	if err != nil {
		return Query{}, err
	}
	if p.peek().kind != tokEOF {
		if p.peek().kind == tokRParen {
			return Query{}, errors.New("unexpected )")
		}
		return Query{}, fmt.Errorf("unexpected %q", p.peek().text)
	}
	return Query{Node: n, Sort: p.sort}, nil
}

type parser struct {
	toks []token
	i    int
	now  time.Time
	// sort is filled in by a sort: term wherever it appears. It applies to the
	// whole query: it is an instruction about the answer, not part of the
	// question, so its position in the string carries no meaning.
	sort Sort
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
	terms := 0
	for startsTerm(p.peek().kind) {
		n, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		terms++
		// A sort: term parses to no node. It said how to order the answer, not
		// what belongs in it, so it contributes nothing to match against and
		// "sort:due" on its own is every task in that order.
		if n != nil {
			nodes = append(nodes, n)
		}
	}
	switch {
	case terms == 0:
		return nil, errors.New("expected a term")
	case len(nodes) == 0:
		return nil, nil
	case len(nodes) == 1:
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
	if lkey == "sort" {
		// Recorded and dropped rather than turned into a node. It says how to
		// order the answer, not what belongs in it, and a predicate that
		// matched everything would be a term somebody could put under an OR
		// where it would mean nothing at all.
		if err := p.setSort(value); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return p.predicate(lkey, value)
}

// setSort reads a sort: value. A comma composes keys, and a leading minus
// reverses the key it is attached to: sort:due,-priority.
func (p *parser) setSort(value string) error {
	raw := strings.ToLower(strings.TrimSpace(value))
	if raw == "" {
		return fmt.Errorf("sort: needs a key (try: %s)", strings.Join(SortKeys, ", "))
	}
	var next Sort
	for _, part := range strings.Split(raw, ",") {
		desc := strings.HasPrefix(part, "-")
		key := strings.TrimPrefix(part, "-")
		if key == "" {
			return fmt.Errorf("sort: needs a key (try: %s)", strings.Join(SortKeys, ", "))
		}
		if !contains(SortKeys, key) {
			return fmt.Errorf("cannot sort by %q (try: %s)", key, strings.Join(SortKeys, ", "))
		}
		// The same key twice in one list is dead weight: the second copy can
		// never break a tie the first left, so it is a typo worth reporting.
		for _, k := range next.Keys {
			if k.Key == key {
				return fmt.Errorf("sort:%s sorts by %q twice", raw, key)
			}
		}
		next.Keys = append(next.Keys, SortKey{Key: key, Desc: desc})
	}
	if p.sort.Explicit() && !p.sort.Equal(next) {
		return fmt.Errorf("two different sorts in one query: %s and %s", p.sort, next)
	}
	p.sort = next
	return nil
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
