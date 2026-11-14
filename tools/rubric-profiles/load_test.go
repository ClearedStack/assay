package rubricprofiles

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/errs"
)

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// realFiles returns the real rubric and the real pricing profile, ready to
// be damaged one way at a time.
func realFiles(t *testing.T) map[string]string {
	return map[string]string{
		"rubric.yaml":          read(t, "rubric.yaml"),
		"pricing/match.yaml":   read(t, "pricing/match.yaml"),
		"pricing/profile.yaml": read(t, "pricing/profile.yaml"),
	}
}

func loadFiles(files map[string]string) (*Set, error) {
	fsys := fstest.MapFS{}
	for name, content := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return Load(fsys)
}

func TestRealRulesLoad(t *testing.T) {
	set, err := Load(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cyber", "devsecops", "market", "pricing", "proposal"}
	got := set.Categories()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("categories: got %v, want %v", got, want)
	}
	if set.Match("pricing") == nil || set.Match("logistics") != nil {
		t.Error("the matching rule is wrong")
	}
}

// TestRubricMatchesTheSpecification pins rubric.yaml to the numbers in
// SPEC.md. The rubric is data, so without this test it could drift from the
// specification unnoticed.
func TestRubricMatchesTheSpecification(t *testing.T) {
	set, err := Load(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	r := set.Rubric

	gates := []string{"Public source", "Runs in the browser", "Private by design", "One job", "Arithmetic, not advice"}
	if len(r.Gates) != len(gates) {
		t.Fatalf("want %d gates, got %d", len(gates), len(r.Gates))
	}
	for i, name := range gates {
		if r.Gates[i].Name != name {
			t.Errorf("gate %d: got %q, want %q", i+1, r.Gates[i].Name, name)
		}
	}

	factors := []struct {
		name   string
		weight int
	}{
		{"Need for small businesses", 4}, {"Gap", 4}, {"Strength of public source", 3}, {"Fits in a browser", 3},
		{"Small enough to read", 2}, {"Can be checked", 2}, {"Low upkeep of reference data", 2},
	}
	if len(r.Factors) != len(factors) {
		t.Fatalf("want %d factors, got %d", len(factors), len(r.Factors))
	}
	sum := 0
	for i, f := range factors {
		if r.Factors[i].Name != f.name || r.Factors[i].Weight != f.weight {
			t.Errorf("factor %d: got %q x%d, want %q x%d", i+1, r.Factors[i].Name, r.Factors[i].Weight, f.name, f.weight)
		}
		sum += r.Factors[i].Weight
	}
	if r.RatingMin != 1 || r.RatingMax != 5 || sum*r.RatingMin != 20 || sum*r.RatingMax != 100 {
		t.Errorf("the composite must run from 20 to 100; ratings %d-%d, weights sum to %d", r.RatingMin, r.RatingMax, sum)
	}

	ties := []string{"Roadmap phase fit", "Gap severity", "Low upkeep", "Small code footprint", "Community demand"}
	if len(r.TieBreakers) != len(ties) {
		t.Fatalf("want %d tie-breakers, got %d", len(ties), len(r.TieBreakers))
	}
	for i, name := range ties {
		if r.TieBreakers[i].Name != name {
			t.Errorf("tie-breaker %d: got %q, want %q", i+1, r.TieBreakers[i].Name, name)
		}
	}
}

// TestRecommendationsMatchTheSpecification checks the recommendation and the
// split/merge flag against SPEC.md's wording for every one of the 78,125
// possible sets of ratings, using a second, independent statement of the
// rule.
func TestRecommendationsMatchTheSpecification(t *testing.T) {
	set, err := Load(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	r := set.Rubric
	ratings := map[string]int{}
	seen := map[string]int{}
	var walk func(i, total int)
	walk = func(i, total int) {
		if i == len(r.Factors) {
			gap, need, size := ratings["gap"], ratings["need"], ratings["readable_size"]
			var want string
			switch {
			case total >= 75 && gap > 1:
				want = "build"
			case total >= 75 && gap == 1:
				want = "link"
			case total >= 55 && total <= 74:
				want = "park"
			case total < 55:
				want = "reject"
			}
			if got := r.Recommend(total, ratings); got != want {
				t.Fatalf("total %d, gap %d: got %s, want %s", total, gap, got, want)
			}
			wantFlag := need >= 4 && size <= 2
			if got := r.SplitMergeReview(ratings); got != wantFlag {
				t.Fatalf("need %d, size %d: split/merge got %v, want %v", need, size, got, wantFlag)
			}
			seen[want]++
			return
		}
		f := r.Factors[i]
		for v := 1; v <= 5; v++ {
			ratings[f.ID] = v
			walk(i+1, total+v*f.Weight)
		}
	}
	walk(0, 0)
	for _, name := range []string{"build", "link", "park", "reject"} {
		if seen[name] == 0 {
			t.Errorf("no set of ratings gives %s", name)
		}
	}
}

func TestLoadRefusesBadRuleData(t *testing.T) {
	cases := []struct {
		name string
		file string
		old  string
		new  string
		want string // part of the error
	}{
		{"unknown key in profile", "pricing/profile.yaml", "version: 2", "version: 2\ndefault_rating: 3", "default_rating"},
		{"second YAML document", "pricing/profile.yaml", "version: 2", "version: 2\n---\nversion: 3", ""},
		{"profile name differs from folder", "pricing/profile.yaml", "profile: pricing", "profile: prices", "folder"},
		{"condition on undeclared fact", "pricing/profile.yaml", "- when: {needs_backend: {eq: true}}", "- when: {has_backend: {eq: true}}", "undeclared fact"},
		{"catch-all row", "pricing/profile.yaml", "- when: {needs_backend: {eq: true}}", "- when: {}", "at least one condition"},
		{"row without pass", "pricing/profile.yaml", "pass: false\n      reason: \"It needs a backend server.\"", "reason: \"It needs a backend server.\"", "pass is missing"},
		{"row without reason", "pricing/profile.yaml", "reason: \"It needs a backend server.\"", "reason: \" \"", "reason is missing"},
		{"rating out of range", "pricing/profile.yaml", "- when: {need_signals: {gte: 30}}\n      rating: 5", "- when: {need_signals: {gte: 30}}\n      rating: 6", "rating must be 1 to 5"},
		{"operator for the wrong type", "pricing/profile.yaml", "{needs_backend: {eq: true}}", "{needs_backend: {gte: 1}}", "does not apply"},
		{"eq of the wrong kind", "pricing/profile.yaml", "{needs_backend: {eq: true}}", "{needs_backend: {eq: 1}}", "true or false"},
		{"value outside the declared set", "pricing/profile.yaml", "{best_alternative: {in: [none]}}", "{best_alternative: {in: [nothing]}}", "declared values"},
		{"threshold outside the declared range", "pricing/profile.yaml", "{worked_examples: {gte: 10}}", "{worked_examples: {gte: 10001}}", "outside the declared range"},
		{"impossible range", "pricing/profile.yaml", "{need_signals: {gte: 15, lte: 29}}", "{need_signals: {gte: 29, lte: 15}}", "never match"},
		{"empty value list", "pricing/profile.yaml", "{best_alternative: {in: [none]}}", "{best_alternative: {in: []}}", "never match"},
		{"empty tier_in list", "pricing/profile.yaml", "tier_in: [Seen]", "tier_in: []", "never match"},
		{"lower-case tier in tier_in", "pricing/profile.yaml", "tier_in: [Seen]", "tier_in: [seen]", "Seen, Stated or Unknown"},
		{"duplicate tier in tier_in", "pricing/profile.yaml", "tier_in: [Seen]", "tier_in: [Seen, Seen]", "listed twice"},
		{"Unknown tier with value operator", "pricing/profile.yaml", "tier_in: [Seen]", "tier_in: [Unknown]", "Unknown has no value"},
		{"free-text fact type forbidden", "pricing/profile.yaml", "source_public: {type: bool}", "source_public: {type: text}", "unknown type"},
		{"gate with no rows", "pricing/profile.yaml", "  arithmetic_not_advice:\n    - when: {output_kinds: {any_in: [legal_advice, compliance_determination]}}\n      pass: false\n      reason: \"It renders advice or a legal conclusion.\"\n    - when: {output_kinds: {all_in: [computed_number, structured_data, citation_lookup]}}\n      pass: true\n      reason: \"It computes numbers, structures data or looks up citations.\"\n", "  arithmetic_not_advice: []\n", "has no rows"},
		{"gate the rubric does not have", "pricing/profile.yaml", "\nfactors:\n", "\n  sixth_gate:\n    - when: {needs_backend: {eq: true}}\n      pass: false\n      reason: \"No.\"\n\nfactors:\n", "rubric does not have"},
		{"int without bounds", "pricing/profile.yaml", "task_count: {type: int, min: 1, max: 100}", "task_count: {type: int}", "min and max"},
		{"unknown fact type", "pricing/profile.yaml", "source_public: {type: bool}", "source_public: {type: boolean}", "unknown type"},
		{"bool with values", "pricing/profile.yaml", "source_public: {type: bool}", "source_public: {type: bool, values: [yes_please]}", "does not apply"},
		{"fact nobody reads", "pricing/profile.yaml", "source_public: {type: bool}", "source_public: {type: bool}\n  spare_fact: {type: bool}", "no rule reads it"},
		{"tie-breaker fact not declared", "pricing/profile.yaml", "  community_demand: {type: int, min: 0, max: 100000}\n", "", "community_demand"},
		{"duplicate key", "pricing/profile.yaml", "source_public: {type: bool}", "source_public: {type: bool}\n  source_public: {type: bool}", "already defined"},
		{"match names another category", "pricing/match.yaml", "category: pricing", "category: prices", "folder"},
		{"match names another state", "pricing/match.yaml", "state: observed", "state: scored", "match.state"},
		{"match names another contract", "pricing/match.yaml", "contract: v1", "contract: v2", "match.contract"},
		{"recommendation bands overlap", "rubric.yaml", "total: {min: 55, max: 74}", "total: {min: 55, max: 75}", "exactly one"},
		{"recommendation bands leave a hole", "rubric.yaml", "total: {min: 55, max: 74}", "total: {min: 56, max: 74}", "exactly one"},
		{"recommendation on unknown factor", "rubric.yaml", "factor: {factor: gap, min: 2}", "factor: {factor: gaps, min: 2}", "unknown factor"},
		{"zero weight", "rubric.yaml", "name: \"Gap\", weight: 4", "name: \"Gap\", weight: 0", "weight"},
		{"factor listed twice", "rubric.yaml", "{id: checkable,", "{id: gap,", "listed twice"},
		{"tie-breaker with two sources", "rubric.yaml", "name: \"Gap severity\", factor: gap", "name: \"Gap severity\", factor: gap, fact: community_demand", "not both"},
		{"tie-breaker with no source", "rubric.yaml", "name: \"Gap severity\", factor: gap", "name: \"Gap severity\"", "must name"},
		{"unknown key in rubric", "rubric.yaml", "version: 1", "version: 1\nfallback: Park", "fallback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := realFiles(t)
			if !strings.Contains(files[tc.file], tc.old) {
				t.Fatalf("the text to replace is not in %s; the test is out of date", tc.file)
			}
			files[tc.file] = strings.Replace(files[tc.file], tc.old, tc.new, 1)
			_, err := loadFiles(files)
			if err == nil {
				t.Fatal("bad rule data was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadRefusesMissingFiles(t *testing.T) {
	for _, missing := range []string{"rubric.yaml", "pricing/match.yaml", "pricing/profile.yaml"} {
		files := realFiles(t)
		delete(files, missing)
		if missing == "pricing/match.yaml" {
			// Keep the folder present so the loader looks for the file.
			files["pricing/profile.yaml"] = files["pricing/profile.yaml"] + "\n"
		}
		if _, err := loadFiles(files); err == nil {
			t.Errorf("loading without %s should fail", missing)
		}
	}
	if _, err := loadFiles(map[string]string{"rubric.yaml": read(t, "rubric.yaml")}); err == nil {
		t.Error("loading with no profiles should fail")
	}
}

func TestFixturesFolderIsNotACategory(t *testing.T) {
	files := realFiles(t)
	files["fixtures/unknown.input.json"] = "{}"
	set, err := loadFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Categories()) != 1 {
		t.Errorf("got categories %v", set.Categories())
	}
}

func pricingFacts() map[string]contract.Value {
	b := func(v bool) contract.Value { return contract.Value{Kind: contract.KindBool, Tier: contract.TierSeen, Bool: v} }
	i := func(v int64) contract.Value { return contract.Value{Kind: contract.KindInt, Tier: contract.TierSeen, Int: v} }
	s := func(v string) contract.Value { return contract.Value{Kind: contract.KindString, Tier: contract.TierSeen, Str: v} }
	return map[string]contract.Value{
		"source_kind": s("far"), "source_public": b(true), "source_precision": s("exact_formula"),
		"client_runtime": s("browser_js"), "needs_backend": b(false), "needs_remote_api": b(false), "largest_input_kb": i(40),
		"works_offline": b(true), "needs_account": b(false), "emits_tracking": b(false), "connect_src_self": b(true),
		"task_count": i(1), "core_job": s("rate_calculation"),
		"output_kinds": {Kind: contract.KindList, Tier: contract.TierSeen, List: []string{"computed_number"}},
		"need_signals": i(20), "best_alternative": s("paid_only"), "est_source_lines": i(500), "worked_examples": i(6),
		"reference_updates_per_year": i(1), "rate_tables": i(2), "roadmap_phase_fit": i(2), "community_demand": i(14),
	}
}

func TestTierConditions(t *testing.T) {
	set, err := Load(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	p := set.Match("pricing")

	// Stated core_job satisfies ordinary enum value checks.
	statedJob := pricingFacts()
	statedJob["core_job"] = contract.Value{Kind: contract.KindString, Tier: contract.TierStated, Str: "rate_calculation"}
	if d, ok := p.Gate("one_job", statedJob); !ok || !d.Pass {
		t.Fatalf("Stated core_job should pass one_job, got %+v, %v", d, ok)
	}

	// Stated emits_tracking=false does not satisfy tier_in: [Seen] on private_by_design.
	statedPrivacy := pricingFacts()
	statedPrivacy["emits_tracking"] = contract.Value{Kind: contract.KindBool, Tier: contract.TierStated, Bool: false}
	if _, ok := p.Gate("private_by_design", statedPrivacy); ok {
		t.Fatal("Stated emits_tracking=false must pause private_by_design rather than passing")
	}

	// Unknown fact passes CheckFacts and fails closed (ok=false) in rule evaluation.
	unknownJob := pricingFacts()
	unknownJob["core_job"] = contract.Value{Kind: contract.KindUnknown, Tier: contract.TierUnknown}
	if perr := p.CheckFacts(unknownJob); perr != nil {
		t.Fatalf("Unknown fact should pass CheckFacts, got %v", perr)
	}
	if _, ok := p.Gate("one_job", unknownJob); ok {
		t.Fatal("Unknown core_job must fail closed with no matching rule")
	}
}

func TestCheckFacts(t *testing.T) {
	set, err := Load(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	p := set.Match("pricing")
	if perr := p.CheckFacts(pricingFacts()); perr != nil {
		t.Fatalf("good facts refused: %v", perr)
	}

	str := func(v string) contract.Value {
		return contract.Value{Kind: contract.KindString, Tier: contract.TierSeen, Str: v}
	}
	cases := []struct {
		name   string
		change func(f map[string]contract.Value)
		code   errs.Code
		reason errs.Reason
		where  string
	}{
		{"missing", func(f map[string]contract.Value) { delete(f, "task_count") }, errs.Parse, errs.MissingFact, "task_count"},
		{"bool as string", func(f map[string]contract.Value) { f["needs_backend"] = str("false") }, errs.Parse, errs.InvalidType, "needs_backend"},
		{"enum as int", func(f map[string]contract.Value) {
			f["source_kind"] = contract.Value{Kind: contract.KindInt, Tier: contract.TierSeen}
		}, errs.Parse, errs.InvalidType, "source_kind"},
		{"list as string", func(f map[string]contract.Value) { f["output_kinds"] = str("computed_number") }, errs.Parse, errs.InvalidType, "output_kinds"},
		{"below range", func(f map[string]contract.Value) {
			f["task_count"] = contract.Value{Kind: contract.KindInt, Tier: contract.TierSeen, Int: 0}
		}, errs.Parse, errs.OutOfRange, "task_count"},
		{"above range", func(f map[string]contract.Value) {
			f["roadmap_phase_fit"] = contract.Value{Kind: contract.KindInt, Tier: contract.TierSeen, Int: 4}
		}, errs.Parse, errs.OutOfRange, "roadmap_phase_fit"},
		{"repeated list item", func(f map[string]contract.Value) {
			f["output_kinds"] = contract.Value{Kind: contract.KindList, Tier: contract.TierSeen, List: []string{"computed_number", "computed_number"}}
		}, errs.Parse, errs.InvalidValue, "output_kinds"},
		{"undeclared fact", func(f map[string]contract.Value) { f["margin_basis"] = str("x") }, errs.NeedsSubtool, errs.UnknownFact, "facts"},
		{"unfamiliar enum value", func(f map[string]contract.Value) { f["client_runtime"] = str("smart_tv") }, errs.NeedsSubtool, errs.UnknownValue, "client_runtime"},
		{"unfamiliar core_job value", func(f map[string]contract.Value) { f["core_job"] = str("freeform_chat") }, errs.NeedsSubtool, errs.UnknownValue, "core_job"},
		{"broken beats unfamiliar", func(f map[string]contract.Value) {
			f["margin_basis"] = str("x")
			f["client_runtime"] = str("smart_tv")
			delete(f, "worked_examples")
		}, errs.Parse, errs.MissingFact, "worked_examples"},
		{"undeclared fact beats unfamiliar value", func(f map[string]contract.Value) {
			f["margin_basis"] = str("x")
			f["client_runtime"] = str("smart_tv")
		}, errs.NeedsSubtool, errs.UnknownFact, "facts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := pricingFacts()
			tc.change(facts)
			perr := p.CheckFacts(facts)
			if perr == nil {
				t.Fatal("expected an error")
			}
			if perr.Code != tc.code || perr.Reason != tc.reason || perr.Where != tc.where {
				t.Errorf("got %s %s (%s), want %s %s (%s)", perr.Code, perr.Reason, perr.Where, tc.code, tc.reason, tc.where)
			}
		})
	}
}

// TestNumericTablesHaveNoHoles walks every whole number across each band
// edge of the single-fact numeric tables and checks that exactly one row
// answers. A hole would pause real items; an overlap would hide a row.
func TestNumericTablesHaveNoHoles(t *testing.T) {
	set, err := Load(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	for _, category := range set.Categories() {
		p := set.Match(category)
		for id, rows := range p.factors {
			single := ""
			for _, row := range rows {
				if len(row.when) != 1 || row.when[0].typ != typeInt {
					single = ""
					break
				}
				single = row.when[0].fact
			}
			if single == "" {
				continue
			}
			def := p.facts[single]
			limit := def.max
			if limit > 250000 {
				limit = 250000
			}
			for v := def.min; v <= limit; v++ {
				n := 0
				for _, row := range rows {
					if row.when[0].holds(contract.Value{Kind: contract.KindInt, Tier: contract.TierSeen, Int: v}) {
						n++
					}
				}
				if n != 1 {
					t.Fatalf("%s: factor %s: %s = %d is answered by %d rows", category, id, single, v, n)
				}
			}
		}
	}
}
