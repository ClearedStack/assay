package yamlite

import "fmt"

// Reader reads the fields of one mapping. It remembers the first thing that
// goes wrong, so a caller can read every field and check for an error once.
// A key the caller did not name is an error: nothing in a rule file is
// silently ignored.
type Reader struct {
	n   *Node
	err error
}

// Read starts reading n as a mapping whose keys may only be those allowed.
func (n *Node) Read(allowed ...string) *Reader {
	r := &Reader{n: n}
	if n.Kind != Map {
		r.err = fmt.Errorf("line %d: expected a mapping, found a %s", n.Line, n.Kind)
		return r
	}
	for _, key := range n.Keys {
		known := false
		for _, a := range allowed {
			known = known || a == key
		}
		if !known {
			r.err = fmt.Errorf("line %d: unknown key %q", n.Map[key].Line, key)
			return r
		}
	}
	return r
}

// Err returns the first error met while reading.
func (r *Reader) Err() error { return r.err }

// Has reports whether the mapping has the key.
func (r *Reader) Has(key string) bool {
	return r.err == nil && r.n.Map[key] != nil
}

func (r *Reader) get(key string, kind Kind, required bool) *Node {
	if r.err != nil {
		return nil
	}
	v := r.n.Map[key]
	if v == nil {
		if required {
			r.err = fmt.Errorf("line %d: key %q is missing", r.n.Line, key)
		}
		return nil
	}
	if kind != 0 && v.Kind != kind {
		r.err = fmt.Errorf("line %d: %q must be a %s, not a %s", v.Line, key, kind, v.Kind)
		return nil
	}
	return v
}

// String returns a required string.
func (r *Reader) String(key string) string {
	if v := r.get(key, String, true); v != nil {
		return v.Str
	}
	return ""
}

// OptString returns a string, or "" when the key is absent.
func (r *Reader) OptString(key string) string {
	if v := r.get(key, String, false); v != nil {
		return v.Str
	}
	return ""
}

// Int returns a required whole number.
func (r *Reader) Int(key string) int64 {
	if v := r.get(key, Int, true); v != nil {
		return v.Int
	}
	return 0
}

// OptInt returns a whole number, or nil when the key is absent.
func (r *Reader) OptInt(key string) *int64 {
	if v := r.get(key, Int, false); v != nil {
		i := v.Int
		return &i
	}
	return nil
}

// OptBool returns true or false, or nil when the key is absent.
func (r *Reader) OptBool(key string) *bool {
	if v := r.get(key, Bool, false); v != nil {
		b := v.Bool
		return &b
	}
	return nil
}

// Node returns a required value of any kind.
func (r *Reader) Node(key string) *Node { return r.get(key, 0, true) }

// OptNode returns a value of any kind, or nil when the key is absent.
func (r *Reader) OptNode(key string) *Node { return r.get(key, 0, false) }

// List returns the items of a required list.
func (r *Reader) List(key string) []*Node {
	if v := r.get(key, Seq, true); v != nil {
		return v.Items
	}
	return nil
}

// OptStrings returns a list of strings. It is nil when the key is absent
// and empty, but not nil, when the list is written as [].
func (r *Reader) OptStrings(key string) []string {
	v := r.get(key, Seq, false)
	if v == nil {
		return nil
	}
	out := make([]string, 0, len(v.Items))
	for _, item := range v.Items {
		if item.Kind != String {
			r.err = fmt.Errorf("line %d: every item of %q must be a string, not a %s; quote it if it is text", item.Line, key, item.Kind)
			return nil
		}
		out = append(out, item.Str)
	}
	return out
}

// Map returns the keys, in the order written, and the values of a required
// mapping.
func (r *Reader) Map(key string) ([]string, map[string]*Node) {
	if v := r.get(key, Map, true); v != nil {
		return v.Keys, v.Map
	}
	return nil, nil
}
