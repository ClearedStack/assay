package yamlite

import (
	"encoding/json"
	"strings"
	"testing"
)

// asJSON renders a parsed document so that a test can state what it
// expects in one line.
func asJSON(t *testing.T, n *Node) string {
	t.Helper()
	data, err := json.Marshal(n.Value())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"scalars", "a: 1\nb: -20\nc: true\nd: false\ne: text\nf: \"quoted: text # not a comment\"\ng: 'it''s'\n",
			`{"a":1,"b":-20,"c":true,"d":false,"e":"text","f":"quoted: text # not a comment","g":"it's"}`},
		{"quoted words stay text", "a: \"true\"\nb: '12'\nc: \"no\"\n", `{"a":"true","b":"12","c":"no"}`},
		{"comments and blank lines", "# top\n\na: 1  # trailing\n\n  # indented comment\nb: 2\n", `{"a":1,"b":2}`},
		{"nested mappings", "a:\n  b:\n    c: 1\n  d: 2\ne: 3\n", `{"a":{"b":{"c":1},"d":2},"e":3}`},
		{"list of scalars", "a:\n  - x\n  - -trimpath\n  - CGO_ENABLED=0\n  - \"- quoted\"\n", `{"a":["x","-trimpath","CGO_ENABLED=0","- quoted"]}`},
		{"list of mappings", "a:\n  - name: x\n    value: 1\n  - name: w\n    nested:\n      k: v\n", `{"a":[{"name":"x","value":1},{"name":"w","nested":{"k":"v"}}]}`},
		{"list inside a list item", "a:\n  - to:\n      - x: 1\n        y: 2\n    ports:\n      - 80\n", `{"a":[{"ports":[80],"to":[{"x":1,"y":2}]}]}`},
		{"flow mapping", "a: {x: 1, y: {z: [p, q]}, \"k k\": 'v, w'}\n", `{"a":{"k k":"v, w","x":1,"y":{"z":["p","q"]}}}`},
		{"flow list", "a: [x, 2, true, \"a, b\", {k: v}]\nb: []\nc: {}\n", `{"a":["x",2,true,"a, b",{"k":"v"}],"b":[],"c":{}}`},
		{"list of flow mappings", "a:\n  - {id: x, name: \"A, b\"}\n  - {id: w, name: Plain}\n", `{"a":[{"id":"x","name":"A, b"},{"id":"w","name":"Plain"}]}`},
		{"values with colons and slashes", "url: http://muster.example:8080/path\nimage: ko://github.com/x/y\nkey.with/dots: v0.1.0-draft\nsize: 30Mi\n",
			`{"image":"ko://github.com/x/y","key.with/dots":"v0.1.0-draft","size":"30Mi","url":"http://muster.example:8080/path"}`},
		{"apostrophe in plain and quoted text", "a: don't\nb: \"connect-src 'self'.\"\n", `{"a":"don't","b":"connect-src 'self'."}`},
		{"escapes", `a: "say \"hi\" \\ bye"` + "\n", `{"a":"say \"hi\" \\ bye"}`},
		{"top-level list", "- a\n- b: 1\n", `["a",{"b":1}]`},
		{"quoted key", "\"a b\": 1\n'c': 2\n", `{"a b":1,"c":2}`},
		{"hash that is not a comment", "a: x#y\nb: http://x/#frag\n", `{"a":"x#y","b":"http://x/#frag"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := Parse([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := asJSON(t, n); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestParseRefusesWhatIsOutsideTheSubset(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string // part of the error
	}{
		"duplicate key":                  {"a: 1\na: 2\n", "already defined"},
		"duplicate key in flow":          {"a: {x: 1, x: 2}\n", "already defined"},
		"key with no value":              {"a:\nb: 1\n", "has no value"},
		"last key with no value":         {"a:\n", "has no value"},
		"null":                           {"a: null\n", "quotes"},
		"tilde":                          {"a: ~\n", "quotes"},
		"yes":                            {"a: yes\n", "quotes"},
		"off":                            {"a: off\n", "quotes"},
		"capital True":                   {"a: True\n", "quotes"},
		"fraction":                       {"a: 1.5\n", "quotes"},
		"exponent":                       {"a: 1e3\n", "quotes"},
		"leading zero":                   {"a: 010\n", "quotes"},
		"plus sign":                      {"a: +1\n", "quotes"},
		"hexadecimal":                    {"a: 0x1F\n", "quotes"},
		"underscore in a number":         {"a: 1_000\n", "quotes"},
		"number too large":               {"a: 99999999999999999999\n", "too large"},
		"ambiguous word in a flow list":  {"a: [yes, no]\n", "quotes"},
		"anchor":                         {"a: &x 1\n", "not supported"},
		"alias":                          {"a: *x\n", "not supported"},
		"tag":                            {"a: !!str 1\n", "not supported"},
		"literal block":                  {"a: |\n  text\n", "not supported"},
		"folded block":                   {"a: >-\n  text\n", "not supported"},
		"tab indentation":                {"a:\n\tb: 1\n", "tabs"},
		"carriage return":                {"a: 1\r\nb: 2\r\n", "carriage"},
		"indented too far":               {"a: 1\n  b: 2\n", "indentation"},
		"sibling indented too far":       {"a:\n  b: 1\n    c: 2\n", "indentation"},
		"indented start":                 {"  a: 1\n", "left margin"},
		"list where a key was expected":  {"a: 1\n- b\n", "list item"},
		"key where an item was expected": {"- a\nb: 1\n", "list item was expected"},
		"bare dash":                      {"a:\n  -\n  - b\n", "needs a value"},
		"list in a list":                 {"a:\n  - - b\n", "not supported"},
		"text under a scalar item":       {"a:\n  - b\n      c\n", "indentation"},
		"not a mapping":                  {"just text\n", "key: value"},
		"colon in a plain value":         {"a: b: c\n", "colon"},
		"unclosed double quote":          {"a: \"text\n", "not closed"},
		"unclosed single quote":          {"a: 'text\n", "not closed"},
		"text after a quoted string":     {"a: \"x\" y\n", "after a quoted string"},
		"unknown escape":                 {`a: "line\nbreak"` + "\n", "escapes"},
		"quote inside plain text":        {"a: say \"hi\"\n", "quote"},
		"unclosed flow mapping":          {"a: {x: 1\n", "not closed"},
		"unclosed flow list":             {"a: [x, w\n", "not closed"},
		"flow over two lines":            {"a: [x,\n  w]\n", "missing"},
		"flow without a key":             {"a: {x}\n", "key: value"},
		"empty flow item":                {"a: [x, , y]\n", "missing"},
		"text after flow":                {"a: [x] y\n", "after"},
		"brace inside flow text":         {"a: [x{y]\n", "unexpected"},
		"document end marker":            {"a: 1\n...\n", "not supported"},
		"directive":                      {"%YAML 1.2\na: 1\n", "not supported"},
		"complex key":                    {"? a\n: b\n", ""},
		"invalid UTF-8":                  {"a: \xff\n", "UTF-8"},
		"two documents":                  {"a: 1\n---\nb: 2\n", "one document"},
		"nothing at all":                 {"# only a comment\n", "one document"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			n, err := Parse([]byte(tc.in))
			if err == nil {
				t.Fatalf("accepted, as %s", asJSON(t, n))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestErrorsNameTheLine(t *testing.T) {
	_, err := Parse([]byte("a: 1\nb:\n  c: 2\n  c: 3\n"))
	if err == nil || !strings.Contains(err.Error(), "line 4") {
		t.Errorf("got %v", err)
	}
}

func TestParseAll(t *testing.T) {
	docs, err := ParseAll([]byte("# leading comment\na: 1\n---\n# an empty document is skipped\n---\nb:\n  - 2\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || asJSON(t, docs[0]) != `{"a":1}` || asJSON(t, docs[1]) != `{"b":[2]}` {
		t.Errorf("unexpected documents: %d", len(docs))
	}
}

func TestKeysKeepTheirOrder(t *testing.T) {
	n, err := Parse([]byte("zeta: 1\nalpha: 2\nmid: {y: 1, x: 2}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(n.Keys, ",") != "zeta,alpha,mid" || strings.Join(n.Map["mid"].Keys, ",") != "y,x" {
		t.Errorf("keys out of order: %v, %v", n.Keys, n.Map["mid"].Keys)
	}
}

func TestReader(t *testing.T) {
	n, err := Parse([]byte("name: x\ncount: 3\nflag: true\nlist: [a, b]\nempty: []\nitems:\n  - 1\nsub: {k: v}\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := n.Read("name", "count", "flag", "list", "empty", "items", "sub", "absent")
	if r.String("name") != "x" || r.Int("count") != 3 || !*r.OptBool("flag") || len(r.OptStrings("list")) != 2 || len(r.List("items")) != 1 {
		t.Error("values read wrongly")
	}
	if e := r.OptStrings("empty"); e == nil || len(e) != 0 {
		t.Error("an empty list must be empty, not absent")
	}
	if r.OptStrings("absent") != nil || r.OptInt("absent") != nil || r.OptBool("absent") != nil || r.OptString("absent") != "" || r.OptNode("absent") != nil || r.Has("absent") {
		t.Error("an absent key must read as absent")
	}
	keys, values := r.Map("sub")
	if len(keys) != 1 || values["k"].Str != "v" || !r.Has("sub") {
		t.Error("mapping read wrongly")
	}
	if r.Err() != nil {
		t.Fatalf("unexpected error: %v", r.Err())
	}

	problems := map[string]func(*Reader){
		"unknown key":                  nil,
		"key \"missing\" is missing":   func(r *Reader) { r.String("missing") },
		"must be a string":             func(r *Reader) { r.String("count") },
		"must be a whole number":       func(r *Reader) { r.Int("name") },
		"must be a true or false":      func(r *Reader) { r.OptBool("name") },
		"must be a list":               func(r *Reader) { r.List("name") },
		"must be a mapping":            func(r *Reader) { r.Map("list") },
		"every item of \"items\" must": func(r *Reader) { r.OptStrings("items") },
	}
	for want, read := range problems {
		var r *Reader
		if read == nil {
			r = n.Read("name")
		} else {
			r = n.Read("name", "count", "flag", "list", "empty", "items", "sub", "missing")
			read(r)
		}
		if r.Err() == nil || !strings.Contains(r.Err().Error(), want) {
			t.Errorf("want an error mentioning %q, got %v", want, r.Err())
		}
		// After an error every read is empty and the first error stays.
		first := r.Err()
		if r.String("name") != "" || r.Has("name") || r.Err() != first {
			t.Error("a reader must stop at its first error")
		}
	}
	if err := n.Map["name"].Read().Err(); err == nil || !strings.Contains(err.Error(), "expected a mapping") {
		t.Errorf("reading a scalar as a mapping: %v", err)
	}
}
