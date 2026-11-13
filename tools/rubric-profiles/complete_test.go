package rubricprofiles

import (
	"math/rand"
	"os"
	"sort"
	"testing"

	"github.com/ClearedStack/assay/internal/contract"
)

// TestTablesAnswerEveryCandidateThatClearsTheGates throws a large, fixed set
// of made-up candidates at each profile. Each fact is drawn from the values
// that matter: every declared value of an enum, and for a number the
// thresholds the rules mention, one either side of each, and the ends of
// the declared range.
//
// Every gate must give a verdict (the one deliberate exception is a
// sentence whose opening word is on neither verb list, which is not
// generated here), and every candidate that clears the gates must get a
// rating for every factor. A hole in a table would otherwise only show up
// when a real item fell into it.
func TestTablesAnswerEveryCandidateThatClearsTheGates(t *testing.T) {
	set, err := Load(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	for _, category := range set.Categories() {
		p := set.Match(category)

		// Thresholds the rules mention, per number fact, and the verbs.
		edges := map[string]map[int64]bool{}
		var verbs []string
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
				for w := range c.firstWord {
					verbs = append(verbs, w)
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
		sort.Strings(verbs)

		choices := map[string][]contract.Value{}
		for _, name := range p.factNames {
			def := p.facts[name]
			switch def.typ {
			case typeBool:
				choices[name] = []contract.Value{{Kind: contract.KindBool, Bool: false}, {Kind: contract.KindBool, Bool: true}}
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
					choices[name] = append(choices[name], contract.Value{Kind: contract.KindInt, Int: v})
				}
			case typeEnum, typeList:
				var values []string
				for v := range def.values {
					values = append(values, v)
				}
				sort.Strings(values)
				for _, v := range values {
					if def.typ == typeEnum {
						choices[name] = append(choices[name], contract.Value{Kind: contract.KindString, Str: v})
					} else {
						choices[name] = append(choices[name], contract.Value{Kind: contract.KindList, List: []string{v}})
					}
				}
				if def.typ == typeList {
					choices[name] = append(choices[name], contract.Value{Kind: contract.KindList, List: values})
				}
			case typeText:
				for _, w := range verbs {
					choices[name] = append(choices[name], contract.Value{Kind: contract.KindString, Str: w + " a made-up thing."})
				}
			}
		}

		rng := rand.New(rand.NewSource(1)) // fixed seed: the same candidates every run
		cleared := 0
		for n := 0; n < 40000; n++ {
			facts := make(map[string]contract.Value, len(p.factNames))
			for _, name := range p.factNames {
				c := choices[name]
				facts[name] = c[rng.Intn(len(c))]
			}
			// Bias half the candidates towards clearing the gates, so the
			// factor tables get a proper workout.
			if n%2 == 0 {
				for _, name := range []string{"needs_backend", "needs_remote_api", "needs_account", "emits_tracking"} {
					facts[name] = contract.Value{Kind: contract.KindBool, Bool: false}
				}
				for _, name := range []string{"source_public", "works_offline", "connect_src_self"} {
					facts[name] = contract.Value{Kind: contract.KindBool, Bool: true}
				}
				facts["task_count"] = contract.Value{Kind: contract.KindInt, Int: 1}
				facts["output_kinds"] = contract.Value{Kind: contract.KindList, List: []string{"computed_number"}}
			}
			if perr := p.CheckFacts(facts); perr != nil {
				t.Fatalf("%s: generated facts were refused: %v", category, perr)
			}
			pass := true
			for _, g := range set.Rubric.Gates {
				d, ok := p.Gate(g.ID, facts)
				if !ok {
					t.Fatalf("%s: gate %s gave no verdict for %+v", category, g.ID, facts)
				}
				pass = pass && d.Pass
			}
			if !pass {
				continue
			}
			cleared++
			for _, f := range set.Rubric.Factors {
				if _, _, ok := p.Factor(f.ID, facts); !ok {
					t.Fatalf("%s: factor %s gave no rating for %+v", category, f.ID, facts)
				}
			}
		}
		if cleared < 1000 {
			t.Errorf("%s: only %d generated candidates cleared the gates; the test is not exercising the factor tables", category, cleared)
		}
	}
}
