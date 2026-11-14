package tools

import (
	"bytes"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
	rubricprofiles "github.com/ClearedStack/assay/tools/rubric-profiles"
)

// Summary counts the rule artifacts checked during a Verify pass.
type Summary struct {
	Categories int
	Fixtures   int
	Rules      int
}

// FixturePair names one saved input and its companion golden file inside the
// rubric-profiles sub-tool directory.
type FixturePair struct {
	Name   string // for example "pricing/build"
	Input  string // path inside rubric-profiles, for example "pricing/fixtures/build.input.json"
	Golden string // path inside rubric-profiles, for example "pricing/fixtures/build.golden.json"
}

// SubToolPath returns the relative path of the rubric-profiles sub-tool from
// registry.yaml in fsys.
func SubToolPath(fsys fs.FS) (string, error) {
	data, err := fs.ReadFile(fsys, "registry.yaml")
	if err != nil {
		return "", fmt.Errorf("registry.yaml: %w", err)
	}
	entries, err := readRegistry(data)
	if err != nil {
		return "", fmt.Errorf("registry.yaml: %w", err)
	}
	for _, st := range entries {
		if st.name == rubricprofiles.Name {
			return st.path, nil
		}
	}
	return "", fmt.Errorf("registry.yaml: sub-tool %s is not listed", rubricprofiles.Name)
}

// FindFixtures lists every *.input.json fixture in sub (rooted at
// rubric-profiles) in deterministic name order, along with any orphan golden
// files that have no companion input.
func FindFixtures(sub fs.FS) (pairs []FixturePair, problems []string, err error) {
	inputs, err := fs.Glob(sub, "fixtures/*.input.json")
	if err != nil {
		return nil, nil, err
	}
	moreIn, err := fs.Glob(sub, "*/fixtures/*.input.json")
	if err != nil {
		return nil, nil, err
	}
	inputs = append(inputs, moreIn...)
	sort.Strings(inputs)

	goldens, err := fs.Glob(sub, "fixtures/*.golden.json")
	if err != nil {
		return nil, nil, err
	}
	moreGold, err := fs.Glob(sub, "*/fixtures/*.golden.json")
	if err != nil {
		return nil, nil, err
	}
	goldens = append(goldens, moreGold...)
	sort.Strings(goldens)

	if len(inputs) == 0 {
		return nil, []string{"no fixtures found"}, nil
	}

	knownGolden := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		base := strings.TrimSuffix(in, ".input.json")
		name := strings.Replace(base, "/fixtures/", "/", 1)
		name = strings.TrimPrefix(name, "fixtures/")
		gold := base + ".golden.json"
		knownGolden[gold] = true
		pairs = append(pairs, FixturePair{Name: name, Input: in, Golden: gold})
		if _, statErr := fs.Stat(sub, gold); statErr != nil {
			problems = append(problems, fmt.Sprintf("%s has no golden file", in))
		}
	}
	for _, g := range goldens {
		if !knownGolden[g] {
			problems = append(problems, fmt.Sprintf("%s has no input file", g))
		}
	}
	return pairs, problems, nil
}

// Verify loads fsys and checks all rule, fixture, Logbook compatibility,
// row-coverage, outcome-diversity, and table-completeness invariants.
func Verify(fsys fs.FS) (Summary, []string, error) {
	return VerifySamples(fsys, rubricprofiles.DefaultCompletenessSamples)
}

// VerifySamples is Verify with an explicit generated-candidate sample count per
// category.
func VerifySamples(fsys fs.FS, samples int) (Summary, []string, error) {
	var sum Summary
	set, err := Load(fsys)
	if err != nil {
		return sum, nil, err
	}
	subPath, err := SubToolPath(fsys)
	if err != nil {
		return sum, nil, err
	}
	sub, err := fs.Sub(fsys, subPath)
	if err != nil {
		return sum, nil, err
	}

	pairs, problems, err := FindFixtures(sub)
	if err != nil {
		return sum, nil, err
	}

	categories := set.Categories()
	sum.Categories = len(categories)
	sum.Fixtures = len(pairs)

	eng := engine.New(set)
	type rowKey struct {
		category, table string
		rule            int
	}
	hit := map[rowKey]bool{}
	seen := map[string]map[string]bool{}

	for _, fx := range pairs {
		raw, rerr := fs.ReadFile(sub, fx.Input)
		if rerr != nil {
			problems = append(problems, fmt.Sprintf("%s: input could not be read", fx.Name))
			continue
		}
		out := eng.Evaluate(raw)
		got := contract.Encode(out)

		want, gerr := fs.ReadFile(sub, fx.Golden)
		if gerr == nil && !bytes.Equal(got, want) {
			problems = append(problems, fmt.Sprintf("%s: outcome differs from %s", fx.Name, fx.Golden))
		}

		if out.Error == nil {
			if cerr := contract.CheckEvaluation(got); cerr != nil {
				problems = append(problems, fmt.Sprintf("%s: Logbook evaluation check failed (%s at %s)", fx.Name, cerr.Reason, cerr.Where))
			}
		}

		for _, g := range out.Gates {
			hit[rowKey{out.Category, g.ID, g.Rule}] = true
		}
		for _, f := range out.Factors {
			hit[rowKey{out.Category, f.ID, f.Rule}] = true
		}

		if seen[out.Category] == nil {
			seen[out.Category] = map[string]bool{}
		}
		switch {
		case out.Error != nil:
			seen[out.Category][string(out.Error.Code)] = true
		case out.State == contract.StateScored:
			seen[out.Category][out.Recommendation] = true
			if out.SplitFlag != nil && *out.SplitFlag {
				seen[out.Category]["split/merge"] = true
			}
		default:
			seen[out.Category][out.State] = true
		}
	}

	for _, category := range categories {
		p := set.Match(category)
		for _, g := range set.Rubric.Gates {
			rows := p.GateRows(g.ID)
			sum.Rules += rows
			for rule := 1; rule <= rows; rule++ {
				if !hit[rowKey{category, g.ID, rule}] {
					problems = append(problems, fmt.Sprintf("%s: gate %s row %d has no saved example", category, g.ID, rule))
				}
			}
		}
		for _, f := range set.Rubric.Factors {
			rows := p.FactorRows(f.ID)
			sum.Rules += rows
			for rule := 1; rule <= rows; rule++ {
				if !hit[rowKey{category, f.ID, rule}] {
					problems = append(problems, fmt.Sprintf("%s: factor %s row %d has no saved example", category, f.ID, rule))
				}
			}
		}
	}

	wantOutcomes := []string{"build", "link", "park", "reject", "split/merge", contract.StateScreenedOut, "E_NEEDS_SUBTOOL", "E_PARSE"}
	for _, category := range categories {
		for _, kind := range wantOutcomes {
			if !seen[category][kind] {
				problems = append(problems, fmt.Sprintf("%s: no saved example of %s", category, kind))
			}
		}
	}

	problems = append(problems, rubricprofiles.CheckCompleteness(set, samples)...)
	return sum, problems, nil
}
