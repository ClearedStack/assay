// Package yamlite reads the small part of YAML that Assay's rule files and
// deploy files are written in, using only the standard library.
//
// It is strict on purpose. A rule file that means something other than what
// it appears to say is worse than one that will not load, so anything
// outside the subset is an error, never a guess:
//
//   - block mappings and block sequences, indented with spaces;
//   - flow mappings {a: 1} and flow sequences [a, b], on one line;
//   - plain, "double-quoted" and 'single-quoted' scalars;
//   - comments, and documents separated by a line of three dashes.
//
// A plain scalar is true or false, a whole number, or a string. There are
// no nulls, no fractions, no anchors, aliases or tags, and no multi-line
// scalars. A word YAML dialects disagree about (yes, no, on, off, a number
// with a leading zero, and the like) must be quoted. A key given twice is
// an error.
package yamlite

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kind is the kind of a node.
type Kind int

const (
	Map Kind = iota + 1
	Seq
	String
	Int
	Bool
)

func (k Kind) String() string {
	switch k {
	case Map:
		return "mapping"
	case Seq:
		return "list"
	case String:
		return "string"
	case Int:
		return "whole number"
	case Bool:
		return "true or false"
	}
	return "nothing"
}

// Node is one value in a parsed document.
type Node struct {
	Kind Kind
	Line int // 1-based line the value starts on

	Str  string
	Int  int64
	Bool bool

	Keys  []string // keys of a mapping, in the order written
	Map   map[string]*Node
	Items []*Node // items of a list
}

type line struct {
	indent int
	text   string
	num    int
}

type parser struct {
	lines []line
	pos   int
}

// Parse reads data as exactly one document.
func Parse(data []byte) (*Node, error) {
	docs, err := ParseAll(data)
	if err != nil {
		return nil, err
	}
	if len(docs) != 1 {
		return nil, fmt.Errorf("expected one document, found %d", len(docs))
	}
	return docs[0], nil
}

// ParseAll reads every document in data. Documents are separated by a line
// holding only three dashes. A document with nothing in it is skipped.
func ParseAll(data []byte) ([]*Node, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("the file is not valid UTF-8")
	}
	var docs []*Node
	var current []line
	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		p := &parser{lines: current}
		current = nil
		if p.lines[0].indent != 0 {
			return fmt.Errorf("line %d: a document must start at the left margin", p.lines[0].num)
		}
		n, err := p.block(0)
		if err != nil {
			return err
		}
		if p.pos < len(p.lines) {
			return fmt.Errorf("line %d: unexpected indentation", p.lines[p.pos].num)
		}
		docs = append(docs, n)
		return nil
	}
	for i, raw := range strings.Split(string(data), "\n") {
		num := i + 1
		if strings.ContainsRune(raw, '\r') {
			return nil, fmt.Errorf("line %d: carriage returns are not allowed", num)
		}
		if raw == "---" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		text, err := stripComment(raw, num)
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimLeft(text, " ")
		if trimmed == "" {
			continue
		}
		if trimmed[0] == '\t' {
			return nil, fmt.Errorf("line %d: indent with spaces, not tabs", num)
		}
		if trimmed == "..." || strings.HasPrefix(trimmed, "--- ") || strings.HasPrefix(trimmed, "%") {
			return nil, fmt.Errorf("line %d: document markers and directives other than a bare --- are not supported", num)
		}
		current = append(current, line{indent: len(text) - len(trimmed), text: trimmed, num: num})
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return docs, nil
}

// stripComment removes a comment and trailing spaces from a line. A comment
// starts at a # that begins the line or follows a space, outside quotes.
func stripComment(s string, num int) (string, error) {
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			// A quote only opens a string where a value can start.
			if i == 0 || strings.ContainsRune(" [{,:-", rune(s[i-1])) {
				quote = c
			}
		case c == '#' && (i == 0 || s[i-1] == ' '):
			return strings.TrimRight(s[:i], " "), nil
		}
	}
	if quote != 0 {
		return "", fmt.Errorf("line %d: a quoted string is not closed on its line", num)
	}
	return strings.TrimRight(s, " "), nil
}

