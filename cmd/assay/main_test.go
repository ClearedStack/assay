package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files from the current behaviour")

const fixtures = "../../tools/rubric-profiles"

func noEnv(string) string { return "" }

func runCLI(t *testing.T, stdin string, env func(string) string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(stdin), &out, &errOut, env)
	return code, out.String(), errOut.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden file (run with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s\n--- got ---\n%s--- want ---\n%s", path, got, want)
	}
}

// TestEvalText is the first milestone: one record in, and out come the
// result of each gate, the seven ratings, the total and the recommendation.
// There is a saved example for a build, a link, a park and a reject, and
// for each way an item can stop short.
func TestEvalText(t *testing.T) {
	cases := []struct {
		golden string
		input  string
		code   int
	}{
		{"build.txt", "pricing/fixtures/build.input.json", exitOK},
		{"link.txt", "pricing/fixtures/link.input.json", exitOK},
		{"park.txt", "pricing/fixtures/park.input.json", exitOK},
		{"reject.txt", "pricing/fixtures/reject.input.json", exitOK},
		{"split-merge.txt", "pricing/fixtures/build-split-merge.input.json", exitOK},
		{"screened-out.txt", "pricing/fixtures/screened-out-all-gates.input.json", exitOK},
		{"needs-subtool.txt", "pricing/fixtures/needs-subtool-unlisted-verb.input.json", exitNeedsSubtool},
		{"unknown-category.txt", "fixtures/needs-subtool-unknown-category.input.json", exitNeedsSubtool},
		{"parse.txt", "pricing/fixtures/parse-wrong-type.input.json", exitParse},
		{"malformed.txt", "fixtures/parse-malformed-json.input.json", exitParse},
	}
	for _, tc := range cases {
		t.Run(tc.golden, func(t *testing.T) {
			code, out, errOut := runCLI(t, "", noEnv, "eval", filepath.Join(fixtures, tc.input))
			if code != tc.code {
				t.Errorf("exit code %d, want %d (stderr: %s)", code, tc.code, errOut)
			}
			golden(t, tc.golden, out)
		})
	}
}

func TestEvalJSONIsTheGoldenOutcome(t *testing.T) {
	for _, name := range []string{"pricing/fixtures/build", "cyber/fixtures/screened-out-native", "fixtures/parse-empty"} {
		_, out, _ := runCLI(t, "", noEnv, "eval", "-format", "json", filepath.Join(fixtures, name+".input.json"))
		want, err := os.ReadFile(filepath.Join(fixtures, name+".golden.json"))
		if err != nil {
			t.Fatal(err)
		}
		if out != string(want) {
			t.Errorf("%s: -format json differs from the golden file", name)
		}
	}
}

func TestEvalReadsStandardInput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "market/fixtures/link.input.json"))
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI(t, string(raw), noEnv, "eval", "-")
	if code != exitOK || !strings.Contains(out, "Recommendation: Link") {
		t.Errorf("exit %d, output:\n%s", code, out)
	}
}

func TestRank(t *testing.T) {
	code, out, _ := runCLI(t, "", noEnv, "rank",
		filepath.Join(fixtures, "pricing/fixtures/park.input.json"),
		filepath.Join(fixtures, "pricing/fixtures/screened-out-one-gate.input.json"),
		filepath.Join(fixtures, "market/fixtures/build.input.json"),
		filepath.Join(fixtures, "pricing/fixtures/build.input.json"),
		filepath.Join(fixtures, "proposal/fixtures/build-split-merge.input.json"),
		filepath.Join(fixtures, "fixtures/parse-empty.input.json"),
		filepath.Join(fixtures, "pricing/fixtures/build-split-merge.input.json"),
	)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	golden(t, "rank.txt", out)

	// The order of the arguments must not change the ranking.
	_, again, _ := runCLI(t, "", noEnv, "rank",
		filepath.Join(fixtures, "pricing/fixtures/build-split-merge.input.json"),
		filepath.Join(fixtures, "fixtures/parse-empty.input.json"),
		filepath.Join(fixtures, "proposal/fixtures/build-split-merge.input.json"),
		filepath.Join(fixtures, "pricing/fixtures/build.input.json"),
		filepath.Join(fixtures, "market/fixtures/build.input.json"),
		filepath.Join(fixtures, "pricing/fixtures/screened-out-one-gate.input.json"),
		filepath.Join(fixtures, "pricing/fixtures/park.input.json"),
	)
	if again != out {
		t.Errorf("the ranking depends on the order of the arguments:\n%s\n%s", out, again)
	}
}

