package rubricprofiles_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
	"github.com/ClearedStack/assay/tools"
	rubricprofiles "github.com/ClearedStack/assay/tools/rubric-profiles"
)

var update = flag.Bool("update", false, "rewrite the golden files from the current behaviour")

// loadFromDisk loads the rules through the registry, from the files in the
// repository rather than the copy compiled into the binary.
func loadFromDisk(t *testing.T) *rubricprofiles.Set {
	t.Helper()
	set, err := tools.Load(os.DirFS(".."))
	if err != nil {
		t.Fatalf("rules did not load: %v", err)
	}
	return set
}

type fixture struct {
	name   string // for example "pricing/build"
	input  string
	golden string
}

// findFixtures returns every saved example: the matching-rule fixtures in
// fixtures/ and each category's own in <category>/fixtures/.
func findFixtures(t *testing.T) []fixture {
	t.Helper()
	inputs, err := filepath.Glob("fixtures/*.input.json")
	if err != nil {
		t.Fatal(err)
	}
	more, err := filepath.Glob("*/fixtures/*.input.json")
	if err != nil {
		t.Fatal(err)
	}
	inputs = append(inputs, more...)
	sort.Strings(inputs)
	var out []fixture
	for _, in := range inputs {
		base := strings.TrimSuffix(in, ".input.json")
		name := strings.Replace(filepath.ToSlash(base), "/fixtures/", "/", 1)
		name = strings.TrimPrefix(name, "fixtures/")
		out = append(out, fixture{name: name, input: in, golden: base + ".golden.json"})
	}
	if len(out) == 0 {
		t.Fatal("no fixtures found")
	}
	return out
}

// TestGolden evaluates every saved input and demands the saved output,
// byte for byte. Any change in behaviour fails here.
func TestGolden(t *testing.T) {
	eng := engine.New(loadFromDisk(t))
	for _, fx := range findFixtures(t) {
		t.Run(fx.name, func(t *testing.T) {
			raw, err := os.ReadFile(fx.input)
			if err != nil {
				t.Fatal(err)
			}
			got := contract.Encode(eng.Evaluate(raw))
			if *update {
				if err := os.WriteFile(fx.golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(fx.golden)
			if err != nil {
				t.Fatalf("no golden file (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("outcome differs from %s\n--- got ---\n%s--- want ---\n%s", fx.golden, got, want)
			}
		})
	}
}

// TestEveryInputHasAGolden catches a golden file left behind without its
// input, or the other way round.
func TestEveryInputHasAGolden(t *testing.T) {
	goldens, _ := filepath.Glob("fixtures/*.golden.json")
	more, _ := filepath.Glob("*/fixtures/*.golden.json")
	goldens = append(goldens, more...)
	inputs := map[string]bool{}
	for _, fx := range findFixtures(t) {
		inputs[fx.golden] = true
		if _, err := os.Stat(fx.golden); err != nil {
			t.Errorf("%s has no golden file", fx.input)
		}
	}
	for _, g := range goldens {
		if !inputs[g] {
			t.Errorf("%s has no input file", g)
		}
	}
}

// TestEveryRuleHasASavedExample is rule 5 of the build prompt made into a
// test: each row of each decision table in each profile must be the deciding
// row in at least one golden file. A row added without a fixture fails here.
func TestEveryRuleHasASavedExample(t *testing.T) {
	set := loadFromDisk(t)
	type key struct {
		category, table string
		rule            int
	}
	hit := map[key]bool{}
	for _, fx := range findFixtures(t) {
		data, err := os.ReadFile(fx.golden)
		if err != nil {
			t.Fatal(err)
		}
		var o contract.Outcome
		if err := json.Unmarshal(data, &o); err != nil {
			t.Fatalf("%s: %v", fx.golden, err)
		}
		for _, g := range o.Gates {
			hit[key{o.Category, g.ID, g.Rule}] = true
		}
		for _, f := range o.Factors {
			hit[key{o.Category, f.ID, f.Rule}] = true
		}
	}
	for _, category := range set.Categories() {
		p := set.Match(category)
		for _, g := range set.Rubric.Gates {
			for rule := 1; rule <= p.GateRows(g.ID); rule++ {
				if !hit[key{category, g.ID, rule}] {
					t.Errorf("%s: gate %s row %d has no saved example", category, g.ID, rule)
				}
			}
		}
		for _, f := range set.Rubric.Factors {
			for rule := 1; rule <= p.FactorRows(f.ID); rule++ {
				if !hit[key{category, f.ID, rule}] {
					t.Errorf("%s: factor %s row %d has no saved example", category, f.ID, rule)
				}
			}
		}
	}
}

// TestEveryProfileShowsEachOutcome makes sure each category has a saved
// example of every kind of result, including the fail-closed ones.
func TestEveryProfileShowsEachOutcome(t *testing.T) {
	set := loadFromDisk(t)
	seen := map[string]map[string]bool{}
	for _, fx := range findFixtures(t) {
		data, err := os.ReadFile(fx.golden)
		if err != nil {
			t.Fatal(err)
		}
		var o contract.Outcome
		if err := json.Unmarshal(data, &o); err != nil {
			t.Fatal(err)
		}
		if seen[o.Category] == nil {
			seen[o.Category] = map[string]bool{}
		}
		switch {
		case o.Error != nil:
			seen[o.Category][string(o.Error.Code)] = true
		case o.State == contract.StateScored:
			seen[o.Category][o.Recommendation] = true
			if o.SplitFlag != nil && *o.SplitFlag {
				seen[o.Category]["split/merge"] = true
			}
		default:
			seen[o.Category][o.State] = true
		}
	}
	want := []string{"build", "link", "park", "reject", "split/merge", contract.StateScreenedOut, "E_NEEDS_SUBTOOL", "E_PARSE"}
	for _, category := range set.Categories() {
		for _, kind := range want {
			if !seen[category][kind] {
				t.Errorf("%s: no saved example of %s", category, kind)
			}
		}
	}
}