// block parses the mapping or list whose lines sit at exactly indent.
func (p *parser) block(indent int) (*Node, error) {
	l := p.lines[p.pos]
	if isItem(l.text) {
		return p.sequence(indent)
	}
	return p.mapping(indent)
}

func isItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ")
}

func (p *parser) mapping(indent int) (*Node, error) {
	n := &Node{Kind: Map, Line: p.lines[p.pos].num, Map: map[string]*Node{}}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, fmt.Errorf("line %d: unexpected indentation", l.num)
		}
		if isItem(l.text) {
			return nil, fmt.Errorf("line %d: a list item where a key was expected", l.num)
		}
		key, rest, ok, err := splitKey(l.text, l.num)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("line %d: expected \"key: value\"", l.num)
		}
		if _, dup := n.Map[key]; dup {
			return nil, fmt.Errorf("line %d: key %q is already defined", l.num, key)
		}
		p.pos++
		var value *Node
		if rest == "" {
			// The value is the block on the following, deeper lines.
			if p.pos >= len(p.lines) || p.lines[p.pos].indent <= indent {
				return nil, fmt.Errorf("line %d: key %q has no value", l.num, key)
			}
			if value, err = p.block(p.lines[p.pos].indent); err != nil {
				return nil, err
			}
		} else if value, err = inline(rest, l.num); err != nil {
			return nil, err
		}
		n.Keys = append(n.Keys, key)
		n.Map[key] = value
	}
	return n, nil
}

func (p *parser) sequence(indent int) (*Node, error) {
	n := &Node{Kind: Seq, Line: p.lines[p.pos].num}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, fmt.Errorf("line %d: unexpected indentation", l.num)
		}
		if !isItem(l.text) {
			return nil, fmt.Errorf("line %d: a key where a list item was expected", l.num)
		}
		if l.text == "-" {
			return nil, fmt.Errorf("line %d: a list item needs a value on the same line", l.num)
		}
		rest := strings.TrimLeft(l.text[1:], " ")
		itemIndent := indent + (len(l.text) - len(rest))
		if isItem(rest) {
			return nil, fmt.Errorf("line %d: a list directly inside a list is not supported", l.num)
		}
		_, _, isMapping, err := splitKey(rest, l.num)
		if err != nil {
			return nil, err
		}
		var item *Node
		if isMapping && rest[0] != '{' && rest[0] != '[' {
			// "- key: value" opens a mapping whose other keys line up with
			// the first one.
			p.lines[p.pos] = line{indent: itemIndent, text: rest, num: l.num}
			if item, err = p.mapping(itemIndent); err != nil {
				return nil, err
			}
		} else {
			if item, err = inline(rest, l.num); err != nil {
				return nil, err
			}
			p.pos++
			if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				return nil, fmt.Errorf("line %d: unexpected indentation", p.lines[p.pos].num)
			}
		}
		n.Items = append(n.Items, item)
	}
	return n, nil
}

// splitKey splits "key: value" or "key:". ok is false when the text is not
// a mapping entry at all (a plain or quoted scalar, or flow text).
func splitKey(text string, num int) (key, rest string, ok bool, err error) {
	if text[0] == '{' || text[0] == '[' {
		return "", "", false, nil
	}
	if text[0] == '"' || text[0] == '\'' {
		s, end, err := quoted(text, 0, num)
		if err != nil {
			return "", "", false, err
		}
		tail := text[end:]
		if tail == ":" || strings.HasPrefix(tail, ": ") {
			return s, strings.TrimLeft(tail[1:], " "), true, nil
		}
		return "", "", false, nil
	}
	i := strings.Index(text, ": ")
	if i < 0 {
		if !strings.HasSuffix(text, ":") {
			return "", "", false, nil
		}
		i = len(text) - 1
	}
	key = text[:i]
	if key == "" || strings.ContainsAny(key, "{}[],\"'&*!|>%@`#") || strings.HasPrefix(key, "? ") || key == "?" {
		return "", "", false, fmt.Errorf("line %d: %q cannot be a key", num, key)
	}
	return key, strings.TrimLeft(text[i+1:], " "), true, nil
}

