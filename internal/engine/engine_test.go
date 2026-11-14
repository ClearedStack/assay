package engine_test

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
	"github.com/ClearedStack/assay/internal/errs"
	"github.com/ClearedStack/assay/tools"
)

const fixtures = "../../tools/rubric-profiles"

func newEngine(t *testing.T) *engine.Engine {
	t.Helper()
	set, err := tools.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	return engine.New(set)
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestEmbeddedRulesAreTheRulesOnDisk makes sure the binary carries the same
// rules the fixture tests check: every golden file must also hold for the
// embedded copy.
func TestEmbeddedRulesAreTheRulesOnDisk(t *testing.T) {
	eng := newEngine(t)
	inputs, _ := filepath.Glob(filepath.Join(fixtures, "*/fixtures/*.input.json"))
	more, _ := filepath.Glob(filepath.Join(fixtures, "fixtures/*.input.json"))
	inputs = append(inputs, more...)
	if len(inputs) < 100 {
		t.Fatalf("found only %d fixtures", len(inputs))
	}
	for _, in := range inputs {
		raw, err := os.ReadFile(in)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(strings.TrimSuffix(in, ".input.json") + ".golden.json")
		if err != nil {
			t.Fatal(err)
		}
		if got := contract.Encode(eng.Evaluate(raw)); !bytes.Equal(got, want) {
			t.Errorf("%s: the embedded rules give a different outcome", in)
		}
	}
}

// TestSameFactsSameOutcome rewrites a record with its keys in a different
// order and different spacing. The facts are identical, so the outcome must
// be identical to the byte.
func TestSameFactsSameOutcome(t *testing.T) {
	eng := newEngine(t)
	for _, name := range []string{"pricing/fixtures/build.input.json", "cyber/fixtures/park.input.json", "market/fixtures/screened-out-all-gates.input.json"} {
		raw := readFixture(t, name)
		want := contract.Encode(eng.Evaluate(raw))

		var rec map[string]any
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		// encoding/json writes map keys sorted, which differs from the
		// order the fixture was written in, and writes no spacing.
		reordered, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(reordered, raw) {
			t.Fatal("the rewritten record is not different; the test proves nothing")
		}
		for i := 0; i < 50; i++ {
			if got := contract.Encode(eng.Evaluate(reordered)); !bytes.Equal(got, want) {
				t.Fatalf("%s: outcome changed on run %d", name, i)
			}
		}
	}
}

func TestScoredOutcomeAddsUp(t *testing.T) {
	eng := newEngine(t)
	out := eng.Evaluate(readFixture(t, "pricing/fixtures/build.input.json"))
	if out.State != contract.StateScored || out.Error != nil {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if len(out.Gates) != 5 || len(out.Factors) != 7 || len(out.TieBreakers) != 5 {
		t.Fatalf("want 5 gates, 7 factors, 5 tie-breakers; got %d, %d, %d", len(out.Gates), len(out.Factors), len(out.TieBreakers))
	}
	sum := 0
	for _, f := range out.Factors {
		if f.Rating < 1 || f.Rating > 5 || f.Points != f.Rating*f.Weight {
			t.Errorf("factor %s: rating %d, weight %d, points %d", f.ID, f.Rating, f.Weight, f.Points)
		}
		sum += f.Points
	}
	if *out.Score != sum || sum < 20 || sum > 100 {
		t.Errorf("score %d, factors add up to %d", *out.Score, sum)
	}
}

func TestScreenedOutShowsEveryGateAndNoScore(t *testing.T) {
	eng := newEngine(t)
	out := eng.Evaluate(readFixture(t, "pricing/fixtures/screened-out-one-gate.input.json"))
	if out.State != contract.StateScreenedOut {
		t.Fatalf("state %q", out.State)
	}
	if len(out.Gates) != 5 {
		t.Fatalf("all five gates must be reported, got %d", len(out.Gates))
	}
	failed := 0
	for _, g := range out.Gates {
		if g.Reason == "" {
			t.Errorf("gate %s has no public reason", g.ID)
		}
		if !g.Passed {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("want exactly one failed gate, got %d", failed)
	}
	if out.Factors != nil || out.Score != nil || out.Recommendation != "" || out.SplitFlag != nil || out.TieBreakers != nil {
		t.Error("a candidate that is screened out must not be scored")
	}
}

func TestPausedAndBrokenItemsCarryNoVerdict(t *testing.T) {
	eng := newEngine(t)
	cases := map[string]struct {
		state string
		code  errs.Code
	}{
		"fixtures/needs-subtool-unknown-category.input.json":      {contract.StateNeedsSubtool, errs.NeedsSubtool},
		"pricing/fixtures/needs-subtool-unlisted-verb.input.json": {contract.StateNeedsSubtool, errs.NeedsSubtool},
		"pricing/fixtures/needs-subtool-unknown-value.input.json": {contract.StateNeedsSubtool, errs.NeedsSubtool},
		"pricing/fixtures/parse-wrong-type.input.json":            {"", errs.Parse},
		"fixtures/parse-malformed-json.input.json":                {"", errs.Parse},
	}
	for name, want := range cases {
		out := eng.Evaluate(readFixture(t, name))
		if out.Error == nil || out.Error.Code != want.code || out.State != want.state {
			t.Errorf("%s: got state %q, error %v", name, out.State, out.Error)
		}
		if out.Gates != nil || out.Factors != nil || out.Score != nil || out.Recommendation != "" {
			t.Errorf("%s: a partial verdict was given", name)
		}
	}
}

// TestErrorsNeverEchoTheInput plants text that looks like a machine name, an
// address and a credential in every place a record can carry text, and
// checks that none of it comes back out.
func TestErrorsNeverEchoTheInput(t *testing.T) {
	eng := newEngine(t)
	var rec map[string]any
	if err := json.Unmarshal(readFixture(t, "pricing/fixtures/build.input.json"), &rec); err != nil {
		t.Fatal(err)
	}
	const planted = "db01.corp.example"
	leaks := []string{"db01", "corp", "10.20.30.40", "hunter2", "s3cr3t"}

	observed := func(r map[string]any) map[string]any {
		return r["results"].(map[string]any)["sextant"].(map[string]any)
	}
	facts := func(r map[string]any) map[string]any { return observed(r)["facts"].(map[string]any) }
	mutations := map[string]func(r map[string]any){
		"unknown envelope field":    func(r map[string]any) { r["host_"+planted] = "10.20.30.40" },
		"unknown observation field": func(r map[string]any) { observed(r)["password_hunter2"] = "s3cr3t" },
		"unknown fact":              func(r map[string]any) { facts(r)["password_hunter2"] = "s3cr3t" },
		"unknown enum value":        func(r map[string]any) { facts(r)["source_kind"] = planted },
		"unknown list value":        func(r map[string]any) { facts(r)["output_kinds"] = []any{"10.20.30.40"} },
		"unlisted verb":             func(r map[string]any) { facts(r)["task_sentence"] = "hunter2 is at " + planted },
		"unknown category":          func(r map[string]any) { observed(r)["category"] = "db01" },
		"bad category":              func(r map[string]any) { observed(r)["category"] = planted },
		"bad item id":               func(r map[string]any) { r["id"] = "s3cr3t@" + planted },
		"bad lease":                 func(r map[string]any) { r["lease"] = "hunter2 " + planted },
		"bad lane":                  func(r map[string]any) { r["lane"] = planted },
		"bad expires":               func(r map[string]any) { r["expires"] = "10.20.30.40" },
		"wrong type":                func(r map[string]any) { facts(r)["task_count"] = "hunter2" },
		"bad state":                 func(r map[string]any) { r["state"] = planted },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data, _ := json.Marshal(rec)
			var copyRec map[string]any
			_ = json.Unmarshal(data, &copyRec)
			mutate(copyRec)
			raw, _ := json.Marshal(copyRec)

			out := eng.Evaluate(raw)
			if out.Error == nil {
				t.Fatal("expected an error")
			}
			errJSON, _ := json.Marshal(out.Error)
			for _, leak := range leaks {
				if strings.Contains(string(errJSON), leak) || strings.Contains(out.Error.Error(), leak) {
					t.Errorf("the error leaks %q: %s", leak, errJSON)
				}
			}
			// Outside the error, the only input text an outcome may repeat
			// is a well-formed item identifier and category.
			whole := string(contract.Encode(out))
			for _, leak := range []string{"corp", "10.20.30.40", "hunter2", "s3cr3t"} {
				if strings.Contains(whole, leak) {
					t.Errorf("the outcome leaks %q:\n%s", leak, whole)
				}
			}
		})
	}
}

func scored(id string, score int, ties ...int64) *contract.Outcome {
	o := &contract.Outcome{ItemID: id, State: contract.StateScored, Score: &score}
	for i, v := range ties {
		o.TieBreakers = append(o.TieBreakers, contract.TieBreaker{ID: "t" + strconv.Itoa(i), Value: v})
	}
	return o
}

// TestCompareAppliesTheTieBreakersInOrder builds candidates that differ in
// exactly one place each and checks the order: total, then roadmap phase
// fit, gap severity, low upkeep, small code footprint, community demand,
// and last the item number.
func TestCompareAppliesTheTieBreakersInOrder(t *testing.T) {
	want := []*contract.Outcome{
		scored("z-higher-total", 81, 0, 1, 1, 1, 0),
		scored("y-roadmap", 80, 3, 1, 1, 1, 0),
		scored("x-gap", 80, 2, 5, 1, 1, 0),
		scored("w-upkeep", 80, 2, 4, 5, 1, 0),
		scored("v-footprint", 80, 2, 4, 4, 5, 0),
		scored("u-demand", 80, 2, 4, 4, 4, 900),
		scored("a-same", 80, 2, 4, 4, 4, 10),
		scored("b-same", 80, 2, 4, 4, 4, 10),
		scored("c-lower-total", 79, 3, 5, 5, 5, 100000),
	}
	// Start from several different orders; the result must not depend on it.
	starts := [][]int{{8, 7, 6, 5, 4, 3, 2, 1, 0}, {4, 0, 8, 2, 6, 1, 7, 3, 5}, {0, 1, 2, 3, 4, 5, 6, 7, 8}, {7, 6, 8, 0, 1, 5, 4, 2, 3}}
	for _, start := range starts {
		got := make([]*contract.Outcome, len(want))
		for i, idx := range start {
			got[i] = want[idx]
		}
		sort.Slice(got, func(i, j int) bool { return engine.Compare(got[i], got[j]) < 0 })
		for i := range want {
			if got[i].ItemID != want[i].ItemID {
				t.Fatalf("from start %v: position %d is %s, want %s", start, i, got[i].ItemID, want[i].ItemID)
			}
		}
	}
	// With everything else equal the lower item number sorts first, by
	// number and not by spelling: PA-9999 comes before PA-10000.
	low, high := scored("PA-9999", 80, 1, 1, 1, 1, 1), scored("PA-10000", 80, 1, 1, 1, 1, 1)
	if engine.Compare(low, high) >= 0 || engine.Compare(high, low) <= 0 {
		t.Error("item numbers must be compared as numbers")
	}
	if engine.Compare(want[6], want[6]) != 0 {
		t.Error("an outcome must compare equal to itself")
	}
	if engine.Compare(want[0], want[1]) >= 0 || engine.Compare(want[1], want[0]) <= 0 {
		t.Error("Compare is not antisymmetric")
	}
}

// TestRealTieBreak ranks two real records with the same total.
func TestRealTieBreak(t *testing.T) {
	eng := newEngine(t)
	raw := readFixture(t, "pricing/fixtures/build.input.json")
	a := eng.Evaluate(raw)
	more := bytes.Replace(raw, []byte(`"community_demand": 14`), []byte(`"community_demand": 15`), 1)
	if bytes.Equal(more, raw) {
		t.Fatal("the fixture no longer has the value this test changes")
	}
	b := eng.Evaluate(more)
	if *a.Score != *b.Score {
		t.Fatal("the two candidates should have the same score")
	}
	if engine.Compare(b, a) >= 0 {
		t.Error("the candidate with more community demand should sort first")
	}
}

// TestRulePackagesCannotReachOutside reads the imports of every package that
// decides an outcome. None may import the network, a clock, randomness, the
// environment or a subprocess, so the outcome cannot depend on the machine,
// the moment, or a model. This is rule 3 of the build prompt, checked.
func TestRulePackagesCannotReachOutside(t *testing.T) {
	forbidden := []string{"net", "net/http", "time", "math/rand", "math/rand/v2", "crypto/rand", "os", "os/exec", "syscall", "runtime", "plugin", "unsafe"}
	dirs := []string{".", "../contract", "../errs", "../yamlite", "../../tools", "../../tools/rubric-profiles"}
	for _, dir := range dirs {
		pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(info os.FileInfo) bool {
			return !strings.HasSuffix(info.Name(), "_test.go")
		}, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		if len(pkgs) == 0 {
			t.Fatalf("%s: no Go package found", dir)
		}
		for _, pkg := range pkgs {
			for name, file := range pkg.Files {
				for _, imp := range file.Imports {
					path, _ := strconv.Unquote(imp.Path.Value)
					for _, bad := range forbidden {
						if path == bad {
							t.Errorf("%s imports %q; the rule engine must not reach outside the process", name, path)
						}
					}
				}
			}
		}
	}
}
