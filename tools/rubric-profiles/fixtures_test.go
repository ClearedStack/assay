package rubricprofiles_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
	"github.com/ClearedStack/assay/internal/errs"
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

// TestGolden evaluates every saved input and demands the saved output,
// byte for byte. Any change in behaviour fails here.
func TestGolden(t *testing.T) {
	eng := engine.New(loadFromDisk(t))
	pairs, _, err := tools.FindFixtures(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, fx := range pairs {
		t.Run(fx.Name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.FromSlash(fx.Input))
			if err != nil {
				t.Fatal(err)
			}
			got := contract.Encode(eng.Evaluate(raw))
			if *update {
				if err := os.WriteFile(filepath.FromSlash(fx.Golden), got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(filepath.FromSlash(fx.Golden))
			if err != nil {
				t.Fatalf("no golden file (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("outcome differs from %s\n--- got ---\n%s--- want ---\n%s", fx.Golden, got, want)
			}
		})
	}
}

// TestRuleSetInvariants runs the shared rule-set verification pass (1-to-1
// fixture pairing, golden match, Logbook check_evaluation compatibility, 100%
// row coverage, and per-category outcome diversity) through tools.VerifySamples
// so go test and `assay check` never drift. Table-hole sampling is run in
// TestDecisionTablesHaveNoHoles.
func TestRuleSetInvariants(t *testing.T) {
	if *update {
		t.Skip("skipping invariant check during -update; re-run without -update")
	}
	_, problems, err := tools.VerifySamples(os.DirFS(".."), 0)
	if err != nil {
		t.Fatalf("rules did not load: %v", err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// TestPrivacyGateTiers proves, for all five profiles, that the passing
// private_by_design row accepts Seen and Designed on emits_tracking and
// connect_src_self and still pauses the item when either is Stated.
func TestPrivacyGateTiers(t *testing.T) {
	eng := engine.New(loadFromDisk(t))
	for _, category := range []string{"pricing", "proposal", "cyber", "market", "devsecops"} {
		raw, err := os.ReadFile(filepath.Join(category, "fixtures", "build.input.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			tier    string
			scored  bool
			pausing bool
		}{
			{"Seen", true, false},
			{"Designed", true, false},
			{"Stated", false, true},
		} {
			for _, fact := range []string{"emits_tracking", "connect_src_self"} {
				t.Run(category+"/"+fact+"/"+tc.tier, func(t *testing.T) {
					var doc map[string]any
					if err := json.Unmarshal(raw, &doc); err != nil {
						t.Fatal(err)
					}
					facts := doc["results"].(map[string]any)["sextant"].(map[string]any)["facts"].(map[string]any)
					facts[fact].(map[string]any)["tier"] = tc.tier
					in, err := json.Marshal(doc)
					if err != nil {
						t.Fatal(err)
					}
					out := eng.Evaluate(in)
					if tc.scored && (out.State != contract.StateScored || !gatePassed(out, "private_by_design")) {
						t.Fatalf("%s %s: want scored with private_by_design passed, got state %q error %v", fact, tc.tier, out.State, out.Error)
					}
					if tc.pausing && (out.State != contract.StateNeedsSubtool || out.Error == nil || out.Error.Code != errs.NeedsSubtool) {
						t.Fatalf("%s %s: want needs_subtool, got state %q error %v", fact, tc.tier, out.State, out.Error)
					}
				})
			}
		}
	}
}

func gatePassed(out *contract.Outcome, gate string) bool {
	for _, g := range out.Gates {
		if g.ID == gate {
			return g.Passed
		}
	}
	return false
}

// designedFacts are the facts about the tool still to be built, the only ones
// that may carry Designed. Sextant emits exactly these as Designed.
var designedFacts = map[string]bool{
	"client_runtime": true, "connect_src_self": true, "emits_tracking": true,
	"est_source_lines": true, "largest_input_kb": true, "needs_account": true,
	"needs_backend": true, "needs_remote_api": true, "output_kinds": true,
	"task_count": true, "works_offline": true,
}

// TestDesignedOnlyOnBuildFacts sets each fact of each profile to Designed in
// turn. The eleven facts about the built tool still score; any other fact
// pauses the item with E_NEEDS_SUBTOOL no_matching_rule, as an unaccepted
// tier does everywhere else.
func TestDesignedOnlyOnBuildFacts(t *testing.T) {
	eng := engine.New(loadFromDisk(t))
	for _, category := range []string{"pricing", "proposal", "cyber", "market", "devsecops"} {
		raw, err := os.ReadFile(filepath.Join(category, "fixtures", "build.input.json"))
		if err != nil {
			t.Fatal(err)
		}
		var base map[string]any
		if err := json.Unmarshal(raw, &base); err != nil {
			t.Fatal(err)
		}
		names := base["results"].(map[string]any)["sextant"].(map[string]any)["facts"].(map[string]any)
		for fact := range names {
			t.Run(category+"/"+fact, func(t *testing.T) {
				var doc map[string]any
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				facts := doc["results"].(map[string]any)["sextant"].(map[string]any)["facts"].(map[string]any)
				facts[fact].(map[string]any)["tier"] = "Designed"
				in, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				out := eng.Evaluate(in)
				if designedFacts[fact] {
					if out.State != contract.StateScored {
						t.Fatalf("Designed %s should score, got state %q error %v", fact, out.State, out.Error)
					}
					return
				}
				if out.State != contract.StateNeedsSubtool || out.Error == nil || out.Error.Reason != errs.NoMatchingRule {
					t.Fatalf("Designed %s should pause with no_matching_rule, got state %q error %v", fact, out.State, out.Error)
				}
			})
		}
	}
}
