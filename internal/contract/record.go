// Package contract holds the v1 wire shapes Assay reads and writes: the
// observed record that comes in and the outcome that goes back.
//
// The v1 record shape is provisional. SPEC.md names CONTRACT_VERSION=v1 but
// does not define it; the definition used here is written up in SPEC.md under
// "Proposed changes".
package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/ClearedStack/assay/internal/errs"
)

const (
	// Version is the only contract version this build understands.
	Version = "v1"
	// StateObserved is the only inbound state Assay accepts.
	StateObserved = "observed"
	// MaxRecordBytes bounds one observed record. Anything larger is refused
	// before it is parsed, which keeps memory use flat.
	MaxRecordBytes = 1 << 20
	// maxDepth is the deepest nesting a valid record can have:
	// record -> facts -> list -> string.
	maxDepth = 4
)

var (
	itemIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	categoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// Kind is the type of one fact value.
type Kind int

const (
	KindBool Kind = iota + 1
	KindInt
	KindString
	KindList
)

// Value is one fact: a boolean, a whole number, a string or a list of
// strings. Nothing else is allowed in a record.
type Value struct {
	Kind Kind
	Bool bool
	Int  int64
	Str  string
	List []string
}

// Record is one observed candidate, reduced to what the rules read.
type Record struct {
	ItemID   string
	Category string
	Facts    map[string]Value
}

// ParseRecord reads one observed record strictly.
//
// Broken data (bad JSON, a repeated key, a missing or mistyped field) gives
// E_PARSE. Well-formed data this build does not recognise (an extra envelope
// field) gives E_NEEDS_SUBTOOL. E_PARSE is always reported in preference,
// because "valid and unfamiliar" only makes sense once the data is valid.
//
// The returned Record is filled as far as parsing got, so that an error can
// still be reported against the right item.
func ParseRecord(data []byte) (*Record, *errs.Error) {
	rec := &Record{}
	if len(data) > MaxRecordBytes {
		return rec, errs.New(errs.Parse, errs.TooLarge, "record")
	}
	root, reason := parseTree(data)
	if reason != "" {
		return rec, errs.New(errs.Parse, reason, "record")
	}
	if root.kind != nodeObject {
		return rec, errs.New(errs.Parse, errs.NotAnObject, "record")
	}

	itemID, perr := stringField(root, "item_id")
	if perr != nil {
		return rec, perr
	}
	if !itemIDPattern.MatchString(itemID) {
		return rec, errs.New(errs.Parse, errs.InvalidValue, "item_id")
	}
	rec.ItemID = itemID

	version, perr := stringField(root, "contract_version")
	if perr != nil {
		return rec, perr
	}
	if version != Version {
		return rec, errs.New(errs.Parse, errs.UnsupportedContract, "contract_version")
	}

	state, perr := stringField(root, "state")
	if perr != nil {
		return rec, perr
	}
	if state != StateObserved {
		return rec, errs.New(errs.Parse, errs.InvalidValue, "state")
	}

	category, perr := stringField(root, "category")
	if perr != nil {
		return rec, perr
	}
	if !categoryPattern.MatchString(category) {
		return rec, errs.New(errs.Parse, errs.InvalidValue, "category")
	}
	rec.Category = category

	factsNode, ok := root.obj["facts"]
	if !ok {
		return rec, errs.New(errs.Parse, errs.MissingField, "facts")
	}
	if factsNode.kind != nodeObject {
		return rec, errs.New(errs.Parse, errs.InvalidType, "facts")
	}
	facts := make(map[string]Value, len(factsNode.keys))
	for _, name := range factsNode.keys {
		v, ok := toValue(factsNode.obj[name])
		if !ok {
			return rec, errs.New(errs.Parse, errs.InvalidType, "facts")
		}
		facts[name] = v
	}
	rec.Facts = facts

	// Last, and only once everything else is sound: fields nobody declared.
	var unknown []string
	for _, key := range root.keys {
		switch key {
		case "contract_version", "item_id", "state", "category", "facts":
		default:
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return rec, errs.New(errs.NeedsSubtool, errs.UnknownField, "record", unknown[0])
	}
	return rec, nil
}

func stringField(root node, name string) (string, *errs.Error) {
	n, ok := root.obj[name]
	if !ok {
		return "", errs.New(errs.Parse, errs.MissingField, name)
	}
	if n.kind != nodeString {
		return "", errs.New(errs.Parse, errs.InvalidType, name)
	}
	return n.str, nil
}

func toValue(n node) (Value, bool) {
	switch n.kind {
	case nodeBool:
		return Value{Kind: KindBool, Bool: n.b}, true
	case nodeNumber:
		// Whole numbers only. "1.0" and "1e3" are refused rather than
		// rounded, so no two machines can disagree about a value.
		i, err := strconv.ParseInt(n.str, 10, 64)
		if err != nil {
			return Value{}, false
		}
		return Value{Kind: KindInt, Int: i}, true
	case nodeString:
		return Value{Kind: KindString, Str: n.str}, true
	case nodeArray:
		list := make([]string, 0, len(n.arr))
		for _, el := range n.arr {
			if el.kind != nodeString {
				return Value{}, false
			}
			list = append(list, el.str)
		}
		return Value{Kind: KindList, List: list}, true
	}
	return Value{}, false
}

type nodeKind int

const (
	nodeNull nodeKind = iota
	nodeBool
	nodeNumber
	nodeString
	nodeArray
	nodeObject
)

// node is a parsed JSON value. Unlike a plain map it remembers that a key
// appeared twice, which encoding/json would hide.
type node struct {
	kind nodeKind
	b    bool
	str  string // string value, or the literal text of a number
	arr  []node
	keys []string
	obj  map[string]node
}

// parseTree parses data into a tree. It returns a reason when the data is
// not one well-formed UTF-8 JSON value, repeats a key, or nests too deeply.
func parseTree(data []byte) (node, errs.Reason) {
	if !utf8.Valid(data) || !json.Valid(data) {
		return node{}, errs.MalformedJSON
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, reason := readValue(dec, 1)
	if reason != "" {
		return node{}, reason
	}
	if _, err := dec.Token(); err != io.EOF {
		return node{}, errs.MalformedJSON
	}
	return n, ""
}

func readValue(dec *json.Decoder, depth int) (node, errs.Reason) {
	tok, err := dec.Token()
	if err != nil {
		return node{}, errs.MalformedJSON
	}
	switch t := tok.(type) {
	case nil:
		return node{kind: nodeNull}, ""
	case bool:
		return node{kind: nodeBool, b: t}, ""
	case json.Number:
		return node{kind: nodeNumber, str: t.String()}, ""
	case string:
		return node{kind: nodeString, str: t}, ""
	case json.Delim:
		if depth >= maxDepth {
			return node{}, errs.InvalidType
		}
		switch t {
		case '[':
			n := node{kind: nodeArray}
			for dec.More() {
				el, reason := readValue(dec, depth+1)
				if reason != "" {
					return node{}, reason
				}
				n.arr = append(n.arr, el)
			}
			if _, err := dec.Token(); err != nil {
				return node{}, errs.MalformedJSON
			}
			return n, ""
		case '{':
			n := node{kind: nodeObject, obj: map[string]node{}}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return node{}, errs.MalformedJSON
				}
				key, ok := keyTok.(string)
				if !ok {
					return node{}, errs.MalformedJSON
				}
				if _, dup := n.obj[key]; dup {
					return node{}, errs.DuplicateKey
				}
				val, reason := readValue(dec, depth+1)
				if reason != "" {
					return node{}, reason
				}
				n.keys = append(n.keys, key)
				n.obj[key] = val
			}
			if _, err := dec.Token(); err != nil {
				return node{}, errs.MalformedJSON
			}
			return n, ""
		}
	}
	return node{}, errs.MalformedJSON
}
