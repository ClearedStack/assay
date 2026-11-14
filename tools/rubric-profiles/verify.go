package rubricprofiles

import (
	"fmt"
	"sort"

	"github.com/ClearedStack/assay/internal/contract"
)

// DefaultCompletenessSamples is the number of deterministic boundary and enum
// candidates tested per category when checking tables for holes.
const DefaultCompletenessSamples = 40000

// splitMix64 advances state deterministically without importing math/rand.
func splitMix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// CheckCompleteness throws a deterministic set of boundary and enum candidates
// at every profile in set and returns any holes or schema mismatches found.
//
// Every Seen candidate must receive a verdict on all five gates, and every
// candidate that clears the five gates must receive a rating on all seven
// factors.
func CheckCompleteness(set *Set, samples int) []string {
	if samples <= 0 {
		samples = DefaultCompletenessSamples
	}
	var problems []string
	for _, category := range set.Categories() {
		p := set.Match(category)

		edges := map[string]map[int64]bool{}
		note := func(conds []cond) {
			for _, c := range conds {
				for _, v := range []*int64{c.eqInt, c.gte, c.lte} {
					if v == nil {
						continue
					}
					if edges[c.fact] == nil {
						edges[c.fact] = map[int64]bool{}
					}
					for _, e := range []int64{*v - 1, *v, *v + 1} {
						edges[c.fact][e] = true
					}
				}
			}
		}
		for _, rows := range p.gates {
			for _, row := range rows {
				note(row.when)
			}
		}
		for _, rows := range p.factors {
			for _, row := range rows {
				note(row.when)
			}
		}

		choices := map[string][]contract.Value{}
		for _, name := range p.factNames {
			def := p.facts[name]
			switch def.typ {
			case typeBool:
				choices[name] = []contract.Value{
					{Kind: contract.KindBool, Tier: contract.TierSeen, Bool: false},
					{Kind: contract.KindBool, Tier: contract.TierSeen, Bool: true},
				}
			case typeInt:
				vals := map[int64]bool{def.min: true, def.max: true}
				for e := range edges[name] {
					if e >= def.min && e <= def.max {
						vals[e] = true
					}
				}
				var sorted []int64
				for v := range vals {
					sorted = append(sorted, v)
				}
				sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
				for _, v := range sorted {
					choices[name] = append(choices[name], contract.Value{Kind: contract.KindInt, Tier: contract.TierSeen, Int: v})
				}
			case typeEnum, typeList:
				var values []string
				for v := range def.values {
					values = append(values, v)
				}
				sort.Strings(values)
				for _, v := range values {
					if def.typ == typeEnum {
						choices[name] = append(choices[name], contract.Value{Kind: contract.KindString, Tier: contract.TierSeen, Str: v})
					} else {
						choices[name] = append(choices[name], contract.Value{Kind: contract.KindList, Tier: contract.TierSeen, List: []string{v}})
					}
				}
				if def.typ == typeList {
					choices[name] = append(choices[name], contract.Value{Kind: contract.KindList, Tier: contract.TierSeen, List: values})
				}
			}
		}

		seed := uint64(1)
		cleared := 0
		failed := false
		for n := 0; n < samples && !failed; n++ {
			facts := make(map[string]contract.Value, len(p.factNames))
			for _, name := range p.factNames {
				c := choices[name]
				idx := int(splitMix64(&seed) % uint64(len(c)))
				facts[name] = c[idx]
			}
			// Bias half the candidates towards clearing the gates so factor
			// tables receive thorough boundary coverage.
			if n%2 == 0 {
				for _, name := range []string{"needs_backend", "needs_remote_api", "needs_account", "emits_tracking"} {
					if _, ok := p.facts[name]; ok {
						facts[name] = contract.Value{Kind: contract.KindBool, Tier: contract.TierSeen, Bool: false}
					}
				}
				for _, name := range []string{"source_public", "works_offline", "connect_src_self"} {
					if _, ok := p.facts[name]; ok {
						facts[name] = contract.Value{Kind: contract.KindBool, Tier: contract.TierSeen, Bool: true}
					}
				}
				if _, ok := p.facts["task_count"]; ok {
					facts["task_count"] = contract.Value{Kind: contract.KindInt, Tier: contract.TierSeen, Int: 1}
				}
				if def, ok := p.facts["output_kinds"]; ok && def.values["computed_number"] {
					facts["output_kinds"] = contract.Value{Kind: contract.KindList, Tier: contract.TierSeen, List: []string{"computed_number"}}
				}
			}
			if perr := p.CheckFacts(facts); perr != nil {
				problems = append(problems, fmt.Sprintf("%s: generated facts were refused: %v", category, perr))
				failed = true
				break
			}
			pass := true
			for _, g := range set.Rubric.Gates {
				d, ok := p.Gate(g.ID, facts)
				if !ok {
					problems = append(problems, fmt.Sprintf("%s: gate %s gave no verdict for generated Seen candidate", category, g.ID))
					failed = true
					break
				}
				pass = pass && d.Pass
			}
			if failed || !pass {
				continue
			}
			cleared++
			for _, f := range set.Rubric.Factors {
				if _, _, ok := p.Factor(f.ID, facts); !ok {
					problems = append(problems, fmt.Sprintf("%s: factor %s gave no rating for gate-clearing candidate", category, f.ID))
					failed = true
					break
				}
			}
		}
		minCleared := samples / 40
		if minCleared > 1000 {
			minCleared = 1000
		}
		if !failed && minCleared > 0 && cleared < minCleared {
			problems = append(problems, fmt.Sprintf("%s: only %d of %d generated candidates cleared the gates", category, cleared, samples))
		}
	}
	return problems
}
