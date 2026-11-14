package contract

import (
	"bytes"
	"encoding/json"
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

// Outcome is what Assay decides for one item. Everything in it is meant to
// be public.
//
// An outcome that is scored or screened out is sent to Muster inside the
// result of the complete call: see Result. State is empty for an E_PARSE failure: the item
// keeps the state it had, and Assay reports it with the fail call.
type Outcome struct {
	Contract         string         `json:"contract"`
	Worker           string         `json:"worker"`
	ItemID           string         `json:"id,omitempty"`
	State            string         `json:"state,omitempty"`
	Category         string         `json:"category,omitempty"`
	Rules            *RulesRef      `json:"rules,omitempty"`
	Gates            []GateResult   `json:"gates,omitempty"`
	Factors          []FactorResult `json:"factors,omitempty"`
	Total            *int           `json:"total,omitempty"`
	Recommendation   string         `json:"recommendation,omitempty"`
	SplitMergeReview *bool          `json:"split_merge_review,omitempty"`
	TieBreakers      []TieBreaker   `json:"tie_breakers,omitempty"`
	Error            *errs.Error    `json:"error,omitempty"`
}

// RulesRef names the rule data that decided the outcome.
type RulesRef struct {
	Rubric         int    `json:"rubric_version"`
	Profile        string `json:"profile"`
	ProfileVersion int    `json:"profile_version"`
}

// GateResult is one knockout gate's verdict. Rule is the 1-based row of the
// profile's decision table that decided it.
type GateResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Rule   int    `json:"rule"`
	Reason string `json:"reason"`
}

// FactorResult is one rubric factor: a rating from 1 to 5, its weight, and
// rating times weight.
type FactorResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Rating int    `json:"rating"`
	Weight int    `json:"weight"`
	Points int    `json:"points"`
	Rule   int    `json:"rule"`
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
func Encode(o *Outcome) []byte { return encode(o) }

// Result writes the result Assay gives Muster's complete call for an
// outcome that is scored or screened out.
//
// Muster's contract records a result that has gates as an evaluation, and
// Logbook refuses one that is not in its own form: profile, gates and
// factors as objects keyed by name, score, recommendation in lower case,
// and split_flag. A failed gate carries its reason, and an evaluation
// with a failed gate has no factors, score or recommendation. Those
// fields are the top level. The outcome itself, unchanged, is under
// "outcome", so that a later worker is given everything Assay decided.
func Result(o *Outcome) []byte {
	r := struct {
		Profile        string            `json:"profile,omitempty"`
		Gates          map[string]gate   `json:"gates,omitempty"`
		Factors        map[string]factor `json:"factors,omitempty"`
		Score          *int              `json:"score,omitempty"`
		Recommendation string            `json:"recommendation,omitempty"`
		SplitFlag      bool              `json:"split_flag,omitempty"`
		Outcome        *Outcome          `json:"outcome"`
	}{
		Score:          o.Total,
		Recommendation: strings.ToLower(o.Recommendation),
		SplitFlag:      o.SplitMergeReview != nil && *o.SplitMergeReview,
		Outcome:        o,
	}
	if o.Rules != nil {
		r.Profile = o.Rules.Profile
	}
	if len(o.Gates) > 0 {
		r.Gates = make(map[string]gate, len(o.Gates))
	}
	for _, g := range o.Gates {
		if g.Pass {
			r.Gates[g.ID] = gate{Passed: true}
		} else {
			r.Gates[g.ID] = gate{Reason: g.Reason}
		}
	}
	if len(o.Factors) > 0 {
		r.Factors = make(map[string]factor, len(o.Factors))
	}
	for _, f := range o.Factors {
		r.Factors[f.ID] = factor{Rating: f.Rating, Weight: f.Weight}
	}
	return encode(r)
}

// gate and factor are one entry each of an evaluation as Logbook keeps it.
type gate struct {
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

type factor struct {
	Rating int `json:"rating"`
	Weight int `json:"weight"`
}

func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		// An Outcome holds only strings, numbers and booleans, so this
		// cannot happen; refusing to continue beats sending half a result.
		panic("contract: outcome could not be encoded")
	}
	return buf.Bytes()
}
