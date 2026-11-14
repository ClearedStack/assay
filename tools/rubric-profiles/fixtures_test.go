package rubricprofiles_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
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