// inline parses a value written on one line: flow text or a scalar.
func inline(text string, num int) (*Node, error) {
	if text[0] == '{' || text[0] == '[' {
		f := &flow{s: text, num: num}
		n, err := f.value()
		if err != nil {
			return nil, err
		}
		f.spaces()
		if f.i != len(f.s) {
			return nil, fmt.Errorf("line %d: unexpected text after %q", num, f.s[:f.i])
		}
		return n, nil
	}
	if text[0] == '"' || text[0] == '\'' {
		s, end, err := quoted(text, 0, num)
		if err != nil {
			return nil, err
		}
		if end != len(text) {
			return nil, fmt.Errorf("line %d: unexpected text after a quoted string", num)
		}
		return &Node{Kind: String, Line: num, Str: s}, nil
	}
	return plain(text, num)
}

// quoted reads a quoted string starting at s[start] and returns it with
// the index just past its closing quote.
func quoted(s string, start, num int) (string, int, error) {
	q := s[start]
	var b strings.Builder
	for i := start + 1; i < len(s); i++ {
		c := s[i]
		switch {
		case q == '"' && c == '\\':
			if i+1 >= len(s) || (s[i+1] != '"' && s[i+1] != '\\') {
				return "", 0, fmt.Errorf("line %d: the only escapes allowed in double quotes are \\\" and \\\\", num)
			}
			i++
			b.WriteByte(s[i])
		case q == '\'' && c == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
			b.WriteByte('\'')
		case c == q:
			return b.String(), i + 1, nil
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, fmt.Errorf("line %d: a quoted string is not closed on its line", num)
}

var (
	wholePattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	// Text that some YAML reader would take for a number, though it is not
	// a plain whole number: a fraction, an exponent, a sign, a leading
	// zero, hexadecimal, octal, an underscore.
	numberLike = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9][0-9_]*(\.[0-9]*)?)([eE][-+]?[0-9]+)?$|^[-+]?0[xXoObB][0-9a-fA-F_]+$|^[-+]?\.(inf|Inf|INF|nan|NaN|NAN)$`)
	// Words YAML dialects disagree about.
	ambiguous = map[string]bool{
		"null": true, "Null": true, "NULL": true, "~": true,
		"True": true, "TRUE": true, "False": true, "FALSE": true,
		"yes": true, "Yes": true, "YES": true, "no": true, "No": true, "NO": true,
		"on": true, "On": true, "ON": true, "off": true, "Off": true, "OFF": true,
		"y": true, "Y": true, "n": true, "N": true,
	}
)

// plain reads an unquoted scalar.
func plain(text string, num int) (*Node, error) {
	n := &Node{Line: num}
	switch {
	case text == "true" || text == "false":
		n.Kind, n.Bool = Bool, text == "true"
	case wholePattern.MatchString(text):
		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s is too large", num, text)
		}
		n.Kind, n.Int = Int, v
	case ambiguous[text] || numberLike.MatchString(text):
		return nil, fmt.Errorf("line %d: %q is read differently by different YAML readers; put it in quotes if it is text", num, text)
	case strings.ContainsAny(text[:1], "&*!|>%@`?") && (len(text) == 1 || text[0] != '?' || text[1] == ' '):
		return nil, fmt.Errorf("line %d: anchors, aliases, tags and multi-line text are not supported", num)
	case strings.ContainsAny(text, "\"") || strings.HasPrefix(text, "'"):
		return nil, fmt.Errorf("line %d: a quote in the middle of plain text; quote the whole value", num)
	case strings.Contains(text, ": ") || strings.HasSuffix(text, ":"):
		return nil, fmt.Errorf("line %d: a colon here would start a mapping inside a value; quote the value", num)
	default:
		n.Kind, n.Str = String, text
	}
	return n, nil
}

// flow parses flow text: {a: 1, b: [x, y]}.
type flow struct {
	s   string
	i   int
	num int
}

func (f *flow) spaces() {
	for f.i < len(f.s) && f.s[f.i] == ' ' {
		f.i++
	}
}

