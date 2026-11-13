// Package contract holds the v1 wire shapes Assay reads and writes: the
// lease Muster hands out, and the outcome Assay hands back.
//
// The envelope of a lease is Muster's contract v1. The part Assay scores,
// the result Sextant left on the item, is this repository's proposal: see
// "Proposed changes" in SPEC.md.
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
	// Version is the only contract version this build speaks.
	Version = "v1"
	// StateObserved is the only state Assay accepts an item in.
	StateObserved = "observed"
	// KindItem is the only kind of lease Assay accepts.
	KindItem = "item"
	// Observer is the worker whose result Assay scores.
	Observer = "sextant"
	// MaxLeaseBytes bounds one lease reply. Anything larger is refused
	// before it is parsed, which keeps memory use flat.
	MaxLeaseBytes = 1 << 20
	// MaxRequestBytes is the largest request body Muster accepts.
	MaxRequestBytes = 64 << 10
	// maxDepth bounds nesting. Results left by other workers may nest; the
	// facts Assay reads may not, and that is checked separately.
	maxDepth = 32
)

var (
	// The form Muster gives item identifiers: PA-0001 up to PA-999999999.
	itemIDPattern   = regexp.MustCompile(`^PA-([0-9]{4}|[1-9][0-9]{4,8})$`)
	leaseIDPattern  = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)
	categoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// A timestamp such as 2026-01-05T09:01:00Z. Assay never reads the
	// clock, so the value is only checked for shape.
	timePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-][0-9]{2}:[0-9]{2})$`)
	lanes       = map[string]bool{"urgent": true, "normal": true, "backfill": true}
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
// strings. Nothing else is allowed among the facts.
type Value struct {
	Kind Kind
	Bool bool
	Int  int64
	Str  string
	List []string
}

// Record is one leased item, reduced to what the rules read.
type Record struct {
	Lease    string
	ItemID   string
	Category string
	Facts    map[string]Value
}

// ParseLease reads one lease reply strictly.
//
// A lease reply is the JSON object Muster answers POST /v1/lease with:
// lease, kind, id, state, lane, attempt, expires and results. Assay scores
// results.sextant, which must hold a category and a flat map of facts.
// Results left by other workers are ignored.
//
// Broken data gives E_PARSE: bad JSON, a repeated key, a missing or
// mistyped field. Well-formed data this build does not recognise gives
// E_NEEDS_SUBTOOL: a field nobody declared, or an observation that names no
// category, so that no profile can match it. When a reply is both, E_PARSE
// is reported, because "valid and unfamiliar" only makes sense once the
// data is valid.
//
// The returned Record is filled as far as parsing got, so that a failure
// can still be reported against the right lease.
func ParseLease(data []byte) (*Record, *errs.Error) {
	rec := &Record{}
	if len(data) > MaxLeaseBytes {
		return rec, errs.New(errs.Parse, errs.TooLarge, "record")
	}
	root, reason := parseTree(data)
	if reason != "" {
		return rec, errs.New(errs.Parse, reason, "record")
	}
	if root.kind != nodeObject {
		return rec, errs.New(errs.Parse, errs.NotAnObject, "record")
	}

	lease, perr := stringField(root, "lease")
	if perr != nil {
		return rec, perr
	}
	if !leaseIDPattern.MatchString(lease) {
		return rec, errs.New(errs.Parse, errs.InvalidValue, "lease")
	}
	rec.Lease = lease

	itemID, perr := stringField(root, "id")
	if perr != nil {
		return rec, perr
	}
	if !itemIDPattern.MatchString(itemID) {
		return rec, errs.New(errs.Parse, errs.InvalidValue, "id")
	}
	rec.ItemID = itemID

	for _, f := range []struct {
		name string
		ok   func(string) bool
	}{
		{"kind", func(v string) bool { return v == KindItem }},
		{"state", func(v string) bool { return v == StateObserved }},
		{"lane", func(v string) bool { return lanes[v] }},
		{"expires", timePattern.MatchString},
	} {
		v, perr := stringField(root, f.name)
		if perr != nil {
			return rec, perr
		}
		if !f.ok(v) {
			return rec, errs.New(errs.Parse, errs.InvalidValue, f.name)
		}
	}

	attempt, ok := root.obj["attempt"]
	if !ok {
		return rec, errs.New(errs.Parse, errs.MissingField, "attempt")
	}
	if n, isInt := toValue(attempt); !isInt || n.Kind != KindInt {
		return rec, errs.New(errs.Parse, errs.InvalidType, "attempt")
	} else if n.Int < 1 {
		return rec, errs.New(errs.Parse, errs.InvalidValue, "attempt")
	}

	results, ok := root.obj["results"]
	if !ok {
		return rec, errs.New(errs.Parse, errs.MissingField, "results")
	}
	if results.kind != nodeObject {
		return rec, errs.New(errs.Parse, errs.InvalidType, "results")
	}
	observed, ok := results.obj[Observer]
	if !ok {
		return rec, errs.New(errs.Parse, errs.MissingField, Observer)
	}
	if observed.kind != nodeObject {
		return rec, errs.New(errs.Parse, errs.InvalidType, Observer)
	}

	categoryNode, hasCategory := observed.obj["category"]
	if hasCategory {
		if categoryNode.kind != nodeString {
			return rec, errs.New(errs.Parse, errs.InvalidType, "category")
		}
		if !categoryPattern.MatchString(categoryNode.str) {
			return rec, errs.New(errs.Parse, errs.InvalidValue, "category")
		}
		rec.Category = categoryNode.str
	}

	factsNode, hasFacts := observed.obj["facts"]
	if hasFacts {
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
	} else if hasCategory {
		return rec, errs.New(errs.Parse, errs.MissingField, "facts")
	}

	// Everything that is there is sound. Now the unfamiliar.
	//
	// An observation with no category is one no profile can match. It is
	// paused, not refused: a later build of Assay may have a rule for it,
	// and Muster replays paused items when a new build is deployed.
	if !hasCategory {
		return rec, errs.New(errs.NeedsSubtool, errs.UnknownCategory, "category")
	}
	if name := firstUnknown(root, "lease", "kind", "id", "state", "lane", "attempt", "expires", "results"); name != "" {
		return rec, errs.New(errs.NeedsSubtool, errs.UnknownField, "record", name)
	}
	if name := firstUnknown(observed, "category", "facts"); name != "" {
		return rec, errs.New(errs.NeedsSubtool, errs.UnknownField, Observer, name)
	}
	return rec, nil
}

// LeaseID reads only the lease identifier from a lease reply. It is what
// the hub client needs in order to answer for a lease whose contents may
// turn out to be broken.
func LeaseID(data []byte) (string, bool) {
	if len(data) > MaxLeaseBytes {
		return "", false
	}
	root, reason := parseTree(data)
	if reason != "" || root.kind != nodeObject {
		return "", false
	}
	n, ok := root.obj["lease"]
	if !ok || n.kind != nodeString || !leaseIDPattern.MatchString(n.str) {
		return "", false
	}
	return n.str, true
}

// firstUnknown returns the first key of n, in name order, that is not one
// of the known names, or "" when there is none.
func firstUnknown(n node, known ...string) string {
	var unknown []string
	for _, key := range n.keys {
		found := false
		for _, k := range known {
			found = found || k == key
		}
		if !found {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	sort.Strings(unknown)
	return unknown[0]
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
		if depth > maxDepth {
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
