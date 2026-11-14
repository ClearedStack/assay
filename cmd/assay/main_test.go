package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ClearedStack/assay/internal/contract"
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
		{"needs-subtool.txt", "pricing/fixtures/needs-subtool-unknown-tier.input.json", exitNeedsSubtool},
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
	if code != exitOK || !strings.Contains(out, "Recommendation: link") {
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
	copyTo := func(src, dst, lane string) {
		data, err := os.ReadFile(filepath.Join(fixtures, src))
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.Replace(string(data), `"lane": "normal"`, `"lane": "`+lane+`"`, 1))
		if err := os.WriteFile(filepath.Join(dir, dst), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyTo("pricing/fixtures/build.input.json", "b.json", "normal")
	copyTo("pricing/fixtures/reject.input.json", "a.json", "normal")
	copyTo("cyber/fixtures/park.input.json", "c.json", "backfill")
	copyTo("market/fixtures/needs-subtool-unknown-fact.input.json", "z.json", "urgent")
	copyTo("proposal/fixtures/parse-wrong-type.input.json", "y.json", "normal")

	code, out, logs := runCLI(t, "", noEnv, "run", "-standin", dir)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, logs)
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	// The urgent item first, then the normal lane in file order, then
	// backfill. A decided item prints the result that was sent: the
	// evaluation with the outcome inside it. The other two print the call
	// that was made, which is all Muster is told.
	sent := func(name string) string {
		var o contract.Outcome
		if err := json.Unmarshal([]byte(read(name)), &o); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return string(contract.Result(&o))
	}
	idOf := func(name string) string {
		var head struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(read(name)), &head); err != nil || head.ID == "" {
			t.Fatalf("%s has no readable id", name)
		}
		return head.ID
	}
	want := "{\n  \"id\": \"" + idOf("market/fixtures/needs-subtool-unknown-fact.input.json") + "\",\n  \"call\": \"needs_subtool\"\n}\n" +
		sent("pricing/fixtures/reject.golden.json") +
		sent("pricing/fixtures/build.golden.json") +
		"{\n  \"id\": \"" + idOf("proposal/fixtures/parse-wrong-type.input.json") + "\",\n  \"call\": \"fail\",\n  \"code\": \"E_PARSE\"\n}\n" +
		sent("cyber/fixtures/park.golden.json")
	if out != want {
		t.Errorf("unexpected output from the stand-in run:\n%s\n--- want ---\n%s", out, want)
	}
	if !strings.Contains(logs, `"reason":"unknown_fact"`) || !strings.Contains(logs, `"reason":"invalid_type"`) {
		t.Errorf("the reasons should be in the log:\n%s", logs)
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
	for _, args := range [][]string{
		{}, {"score"}, {"eval"}, {"eval", "a", "b"}, {"eval", "-format", "xml", "a"},
		{"rank"}, {"rank", "-"}, {"rank", "-format", "xml", "a"},
		{"check", "a", "b"}, {"check", "-nope"},
		{"diff"}, {"diff", "-after", "x"}, {"diff", "-after", "x", "-"}, {"diff", "-after", "x", "-format", "xml", "a"},
		{"run", "extra"}, {"eval", "-nope"},
	} {
		if code, _, _ := runCLI(t, "", noEnv, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	if code, _, errOut := runCLI(t, "", noEnv, "eval", filepath.Join(t.TempDir(), "absent.json")); code != exitFailure || strings.Contains(errOut, "absent") {
		t.Errorf("missing file: exit %d, stderr %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, "", noEnv, "check", filepath.Join(t.TempDir(), "secret-dir")); code != exitFailure || strings.Contains(errOut, "secret-dir") {
		t.Errorf("missing check dir: exit %d, stderr %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, "", noEnv, "diff", "-after", filepath.Join(t.TempDir(), "secret-after"), filepath.Join(fixtures, "pricing/fixtures/build.input.json")); code != exitFailure || strings.Contains(errOut, "secret-after") {
		t.Errorf("missing diff dir: exit %d, stderr %q", code, errOut)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(rel, ".go") {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckAndUpdate(t *testing.T) {
	code, out, errOut := runCLI(t, "", noEnv, "check", "../../tools")
	if code != exitOK || !strings.HasPrefix(out, "ok: 5 categories, ") {
		t.Fatalf("check failed: exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	tmp := filepath.Join(t.TempDir(), "tools")
	copyTree(t, "../../tools", tmp)

	// Corrupt one golden file: check must fail without -update, then heal with -update.
	gold := filepath.Join(tmp, "rubric-profiles/pricing/fixtures/build.golden.json")
	if err := os.WriteFile(gold, []byte("{\"broken\":true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runCLI(t, "", noEnv, "check", tmp)
	if code != exitFailure || !strings.Contains(errOut, "pricing/build: outcome differs") {
		t.Fatalf("expected golden mismatch failure, got exit %d, stderr %q", code, errOut)
	}

	code, out, errOut = runCLI(t, "", noEnv, "check", "-update", tmp)
	if code != exitOK || !strings.HasPrefix(out, "ok: 5 categories, ") {
		t.Fatalf("check -update failed: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestDiffAndCustomRules(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "tools")
	copyTree(t, "../../tools", tmp)

	// Tighten pricing's top need_signals band in the candidate rule copy so
	// pricing/build.input.json (need_signals: 27) drops from rating 4 to 3.
	profPath := filepath.Join(tmp, "rubric-profiles/pricing/profile.yaml")
	profBytes, err := os.ReadFile(profPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(
		string(profBytes),
		"when: {need_signals: {gte: 15, lte: 29}}\n      rating: 4",
		"when: {need_signals: {gte: 15, lte: 29}}\n      rating: 3",
		1,
	)
	if edited == string(profBytes) {
		t.Fatal("target rule line not found in pricing/profile.yaml")
	}
	if err := os.WriteFile(profPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	buildInput := filepath.Join(fixtures, "pricing/fixtures/build.input.json")
	parkInput := filepath.Join(fixtures, "pricing/fixtures/park.input.json")

	// eval -rules uses the modified directory (score drops by 4 points: 86 -> 82).
	code, out, errOut := runCLI(t, "", noEnv, "eval", "-rules", tmp, buildInput)
	if code != exitOK || !strings.Contains(out, "82 / 100") {
		t.Fatalf("eval -rules unexpected: exit %d, out:\n%s\nstderr:\n%s", code, out, errOut)
	}

	// rank -rules -format json produces valid ranked JSON.
	code, rankJSON, errOut := runCLI(t, "", noEnv, "rank", "-rules", tmp, "-format", "json", parkInput, buildInput)
	if code != exitOK {
		t.Fatalf("rank -format json failed: exit %d, stderr %q", code, errOut)
	}
	var ranked []rankedEntry
	if err := json.Unmarshal([]byte(rankJSON), &ranked); err != nil || len(ranked) != 2 || ranked[0].Score == nil || *ranked[0].Score != 82 {
		t.Fatalf("unexpected rank json: %v\n%s", err, rankJSON)
	}

	// diff in text mode reports 1 changed and 1 unchanged item.
	code, diffText, errOut := runCLI(t, "", noEnv, "diff", "-after", tmp, parkInput, buildInput)
	if code != exitOK || !strings.Contains(diffText, "Evaluated: 2, Changed: 1, Unchanged: 1") || !strings.Contains(diffText, "factor.need") {
		t.Fatalf("diff text unexpected: exit %d, out:\n%s\nstderr:\n%s", code, diffText, errOut)
	}

	// diff in json mode reports exact structured delta.
	code, diffJSON, errOut := runCLI(t, "", noEnv, "diff", "-before", "../../tools", "-after", tmp, "-format", "json", buildInput, parkInput)
	if code != exitOK {
		t.Fatalf("diff json failed: exit %d, stderr %q", code, errOut)
	}
	var rep diffReport
	if err := json.Unmarshal([]byte(diffJSON), &rep); err != nil || rep.Evaluated != 2 || rep.Changed != 1 || rep.Unchanged != 1 {
		t.Fatalf("unexpected diff json: %v\n%s", err, diffJSON)
	}
}