func (f *flow) errorf(format string, a ...any) error {
	return fmt.Errorf("line %d: %s", f.num, fmt.Sprintf(format, a...))
}

func (f *flow) value() (*Node, error) {
	f.spaces()
	if f.i >= len(f.s) {
		return nil, f.errorf("a value is missing")
	}
	switch f.s[f.i] {
	case '{':
		return f.mapping()
	case '[':
		return f.sequence()
	case '"', '\'':
		s, end, err := quoted(f.s, f.i, f.num)
		if err != nil {
			return nil, err
		}
		f.i = end
		return &Node{Kind: String, Line: f.num, Str: s}, nil
	}
	start := f.i
	for f.i < len(f.s) && !strings.ContainsRune(",}]", rune(f.s[f.i])) {
		if f.s[f.i] == '{' || f.s[f.i] == '[' {
			return nil, f.errorf("unexpected %q", f.s[f.i])
		}
		f.i++
	}
	text := strings.TrimSpace(f.s[start:f.i])
	if text == "" {
		return nil, f.errorf("a value is missing")
	}
	return plain(text, f.num)
}

func (f *flow) mapping() (*Node, error) {
	n := &Node{Kind: Map, Line: f.num, Map: map[string]*Node{}}
	f.i++ // {
	f.spaces()
	if f.i < len(f.s) && f.s[f.i] == '}' {
		f.i++
		return n, nil
	}
	for {
		f.spaces()
		var key string
		if f.i < len(f.s) && (f.s[f.i] == '"' || f.s[f.i] == '\'') {
			s, end, err := quoted(f.s, f.i, f.num)
			if err != nil {
				return nil, err
			}
			key, f.i = s, end
			f.spaces()
		} else {
			start := f.i
			for f.i < len(f.s) && !strings.ContainsRune(":,{}[]", rune(f.s[f.i])) {
				f.i++
			}
			key = strings.TrimSpace(f.s[start:f.i])
		}
		if key == "" || f.i >= len(f.s) || f.s[f.i] != ':' {
			return nil, f.errorf("expected \"key: value\" inside { }")
		}
		f.i++ // :
		if _, dup := n.Map[key]; dup {
			return nil, f.errorf("key %q is already defined", key)
		}
		v, err := f.value()
		if err != nil {
			return nil, err
		}
		n.Keys = append(n.Keys, key)
		n.Map[key] = v
		f.spaces()
		if f.i >= len(f.s) {
			return nil, f.errorf("{ is not closed on its line")
		}
		switch f.s[f.i] {
		case ',':
			f.i++
		case '}':
			f.i++
			return n, nil
		default:
			return nil, f.errorf("expected , or } inside { }")
		}
	}
}

func (f *flow) sequence() (*Node, error) {
	n := &Node{Kind: Seq, Line: f.num}
	f.i++ // [
	f.spaces()
	if f.i < len(f.s) && f.s[f.i] == ']' {
		f.i++
		return n, nil
	}
	for {
		v, err := f.value()
		if err != nil {
			return nil, err
		}
		n.Items = append(n.Items, v)
		f.spaces()
		if f.i >= len(f.s) {
			return nil, f.errorf("[ is not closed on its line")
		}
		switch f.s[f.i] {
		case ',':
			f.i++
		case ']':
			f.i++
			return n, nil
		default:
			return nil, f.errorf("expected , or ] inside [ ]")
		}
	}
}

// Value returns the node as plain Go values: map[string]any, []any,
// string, int or bool. It is for tests and tools that walk a document
// without knowing its shape in advance.
func (n *Node) Value() any {
	switch n.Kind {
	case Map:
		m := make(map[string]any, len(n.Keys))
		for _, k := range n.Keys {
			m[k] = n.Map[k].Value()
		}
		return m
	case Seq:
		l := make([]any, 0, len(n.Items))
		for _, item := range n.Items {
			l = append(l, item.Value())
		}
		return l
	case String:
		return n.Str
	case Int:
		return int(n.Int)
	case Bool:
		return n.Bool
	}
	return nil
}
