// Package rubricprofiles is the rubric-profiles sub-tool: the knockout gates,
// the rubric and the per-category decision tables, all held as data.
//
// The sub-tool has three parts:
//
//   - a matching rule: <category>/match.yaml says which records a profile
//     takes;
//   - the logic: rubric.yaml (shared by every category) and
//     <category>/profile.yaml (that category's decision tables), read by the
//     Go code in this folder;
//   - fixtures: <category>/fixtures holds saved inputs and the exact outputs
//     expected for them.
//
// A decision table is an ordered list of rows. The first row whose conditions
// all hold decides. When no row matches there is no answer, and the caller
// pauses the item with E_NEEDS_SUBTOOL. There are no defaults.
package rubricprofiles

import (
	"fmt"
	"regexp"
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Rubric is the part of the rules every category shares: which gates exist,
// the factors and their weights, how a total becomes a recommendation, when
// to flag split/merge review, and the order of the tie-breakers.
type Rubric struct {
	Version         int
	RatingMin       int
	RatingMax       int
	Gates           []GateDef
	Factors         []FactorDef
	Recommendations []Recommendation
	SplitMerge      []FactorRange
	TieBreakers     []TieBreakerDef
}

// GateDef names one knockout gate.
type GateDef struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

// FactorDef names one rubric factor and gives its weight.
type FactorDef struct {
	ID     string `yaml:"id"`
	Name   string `yaml:"name"`
	Weight int    `yaml:"weight"`
}

// Bound is an inclusive range. A nil end is open.
type Bound struct {
	Min *int `yaml:"min"`
	Max *int `yaml:"max"`
}

func (b Bound) holds(v int) bool {
	return (b.Min == nil || v >= *b.Min) && (b.Max == nil || v <= *b.Max)
}

// FactorRange is a range on one factor's rating.
type FactorRange struct {
	Factor string `yaml:"factor"`
	Min    *int   `yaml:"min"`
	Max    *int   `yaml:"max"`
}

func (r FactorRange) holds(ratings map[string]int) bool {
	return Bound{Min: r.Min, Max: r.Max}.holds(ratings[r.Factor])
}

// Recommendation is one preliminary recommendation and the condition for it:
// a range on the total and, optionally, a range on one factor.
type Recommendation struct {
	Name   string       `yaml:"name"`
	Total  Bound        `yaml:"total"`
	Factor *FactorRange `yaml:"factor"`
}

func (r Recommendation) holds(total int, ratings map[string]int) bool {
	return r.Total.holds(total) && (r.Factor == nil || r.Factor.holds(ratings))
}

// TieBreakerDef is one tie-breaker. Its value is either a factor's rating or
// a whole-number fact from the record; higher sorts first.
type TieBreakerDef struct {
	ID     string `yaml:"id"`
	Name   string `yaml:"name"`
	Factor string `yaml:"factor"`
	Fact   string `yaml:"fact"`
}

type rubricYAML struct {
	Version         int              `yaml:"version"`
	Rating          Bound            `yaml:"rating"`
	Gates           []GateDef        `yaml:"gates"`
	Factors         []FactorDef      `yaml:"factors"`
	Recommendations []Recommendation `yaml:"recommendations"`
	SplitMerge      []FactorRange    `yaml:"split_merge_review"`
	TieBreakers     []TieBreakerDef  `yaml:"tie_breakers"`
}

func parseRubric(data []byte) (*Rubric, error) {
	var y rubricYAML
	if err := decodeStrict(data, &y); err != nil {
		return nil, err
	}
	if y.Version < 1 {
		return nil, fmt.Errorf("version must be 1 or more")
	}
	if y.Rating.Min == nil || y.Rating.Max == nil || *y.Rating.Min < 1 || *y.Rating.Max <= *y.Rating.Min {
		return nil, fmt.Errorf("rating needs a min of 1 or more and a larger max")
	}
	r := &Rubric{
		Version:         y.Version,
		RatingMin:       *y.Rating.Min,
		RatingMax:       *y.Rating.Max,
		Gates:           y.Gates,
		Factors:         y.Factors,
		Recommendations: y.Recommendations,
		SplitMerge:      y.SplitMerge,
		TieBreakers:     y.TieBreakers,
	}

	if len(r.Gates) == 0 {
		return nil, fmt.Errorf("no gates")
	}
	seen := map[string]bool{}
	for _, g := range r.Gates {
		if !idPattern.MatchString(g.ID) || g.Name == "" {
			return nil, fmt.Errorf("gate %q needs a valid id and a name", g.ID)
		}
		if seen[g.ID] {
			return nil, fmt.Errorf("gate %q is listed twice", g.ID)
		}
		seen[g.ID] = true
	}

	if len(r.Factors) == 0 || len(r.Factors) > 9 {
		return nil, fmt.Errorf("need between 1 and 9 factors")
	}
	factors := map[string]bool{}
	for _, f := range r.Factors {
		if !idPattern.MatchString(f.ID) || f.Name == "" {
			return nil, fmt.Errorf("factor %q needs a valid id and a name", f.ID)
		}
		if factors[f.ID] {
			return nil, fmt.Errorf("factor %q is listed twice", f.ID)
		}
		if f.Weight < 1 {
			return nil, fmt.Errorf("factor %q needs a weight of 1 or more", f.ID)
		}
		factors[f.ID] = true
	}

	if len(r.Recommendations) == 0 {
		return nil, fmt.Errorf("no recommendations")
	}
	for _, rec := range r.Recommendations {
		if rec.Name == "" {
			return nil, fmt.Errorf("a recommendation has no name")
		}
		if rec.Factor != nil && !factors[rec.Factor.Factor] {
			return nil, fmt.Errorf("recommendation %q names unknown factor %q", rec.Name, rec.Factor.Factor)
		}
	}
	if err := r.checkRecommendations(); err != nil {
		return nil, err
	}

	if len(r.SplitMerge) == 0 {
		return nil, fmt.Errorf("no split_merge_review condition")
	}
	for _, c := range r.SplitMerge {
		if !factors[c.Factor] {
			return nil, fmt.Errorf("split_merge_review names unknown factor %q", c.Factor)
		}
	}

	if len(r.TieBreakers) == 0 {
		return nil, fmt.Errorf("no tie-breakers")
	}
	seen = map[string]bool{}
	for _, t := range r.TieBreakers {
		if !idPattern.MatchString(t.ID) || t.Name == "" {
			return nil, fmt.Errorf("tie-breaker %q needs a valid id and a name", t.ID)
		}
		if seen[t.ID] {
			return nil, fmt.Errorf("tie-breaker %q is listed twice", t.ID)
		}
		seen[t.ID] = true
		switch {
		case t.Factor != "" && t.Fact != "":
			return nil, fmt.Errorf("tie-breaker %q must name a factor or a fact, not both", t.ID)
		case t.Factor != "":
			if !factors[t.Factor] {
				return nil, fmt.Errorf("tie-breaker %q names unknown factor %q", t.ID, t.Factor)
			}
		case t.Fact != "":
			if !idPattern.MatchString(t.Fact) {
				return nil, fmt.Errorf("tie-breaker %q names an invalid fact", t.ID)
			}
		default:
			return nil, fmt.Errorf("tie-breaker %q must name a factor or a fact", t.ID)
		}
	}
	return r, nil
}

// checkRecommendations tries every possible set of ratings and insists that
// exactly one recommendation applies to each. A gap or an overlap between
// the bands is a mistake in the data, found at start-up instead of on some
// unlucky item.
func (r *Rubric) checkRecommendations() error {
	ratings := make(map[string]int, len(r.Factors))
	var walk func(i, total int) error
	walk = func(i, total int) error {
		if i == len(r.Factors) {
			n := 0
			for _, rec := range r.Recommendations {
				if rec.holds(total, ratings) {
					n++
				}
			}
			if n != 1 {
				return fmt.Errorf("a total of %d matches %d recommendations; it must match exactly one", total, n)
			}
			return nil
		}
		f := r.Factors[i]
		for v := r.RatingMin; v <= r.RatingMax; v++ {
			ratings[f.ID] = v
			if err := walk(i+1, total+v*f.Weight); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(0, 0)
}

// Recommend turns a total and the ratings into a recommendation name.
func (r *Rubric) Recommend(total int, ratings map[string]int) string {
	for _, rec := range r.Recommendations {
		if rec.holds(total, ratings) {
			return rec.Name
		}
	}
	// checkRecommendations proved one always applies.
	panic("rubricprofiles: no recommendation applies")
}

// SplitMergeReview reports whether the split/merge flag is raised.
func (r *Rubric) SplitMergeReview(ratings map[string]int) bool {
	for _, c := range r.SplitMerge {
		if !c.holds(ratings) {
			return false
		}
	}
	return true
}
