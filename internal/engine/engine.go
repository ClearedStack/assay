// Package engine runs one observed record through the whole evaluation:
// the knockout gates, the rubric, the recommendation, the split/merge flag
// and the tie-breakers.
//
// The engine holds no rules of its own. Every threshold comes from the
// rubric-profiles sub-tool. It uses no clock, no randomness and no network,
// so the same record gives the same outcome, byte for byte, on any machine.
package engine

import (
	"strings"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/errs"
	rubricprofiles "github.com/ClearedStack/assay/tools/rubric-profiles"
)

// Engine evaluates records against a loaded set of rules.
type Engine struct {
	set *rubricprofiles.Set
}

// New returns an engine over the given rules.
func New(set *rubricprofiles.Set) *Engine {
	return &Engine{set: set}
}

// Evaluate reads one lease reply, as Muster sends it, and returns the
// outcome for the item in it. It never fails: broken or unfamiliar input
// becomes an outcome that carries an error.
func (e *Engine) Evaluate(raw []byte) *contract.Outcome {
	out := &contract.Outcome{Contract: contract.Version, Worker: contract.Worker}

	rec, perr := contract.ParseLease(raw)
	out.ItemID = rec.ItemID
	out.Category = rec.Category
	if perr != nil {
		return stop(out, perr)
	}

	profile := e.set.Match(rec.Category)
	if profile == nil {
		return stop(out, errs.New(errs.NeedsSubtool, errs.UnknownCategory, "category", rec.Category))
	}
	rubric := e.set.Rubric
	out.Profile = profile.ID
	out.Rules = &contract.RulesRef{Rubric: rubric.Version, Profile: profile.ID, ProfileVersion: profile.Version}

	if perr := profile.CheckFacts(rec.Facts); perr != nil {
		return stop(out, perr)
	}

	// Stage 1: all five gates are decided, even after one fails, so that
	// every reason for screening a candidate out is on the public record.
	gates := make(contract.GateVerdicts, 0, len(rubric.Gates))
	gateRules := make(contract.RuleRows, 0, len(rubric.Gates))
	cleared := true
	for _, g := range rubric.Gates {
		d, ok := profile.Gate(g.ID, rec.Facts)
		if !ok {
			return stop(out, errs.New(errs.NeedsSubtool, errs.NoMatchingRule, g.ID, rec.Category))
		}
		cleared = cleared && d.Pass
		gates = append(gates, contract.GateResult{ID: g.ID, Name: g.Name, Passed: d.Pass, Rule: d.Rule, Reason: d.Reason})
		gateRules = append(gateRules, contract.RuleRow{ID: g.ID, Rule: d.Rule})
	}
	out.Gates = gates
	out.Rules.Gates = gateRules
	if !cleared {
		out.State = contract.StateScreenedOut
		return out
	}

	// Stage 2: the rubric.
	factors := make(contract.FactorRatings, 0, len(rubric.Factors))
	factorRules := make(contract.RuleRows, 0, len(rubric.Factors))
	ratings := make(map[string]int, len(rubric.Factors))
	score := 0
	for _, f := range rubric.Factors {
		rating, rule, ok := profile.Factor(f.ID, rec.Facts)
		if !ok {
			out.Gates = nil
			out.Rules.Gates = nil
			return stop(out, errs.New(errs.NeedsSubtool, errs.NoMatchingRule, f.ID, rec.Category))
		}
		ratings[f.ID] = rating
		score += rating * f.Weight
		factors = append(factors, contract.FactorResult{
			ID: f.ID, Name: f.Name, Rating: rating, Weight: f.Weight, Points: rating * f.Weight, Rule: rule,
		})
		factorRules = append(factorRules, contract.RuleRow{ID: f.ID, Rule: rule})
	}
	// Stage 4: tie-breaker values, in the rubric's order. If a fact-backed
	// tie-breaker was observed as Unknown, pause rather than inventing a zero.
	ties := make([]contract.TieBreaker, 0, len(rubric.TieBreakers))
	for _, t := range rubric.TieBreakers {
		var v int64
		if t.Factor != "" {
			v = int64(ratings[t.Factor])
		} else {
			fv := rec.Facts[t.Fact]
			if fv.Kind == contract.KindUnknown {
				out.Gates = nil
				out.Rules.Gates = nil
				return stop(out, errs.New(errs.NeedsSubtool, errs.NoMatchingRule, t.Fact, rec.Category))
			}
			v = fv.Int
		}
		ties = append(ties, contract.TieBreaker{ID: t.ID, Name: t.Name, Value: v})
	}

	out.Factors = factors
	out.Rules.Factors = factorRules
	out.Score = &score

	// Stage 3: recommendation and split/merge flag.
	out.Recommendation = rubric.Recommend(score, ratings)
	flag := rubric.SplitMergeReview(ratings)
	out.SplitFlag = &flag
	out.TieBreakers = ties

	out.State = contract.StateScored
	return out
}

// stop ends an evaluation with an error. An unfamiliar item is paused in
// needs_subtool; a broken one gets no state, because SPEC.md names none.
func stop(out *contract.Outcome, e *errs.Error) *contract.Outcome {
	out.Error = e
	if e.Code == errs.NeedsSubtool {
		out.State = contract.StateNeedsSubtool
	}
	return out
}

// Compare orders two scored outcomes. It returns a negative number when a
// sorts before b.
//
// The order is: higher score first; then each tie-breaker in turn, higher
// value first; then the item number, lower first, so that two candidates can
// never be left in an undefined order.
func Compare(a, b *contract.Outcome) int {
	if a.Score != nil && b.Score != nil && *a.Score != *b.Score {
		if *a.Score > *b.Score {
			return -1
		}
		return 1
	}
	for i := 0; i < len(a.TieBreakers) && i < len(b.TieBreakers); i++ {
		av, bv := a.TieBreakers[i].Value, b.TieBreakers[i].Value
		if av != bv {
			if av > bv {
				return -1
			}
			return 1
		}
	}
	// Identifiers are PA- and a number with no padding beyond four digits,
	// so a shorter identifier is a lower number.
	if len(a.ItemID) != len(b.ItemID) {
		if len(a.ItemID) < len(b.ItemID) {
			return -1
		}
		return 1
	}
	return strings.Compare(a.ItemID, b.ItemID)
}
