package engine_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
	"github.com/ClearedStack/assay/internal/errs"
	rubricprofiles "github.com/ClearedStack/assay/tools/rubric-profiles"
)

func FuzzEvaluate(f *testing.F) {
	set, err := rubricprofiles.Load(os.DirFS("../../tools/rubric-profiles"))
	if err != nil {
		f.Fatal(err)
	}
	eng := engine.New(set)

	seedGlobs := []string{
		"../../tools/rubric-profiles/fixtures/*.input.json",
		"../../tools/rubric-profiles/*/fixtures/build*.input.json",
		"../../tools/rubric-profiles/*/fixtures/screened-out-one-gate.input.json",
		"../../tools/rubric-profiles/*/fixtures/needs-subtool-*.input.json",
		"../../tools/rubric-profiles/*/fixtures/parse-*.input.json",
	}
	for _, pattern := range seedGlobs {
		paths, _ := filepath.Glob(pattern)
		for _, p := range paths {
			if data, err := os.ReadFile(p); err == nil {
				f.Add(data)
			}
		}
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		out1 := eng.Evaluate(raw)
		out2 := eng.Evaluate(raw)

		enc1 := contract.Encode(out1)
		enc2 := contract.Encode(out2)
		if !bytes.Equal(enc1, enc2) {
			t.Fatalf("Evaluate output is not byte-for-byte deterministic")
		}

		if out1.Error != nil {
			wantState := ""
			if out1.Error.Code == errs.NeedsSubtool {
				wantState = contract.StateNeedsSubtool
			}
			if out1.State != wantState {
				t.Fatalf("errored outcome (%s) carried state %q, want %q", out1.Error.Code, out1.State, wantState)
			}
			if out1.Score != nil || out1.Recommendation != "" || out1.SplitFlag != nil {
				t.Fatalf("errored outcome carried partial score or recommendation")
			}
			if len(out1.Gates) != 0 || len(out1.Factors) != 0 || len(out1.TieBreakers) != 0 {
				t.Fatalf("errored outcome carried partial verdicts or tie-breakers")
			}
			return
		}

		if cerr := contract.CheckEvaluation(enc1); cerr != nil {
			t.Fatalf("valid engine outcome failed CheckEvaluation: %v\n%s", cerr, enc1)
		}

		switch out1.State {
		case contract.StateScored:
			if out1.Score == nil || *out1.Score < 20 || *out1.Score > 100 {
				t.Fatalf("scored outcome score out of 20..100 bounds: %v", out1.Score)
			}
			if out1.Recommendation == "" || out1.SplitFlag == nil {
				t.Fatalf("scored outcome missing recommendation or split_flag")
			}
		case contract.StateScreenedOut:
			if out1.Score != nil || out1.Recommendation != "" || out1.SplitFlag != nil || len(out1.Factors) != 0 {
				t.Fatalf("screened_out outcome carried scored fields")
			}
		default:
			t.Fatalf("unexpected non-error state %q", out1.State)
		}
	})
}
