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

	"github.com/ClearedStack/assay/internal/yamlite"
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
	ID   string
	Name string
}

// FactorDef names one rubric factor and gives its weight.
type FactorDef struct {
	ID     string
	Name   string
	Weight int
}

// Bound is an inclusive range. A nil end is open.
type Bound struct {
	Min *int
	Max *int
}

func (b Bound) holds(v int) bool {
	return (b.Min == nil || v >= *b.Min) && (b.Max == nil || v <= *b.Max)
}

// FactorRange is a range on one factor's rating.
type FactorRange struct {
	Factor string
	Min    *int
	Max    *int
}

func (r FactorRange) holds(ratings map[string]int) bool {
	return Bound{Min: r.Min, Max: r.Max}.holds(ratings[r.Factor])
}

// Recommendation is one preliminary recommendation and the condition for it:
// a range on the total and, optionally, a range on one factor.
type Recommendation struct {
	Name   string
	Total  Bound
	Factor *FactorRange
}

func (r Recommendation) holds(total int, ratings map[string]int) bool {
	return r.Total.holds(total) && (r.Factor == nil || r.Factor.holds(ratings))
}

// TieBreakerDef is one tie-breaker. Its value is either a factor's rating or
// a whole-number fact from the record; higher sorts first.
type TieBreakerDef struct {
	ID     string
	Name   string
	Factor string
	Fact   string
}

// readBound reads {min: .., max: ..}. Either end may be left out.
func readBound(n *yamlite.Node) (Bound, error) {
	r := n.Read("min", "max")
	b := Bound{Min: toInt(r.OptInt("min")), Max: toInt(r.OptInt("max"))}
	return b, r.Err()
}

// readFactorRange reads {factor: .., min: .., max: ..}.
func readFactorRange(n *yamlite.Node) (FactorRange, error) {
	r := n.Read("factor", "min", "max")
	fr := FactorRange{Factor: r.String("factor"), Min: toInt(r.OptInt("min")), Max: toInt(r.OptInt("max"))}
	return fr, r.Err()
}

func toInt(v *int64) *int {
	if v == nil {
		return nil
	}
	i := int(*v)
	return &i
}

func parseRubric(data []byte) (*Rubric, error) {
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, err
	}
	top := root.Read("version", "rating", "gates", "factors", "recommendations", "split_merge_review", "tie_breakers")
	r := &Rubric{Version: int(top.Int("version"))}
	ratingNode := top.Node("rating")
	gateNodes, factorNodes := top.List("gates"), top.List("factors")
	recNodes, splitNodes, tieNodes := top.List("recommendations"), top.List("split_merge_review"), top.List("tie_breakers")
	if err := top.Err(); err != nil {
		return nil, err
	}

	if r.Version < 1 {
		return nil, fmt.Errorf("version must be 1 or more")
	}
	rating, err := readBound(ratingNode)
	if err != nil {
		return nil, err
	}
	if rating.Min == nil || rating.Max == nil || *rating.Min < 1 || *rating.Max <= *rating.Min {
		return nil, fmt.Errorf("rating needs a min of 1 or more and a larger max")
	}
	r.RatingMin, r.RatingMax = *rating.Min, *rating.Max

	for _, n := range gateNodes {
		f := n.Read("id", "name")
		r.Gates = append(r.Gates, GateDef{ID: f.String("id"), Name: f.String("name")})
		if err := f.Err(); err != nil {
			return nil, err
		}
	}
	for _, n := range factorNodes {
		f := n.Read("id", "name", "weight")
		r.Factors = append(r.Factors, FactorDef{ID: f.String("id"), Name: f.String("name"), Weight: int(f.Int("weight"))})
		if err := f.Err(); err != nil {
			return nil, err
		}
	}
	for _, n := range recNodes {
		f := n.Read("name", "total", "factor")
		rec := Recommendation{Name: f.String("name")}
		totalNode, factorNode := f.Node("total"), f.OptNode("factor")
		if err := f.Err(); err != nil {
			return nil, err
		}
		if rec.Total, err = readBound(totalNode); err != nil {
			return nil, err
		}
		if factorNode != nil {
			fr, err := readFactorRange(factorNode)
			if err != nil {
				return nil, err
			}
			rec.Factor = &fr
		}
		r.Recommendations = append(r.Recommendations, rec)
	}
	for _, n := range splitNodes {
		fr, err := readFactorRange(n)
		if err != nil {
			return nil, err
		}
		r.SplitMerge = append(r.SplitMerge, fr)
	}
	for _, n := range tieNodes {
		f := n.Read("id", "name", "factor", "fact")
		r.TieBreakers = append(r.TieBreakers, TieBreakerDef{ID: f.String("id"), Name: f.String("name"), Factor: f.OptString("factor"), Fact: f.OptString("fact")})
		if err := f.Err(); err != nil {
			return nil, err
		}
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
