package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/ClearedStack/assay/internal/errs"
)

// Worker is this component's name in the naming registry.
const Worker = "assay"

// Outbound states, spelled as Muster's contract spells them.
const (
	StateScreenedOut  = "screened_out"
	StateScored       = "scored"
	StateNeedsSubtool = "needs_subtool"
)

// Patterns and known values from Logbook's 0002_refusals.sql and
// 0003_working_tables.sql, used to check evaluations in the same way
// Logbook checks rows of core.evaluation.
var (
	slugPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*([._-][a-z0-9]+)*$`)
	knownPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	knownRecs    = map[string]bool{
		"build":  true,
		"link":   true,
		"park":   true,
		"reject": true,
	}
)

// Outcome is what Assay decides for one item. Everything in it is meant to
// be public.
//
// An outcome that is scored or screened out is sent to Muster whole, as the
// result of the complete call. Its evaluation fields (profile, gates,
// factors, score, recommendation and split_flag) have the exact shape
// Muster's contract v1 and Logbook's core.evaluation require. State is
// empty for an E_PARSE failure: the item keeps the state it had, and Assay
// reports it with the fail call.
type Outcome struct {
	Contract       string        `json:"contract"`
	Worker         string        `json:"worker"`
	ItemID         string        `json:"id,omitempty"`
	State          string        `json:"state,omitempty"`
	Category       string        `json:"category,omitempty"`
	Profile        string        `json:"profile,omitempty"`
	Rules          *RulesRef     `json:"rules,omitempty"`
	Gates          GateVerdicts  `json:"gates,omitempty"`
	Factors        FactorRatings `json:"factors,omitempty"`
	Score          *int          `json:"score,omitempty"`
	Recommendation string        `json:"recommendation,omitempty"`
	SplitFlag      *bool         `json:"split_flag,omitempty"`
	TieBreakers    []TieBreaker  `json:"tie_breakers,omitempty"`
	Error          *errs.Error   `json:"error,omitempty"`
}

// RulesRef names the rule data that decided the outcome, including the
// 1-based row of each decision table that matched. Keeping the row numbers
// here leaves gates and factors with only the fields Logbook's
// core.evaluation accepts, while every published verdict can still be traced
// to one line of rule data.
type RulesRef struct {
	Rubric         int      `json:"rubric_version"`
	Profile        string   `json:"profile"`
	ProfileVersion int      `json:"profile_version"`
	Gates          RuleRows `json:"gates,omitempty"`
	Factors        RuleRows `json:"factors,omitempty"`
}

// RuleRow records the 1-based row of a gate or factor table that matched.
type RuleRow struct {
	ID   string
	Rule int
}

// RuleRows marshals as a JSON object in slice order: {"gate_id": 1, ...}.
type RuleRows []RuleRow

// MarshalJSON writes the rule map in deterministic slice order.
func (rr RuleRows) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, r := range rr {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := marshalRaw(r.ID)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.WriteString(strconv.Itoa(r.Rule))
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON reads a rule map while keeping key order.
func (rr *RuleRows) UnmarshalJSON(data []byte) error {
	var out RuleRows
	err := readOrderedObject(data, func(key string, raw json.RawMessage) error {
		var rule int
		if err := json.Unmarshal(raw, &rule); err != nil {
			return err
		}
		out = append(out, RuleRow{ID: key, Rule: rule})
		return nil
	})
	if err != nil {
		return err
	}
	*rr = out
	return nil
}

// GateResult is one knockout gate's verdict. Rule is the 1-based row of the
// profile's decision table that decided it.
type GateResult struct {
	ID     string
	Name   string
	Passed bool
	Rule   int
	Reason string
}

// GateVerdicts is the ordered list of gate verdicts. On the wire it is a
// JSON object keyed by gate slug, holding passed and reason and no other
// field, as Logbook's core.evaluation requires.
type GateVerdicts []GateResult

type gateWire struct {
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

// MarshalJSON writes the gate verdicts as an ordered JSON object.
func (gv GateVerdicts) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, g := range gv {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := marshalRaw(g.ID)
		if err != nil {
			return nil, err
		}
		val, err := marshalRaw(gateWire{Passed: g.Passed, Reason: g.Reason})
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON reads gate verdicts from a JSON object in key order.
func (gv *GateVerdicts) UnmarshalJSON(data []byte) error {
	var out GateVerdicts
	err := readOrderedObject(data, func(key string, raw json.RawMessage) error {
		var w gateWire
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&w); err != nil {
			return err
		}
		out = append(out, GateResult{ID: key, Passed: w.Passed, Reason: w.Reason})
		return nil
	})
	if err != nil {
		return err
	}
	*gv = out
	return nil
}

// FactorResult is one rubric factor: a rating from 1 to 5, its weight, and
// rating times weight.
type FactorResult struct {
	ID     string
	Name   string
	Rating int
	Weight int
	Points int
	Rule   int
}

// FactorRatings is the ordered list of factor ratings. On the wire it is a
// JSON object keyed by factor slug, holding rating and weight and no other
// field, as Logbook's core.evaluation requires.
type FactorRatings []FactorResult

type factorWire struct {
	Rating int `json:"rating"`
	Weight int `json:"weight"`
}

// MarshalJSON writes the factor ratings as an ordered JSON object.
func (fr FactorRatings) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range fr {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := marshalRaw(f.ID)
		if err != nil {
			return nil, err
		}
		val, err := marshalRaw(factorWire{Rating: f.Rating, Weight: f.Weight})
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON reads factor ratings from a JSON object in key order.
func (fr *FactorRatings) UnmarshalJSON(data []byte) error {
	var out FactorRatings
	err := readOrderedObject(data, func(key string, raw json.RawMessage) error {
		var w factorWire
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&w); err != nil {
			return err
		}
		out = append(out, FactorResult{ID: key, Rating: w.Rating, Weight: w.Weight, Points: w.Rating * w.Weight})
		return nil
	})
	if err != nil {
		return err
	}
	*fr = out
	return nil
}

// UnmarshalJSON decodes an Outcome and restores Rule on each gate and
// factor from Rules when present.
func (o *Outcome) UnmarshalJSON(data []byte) error {
	type plain Outcome
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*o = Outcome(p)
	if o.Rules != nil {
		gateRules := make(map[string]int, len(o.Rules.Gates))
		for _, r := range o.Rules.Gates {
			gateRules[r.ID] = r.Rule
		}
		for i := range o.Gates {
			o.Gates[i].Rule = gateRules[o.Gates[i].ID]
		}
		factorRules := make(map[string]int, len(o.Rules.Factors))
		for _, r := range o.Rules.Factors {
			factorRules[r.ID] = r.Rule
		}
		for i := range o.Factors {
			o.Factors[i].Rule = factorRules[o.Factors[i].ID]
		}
	}
	return nil
}

// TieBreaker is one of the ordered tie-breaker values. A higher value sorts
// first.
type TieBreaker struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// Encode writes an outcome as indented JSON with a trailing newline. The
// bytes are the same on every machine for the same outcome.
func Encode(o *Outcome) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(o); err != nil {
		// An Outcome holds only strings, numbers and booleans, so this
		// cannot happen; refusing to continue beats sending half a result.
		panic("contract: outcome could not be encoded")
	}
	return buf.Bytes()
}

func marshalRaw(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func readOrderedObject(data []byte, each func(key string, raw json.RawMessage) error) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return errors.New("expected JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := kt.(string)
		if !ok || seen[key] {
			return errors.New("invalid or duplicate object key")
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if err := each(key, raw); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// HasGates reports whether result is a JSON object with a non-null "gates"
// field, which is when Muster records an Assay result as a row of
// core.evaluation.
func HasGates(result []byte) bool {
	root, reason := parseTree(result)
	if reason != "" || root.kind != nodeObject {
		return false
	}
	g, ok := root.obj["gates"]
	return ok && g.kind != nodeNull
}

// CheckEvaluation checks an Assay result against the rules Muster's
// tx.Evaluation and Logbook's core.check_evaluation enforce. It returns nil
// when the evaluation is accepted, an E_PARSE error when Logbook refuses a
// field, and an E_NEEDS_SUBTOOL error when Logbook has no rule for a
// well-formed recommendation value.
func CheckEvaluation(result []byte) *errs.Error {
	bad := func(where string) *errs.Error {
		return errs.New(errs.Parse, errs.InvalidValue, where)
	}
	root, reason := parseTree(result)
	if reason != "" || root.kind != nodeObject {
		return bad("evaluation")
	}

	var e struct {
		Profile        string          `json:"profile"`
		Gates          json.RawMessage `json:"gates"`
		Factors        json.RawMessage `json:"factors"`
		Score          *int            `json:"score"`
		Recommendation *string         `json:"recommendation"`
		SplitFlag      bool            `json:"split_flag"`
	}
	if err := json.Unmarshal(result, &e); err != nil || e.Gates == nil {
		return bad("evaluation")
	}
	// Whole numbers only for score: encoding/json would already reject 26.5
	// into *int, and parseTree rejects exponent or fraction if checked.
	if sNode, ok := root.obj["score"]; ok && sNode.kind != nodeNull {
		if v, ok := toValue(sNode); !ok || v.Kind != KindInt {
			return bad("score")
		}
	}

	if !ValidSlug(e.Profile) {
		return bad("profile")
	}

	gatesNode, ok := root.obj["gates"]
	if !ok || gatesNode.kind != nodeObject || len(gatesNode.keys) == 0 {
		return bad("gates")
	}
	gateFailed := false
	for _, name := range gatesNode.keys {
		if !ValidSlug(name) {
			return bad("gates")
		}
		entry := gatesNode.obj[name]
		if entry.kind != nodeObject {
			return bad("gates")
		}
		passed, hasPassed := entry.obj["passed"]
		if !hasPassed || passed.kind != nodeBool {
			return bad("gates")
		}
		for _, k := range entry.keys {
			if k != "passed" && k != "reason" {
				return bad("gates")
			}
		}
		reasonNode, hasReason := entry.obj["reason"]
		if hasReason && reasonNode.kind != nodeString {
			return bad("gates")
		}
		if !passed.b {
			gateFailed = true
			if !hasReason || !ValidLogbookText(reasonNode.str, 1000) {
				return bad("gates")
			}
		} else if hasReason && !ValidLogbookText(reasonNode.str, 1000) {
			return bad("gates")
		}
	}

	factorsNode, hasFactors := root.obj["factors"]
	if !hasFactors {
		factorsNode = node{kind: nodeObject}
	}
	if factorsNode.kind != nodeObject {
		return bad("factors")
	}

	if gateFailed {
		if len(factorsNode.keys) != 0 || e.Score != nil || e.Recommendation != nil || e.SplitFlag {
			return bad("evaluation")
		}
		return nil
	}

	if len(factorsNode.keys) == 0 {
		return bad("factors")
	}
	total := 0
	for _, name := range factorsNode.keys {
		if !ValidSlug(name) {
			return bad("factors")
		}
		entry := factorsNode.obj[name]
		if entry.kind != nodeObject || len(entry.keys) != 2 {
			return bad("factors")
		}
		rNode, hasRating := entry.obj["rating"]
		wNode, hasWeight := entry.obj["weight"]
		if !hasRating || !hasWeight {
			return bad("factors")
		}
		rv, rok := toValue(rNode)
		wv, wok := toValue(wNode)
		if !rok || rv.Kind != KindInt || rv.Int < 1 || rv.Int > 5 {
			return bad("factors")
		}
		if !wok || wv.Kind != KindInt || wv.Int < 1 || wv.Int > 100 {
			return bad("factors")
		}
		total += int(rv.Int) * int(wv.Int)
	}

	if e.Score == nil || *e.Score != total {
		return bad("score")
	}
	if e.Recommendation == nil || !knownPattern.MatchString(*e.Recommendation) {
		return bad("recommendation")
	}
	if !knownRecs[*e.Recommendation] {
		return errs.New(errs.NeedsSubtool, errs.UnknownValue, "recommendation")
	}
	return nil
}

// ValidSlug reports whether s matches Logbook's core.require_slug rule.
func ValidSlug(s string) bool {
	return len(s) > 0 && len(s) <= 100 && slugPattern.MatchString(s)
}

// ValidLogbookText reports whether s matches Logbook's core.require_text
// rule with the given byte limit.
func ValidLogbookText(s string, maxBytes int) bool {
	if len(s) == 0 || len(s) > maxBytes || strings.TrimSpace(s) == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0 || (c >= 0x01 && c <= 0x08) || c == 0x0b || c == 0x0c || (c >= 0x0e && c <= 0x1f) || c == 0x7f {
			return false
		}
	}
	return true
}
