package tools

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func dirFSForTest(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(".")
}

// withRegistry returns the embedded rules with registry.yaml replaced.
func withRegistry(t *testing.T, registry string) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(embedded, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(embedded, path)
		out[path] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	out["registry.yaml"] = &fstest.MapFile{Data: []byte(registry)}
	return out
}

const entry = `
  - name: rubric-profiles
    path: rubric-profiles
    responsibility: test
    parts: {match: m, logic: l, fixtures: f}
`

func TestEmbeddedRulesLoad(t *testing.T) {
	set, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Categories()) != 5 {
		t.Errorf("want the five categories SPEC.md names, got %v", set.Categories())
	}
}

// TestOnlyRulesAreEmbedded keeps fixtures and tests out of the binary.
func TestOnlyRulesAreEmbedded(t *testing.T) {
	err := fs.WalkDir(embedded, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".yaml") || strings.Contains(path, "fixtures") {
			t.Errorf("%s should not be embedded", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistryMustAgreeWithTheBuild(t *testing.T) {
	cases := map[string]struct {
		registry string
		want     string
	}{
		"as shipped":            {"sub_tools:" + entry, ""},
		"sub-tool not listed":   {"sub_tools: []\n", "is not listed"},
		"empty file":            {"", "one document"},
		"sub-tool with no code": {"sub_tools:" + entry + strings.Replace(entry, "rubric-profiles", "rate-cards", 2), "no code for sub-tool"},
		"listed twice":          {"sub_tools:" + entry + entry, "listed twice"},
		"a part is missing":     {"sub_tools:" + strings.Replace(entry, "fixtures: f", "fixtures: ''", 1), "matching rule, logic and fixtures"},
		"folder does not exist": {"sub_tools:" + strings.Replace(entry, "path: rubric-profiles", "path: elsewhere", 1), "elsewhere"},
		"unknown key":           {"sub_tools:" + entry + "extra: true\n", "extra"},
		"not YAML":              {"sub_tools: [", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(withRegistry(t, tc.registry))
			if name == "as shipped" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
	if _, err := Load(fstest.MapFS{}); err == nil {
		t.Error("loading with no registry should fail")
	}
}

func TestVerifyRuleSet(t *testing.T) {
	disk := dirFSForTest(t)
	sum, problems, err := VerifySamples(disk, 4000)
	if err != nil {
		t.Fatalf("VerifySamples failed: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if sum.Categories != 5 || sum.Fixtures < 138 || sum.Rules < 100 {
		t.Errorf("unexpected summary: %+v", sum)
	}

	// Copy into MapFS and corrupt one golden plus add an orphan golden to
	// verify problem reporting.
	corrupt := fstest.MapFS{}
	err = fs.WalkDir(disk, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, ".go") {
			return err
		}
		data, err := fs.ReadFile(disk, path)
		corrupt[path] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	corrupt["rubric-profiles/pricing/fixtures/build.golden.json"] = &fstest.MapFile{Data: []byte("{}\n")}
	corrupt["rubric-profiles/pricing/fixtures/orphan.golden.json"] = &fstest.MapFile{Data: []byte("{}\n")}

	_, problems, err = VerifySamples(corrupt, 400)
	if err != nil {
		t.Fatalf("expected problem report rather than load error: %v", err)
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "pricing/build: outcome differs") || !strings.Contains(joined, "orphan.golden.json has no input file") {
		t.Errorf("expected mismatch and orphan problems, got:\n%s", joined)
	}
}