func TestRunWithStandIn(t *testing.T) {
	dir := t.TempDir()
	copyTo := func(src, dst string) {
		data, err := os.ReadFile(filepath.Join(fixtures, src))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, dst)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, dst), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyTo("pricing/fixtures/build.input.json", "b.json")
	copyTo("pricing/fixtures/reject.input.json", "a.json")
	copyTo("cyber/fixtures/park.input.json", "backfill/z.json")
	copyTo("market/fixtures/needs-subtool-unknown-fact.input.json", "urgent/u.json")

	code, out, logs := runCLI(t, "", noEnv, "run", "-standin", dir)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, logs)
	}
	var want strings.Builder
	for _, name := range []string{"market/fixtures/needs-subtool-unknown-fact", "pricing/fixtures/reject", "pricing/fixtures/build", "cyber/fixtures/park"} {
		data, err := os.ReadFile(filepath.Join(fixtures, name+".golden.json"))
		if err != nil {
			t.Fatal(err)
		}
		want.Write(data)
	}
	if out != want.String() {
		t.Errorf("the stand-in run should print the golden outcomes, urgent lane first:\n%s", out)
	}

	if code, _, _ := runCLI(t, "", noEnv, "run", "-standin", t.TempDir()); code != exitFailure {
		t.Errorf("an empty folder should be an error, got exit %d", code)
	}
}

func TestRunRefusesBadConfigurationWithoutEchoingIt(t *testing.T) {
	env := func(pairs ...string) func(string) string {
		return func(key string) string {
			for i := 0; i < len(pairs); i += 2 {
				if pairs[i] == key {
					return pairs[i+1]
				}
			}
			return ""
		}
	}
	spoolDir := t.TempDir()
	cases := map[string]func(string) string{
		"no hub address":       env(),
		"other contract":       env("CONTRACT_VERSION", "v2", "MUSTER_URL", "http://hub.invalid"),
		"address with a login": env("MUSTER_URL", "http://someone:hunter2@hub.invalid", "ASSAY_SPOOL_DIR", spoolDir),
		"address not http":     env("MUSTER_URL", "hub.invalid:8080", "ASSAY_SPOOL_DIR", spoolDir),
		"bad interval":         env("MUSTER_URL", "http://hub.invalid", "ASSAY_SPOOL_DIR", spoolDir, "ASSAY_POLL_INTERVAL", "soon"),
		"zero timeout":         env("MUSTER_URL", "http://hub.invalid", "ASSAY_SPOOL_DIR", spoolDir, "ASSAY_HUB_TIMEOUT", "0s"),
		"spool not writable":   env("MUSTER_URL", "http://hub.invalid", "ASSAY_SPOOL_DIR", "/proc/assay-cannot-write-here"),
	}
	for name, e := range cases {
		code, out, errOut := runCLI(t, "", e, "run")
		if code != exitFailure {
			t.Errorf("%s: exit %d, want %d", name, code, exitFailure)
		}
		for _, leak := range []string{"hunter2", "hub.invalid", "someone", "/proc/"} {
			if strings.Contains(out+errOut, leak) {
				t.Errorf("%s: the message echoes %q: %s", name, leak, errOut)
			}
		}
	}
}

func TestUsageAndVersion(t *testing.T) {
	if code, out, _ := runCLI(t, "", noEnv, "version"); code != exitOK || out != "assay v0.1.0-draft\n" {
		t.Errorf("version: exit %d, %q", code, out)
	}
	if code, out, _ := runCLI(t, "", noEnv, "help"); code != exitOK || !strings.Contains(out, "assay eval") {
		t.Errorf("help: exit %d", code)
	}
	for _, args := range [][]string{{}, {"score"}, {"eval"}, {"eval", "a", "b"}, {"eval", "-format", "xml", "a"}, {"rank"}, {"run", "extra"}, {"eval", "-nope"}} {
		if code, _, _ := runCLI(t, "", noEnv, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	if code, _, errOut := runCLI(t, "", noEnv, "eval", filepath.Join(t.TempDir(), "absent.json")); code != exitFailure || strings.Contains(errOut, "absent") {
		t.Errorf("missing file: exit %d, stderr %q", code, errOut)
	}
}
