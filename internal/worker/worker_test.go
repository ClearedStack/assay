package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ClearedStack/assay/internal/engine"
	"github.com/ClearedStack/assay/internal/errs"
	"github.com/ClearedStack/assay/internal/muster"
	"github.com/ClearedStack/assay/internal/spool"
	"github.com/ClearedStack/assay/tools"
)

const fixtures = "../../tools/rubric-profiles"

var ctx = context.Background()

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// inLane returns a fixture's lease reply moved to another lane.
func inLane(t *testing.T, name, lane string) []byte {
	t.Helper()
	raw := fixture(t, name)
	out := bytes.Replace(raw, []byte(`"lane": "normal"`), []byte(`"lane": "`+lane+`"`), 1)
	if bytes.Equal(out, raw) {
		t.Fatalf("%s names no lane to change", name)
	}
	return out
}

// itemOf reads the item identifier out of a fixture.
func itemOf(t *testing.T, name string) string {
	t.Helper()
	var head struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(fixture(t, name), &head); err != nil || head.ID == "" {
		t.Fatalf("%s has no readable id", name)
	}
	return head.ID
}

// compact strips the layout from JSON, which is what happens to a result
// on its way to the hub inside a request body.
func compact(t *testing.T, data []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// outcomeIn takes Assay's own outcome out of a result sent to the hub.
func outcomeIn(t *testing.T, result []byte) []byte {
	t.Helper()
	var r struct {
		Outcome json.RawMessage `json:"outcome"`
	}
	if err := json.Unmarshal(result, &r); err != nil || r.Outcome == nil {
		t.Fatalf("result has no outcome: %s", result)
	}
	return r.Outcome
}

type rig struct {
	hub   *muster.Memory
	spool *spool.Spool
	logs  *bytes.Buffer
	w     *Worker
}

func newRig(t *testing.T) *rig {
	t.Helper()
	set, err := tools.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	sp, err := spool.Open(filepath.Join(t.TempDir(), "cs"))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{hub: muster.NewMemory(), spool: sp, logs: &bytes.Buffer{}}
	r.w = &Worker{
		Hub: r.hub, Engine: engine.New(set), Spool: sp, Log: slog.New(slog.NewJSONHandler(r.logs, nil)),
		HubTimeout: time.Second, PollInterval: time.Millisecond, BackoffMin: time.Millisecond, BackoffMax: 4 * time.Millisecond,
	}
	return r
}

func (r *rig) drain() {
	for r.w.Step(ctx) == Worked {
	}
}

func items(done []muster.Completion) string {
	var ids []string
	for _, c := range done {
		ids = append(ids, c.ItemID)
	}
	return strings.Join(ids, ",")
}

func timeout() error { return errs.New(errs.Timeout, errs.HubTimeout, "hub") }

func TestItemsAreWorkedUrgentThenNormalThenBackfill(t *testing.T) {
	r := newRig(t)
	// Queued in the wrong order on purpose.
	r.hub.Add(inLane(t, "market/fixtures/build.input.json", "backfill"))
	r.hub.Add(fixture(t, "pricing/fixtures/park.input.json"))
	r.hub.Add(fixture(t, "pricing/fixtures/reject.input.json"))
	r.hub.Add(inLane(t, "cyber/fixtures/build.input.json", "urgent"))

	if got := r.w.Step(ctx); got != Worked {
		t.Fatalf("first step: %v", got)
	}
	// An urgent item arriving mid-run jumps the queue.
	r.hub.Add(inLane(t, "proposal/fixtures/link.input.json", "urgent"))
	r.drain()

	want := []string{
		itemOf(t, "cyber/fixtures/build.input.json"), itemOf(t, "proposal/fixtures/link.input.json"),
		itemOf(t, "pricing/fixtures/park.input.json"), itemOf(t, "pricing/fixtures/reject.input.json"),
		itemOf(t, "market/fixtures/build.input.json"),
	}
	if got := items(r.hub.Completions()); got != strings.Join(want, ",") {
		t.Errorf("order: got %s, want %v", got, want)
	}
	if r.w.Step(ctx) != Idle {
		t.Error("with nothing queued the worker should be idle")
	}
}

// TestEachOutcomeBecomesTheRightCall is the heart of the contract: a
// decided item is completed to scored or screened_out with the golden
// outcome as its result; an unfamiliar one is paused with needs_subtool; a
// broken one is failed with E_PARSE.
func TestEachOutcomeBecomesTheRightCall(t *testing.T) {
	r := newRig(t)
	cases := []struct {
		name  string
		call  muster.Call
		state string
		code  errs.Code
	}{
		{"pricing/fixtures/build", muster.CallComplete, "scored", ""},
		{"pricing/fixtures/link", muster.CallComplete, "scored", ""},
		{"pricing/fixtures/park", muster.CallComplete, "scored", ""},
		{"pricing/fixtures/reject", muster.CallComplete, "scored", ""},
		{"pricing/fixtures/screened-out-all-gates", muster.CallComplete, "screened_out", ""},
		{"fixtures/matched-other-results-ignored", muster.CallComplete, "scored", ""},
		{"pricing/fixtures/needs-subtool-unknown-fact", muster.CallNeedsSubtool, "needs_subtool", ""},
		{"pricing/fixtures/needs-subtool-unknown-tier", muster.CallNeedsSubtool, "needs_subtool", ""},
		{"pricing/fixtures/needs-subtool-unverified-privacy", muster.CallNeedsSubtool, "needs_subtool", ""},
		{"fixtures/needs-subtool-unknown-category", muster.CallNeedsSubtool, "needs_subtool", ""},
		{"fixtures/needs-subtool-no-category", muster.CallNeedsSubtool, "needs_subtool", ""},
		{"fixtures/needs-subtool-unknown-field", muster.CallNeedsSubtool, "needs_subtool", ""},
		{"pricing/fixtures/parse-wrong-type", muster.CallFail, "", errs.Parse},
		{"fixtures/parse-wrong-state", muster.CallFail, "", errs.Parse},
		{"fixtures/parse-missing-observation", muster.CallFail, "", errs.Parse},
		{"fixtures/parse-fraction", muster.CallFail, "", errs.Parse},
	}
	for _, tc := range cases {
		r.hub.Add(fixture(t, tc.name+".input.json"))
	}
	r.drain()
	done := r.hub.Completions()
	if len(done) != len(cases) {
		t.Fatalf("want %d closing calls, got %d\n%s", len(cases), len(done), r.logs)
	}
	byItem := map[string]muster.Completion{}
	for _, c := range done {
		byItem[c.ItemID] = c
	}
	for _, tc := range cases {
		got, ok := byItem[itemOf(t, tc.name+".input.json")]
		if !ok {
			t.Errorf("%s: no closing call was made", tc.name)
			continue
		}
		if got.Call != tc.call || got.State != tc.state || got.Code != tc.code {
			t.Errorf("%s: got %s state %q code %q, want %s state %q code %q", tc.name, got.Call, got.State, got.Code, tc.call, tc.state, tc.code)
		}
		if tc.call == muster.CallComplete {
			if compact(t, outcomeIn(t, got.Result)) != compact(t, fixture(t, tc.name+".golden.json")) {
				t.Errorf("%s: the result sent to the hub is not the golden outcome", tc.name)
			}
		} else if len(got.Result) != 0 {
			t.Errorf("%s: only a complete call carries a result", tc.name)
		}
	}
	if r.hub.Paused() != 6 {
		t.Errorf("six items should be paused, got %d", r.hub.Paused())
	}
	// The detail Muster's fail and needs_subtool calls cannot carry is in
	// Assay's own log line.
	for _, want := range []string{`"reason":"unknown_category"`, `"reason":"no_matching_rule"`, `"where":"one_job"`, `"where":"private_by_design"`, `"reason":"invalid_type"`, `"fingerprint":"`} {
		if !strings.Contains(r.logs.String(), want) {
			t.Errorf("the log should carry %s", want)
		}
	}
}

// TestAReplyWithNoReadableLeaseCannotBeAnswered covers the one case with no
// call to make: without a lease there is nothing to fail. The worker backs
// off and carries on; Muster offers the item again when its lease runs out.
func TestAReplyWithNoReadableLeaseCannotBeAnswered(t *testing.T) {
	r := newRig(t)
	r.hub.Add(fixture(t, "fixtures/parse-malformed-json.input.json"))
	r.hub.Add(fixture(t, "pricing/fixtures/build.input.json"))
	if got := r.w.Step(ctx); got != Retry {
		t.Fatalf("got %v", got)
	}
	if !strings.Contains(r.logs.String(), `"reason":"hub_reply_invalid"`) {
		t.Errorf("the unreadable reply should be logged:\n%s", r.logs)
	}
	r.drain()
	if got := items(r.hub.Completions()); got != itemOf(t, "pricing/fixtures/build.input.json") {
		t.Errorf("the rest of the queue should carry on, got %q", got)
	}
}

// TestHubTimeoutIsSpooledAndRetried is the E_TIMEOUT row of the failure
// table: the finished report is kept on disk and delivered once the hub
// answers again, and nothing new is leased in the meantime. It holds for
// all three closing calls.
func TestHubTimeoutIsSpooledAndRetried(t *testing.T) {
	for _, name := range []string{"pricing/fixtures/build", "pricing/fixtures/needs-subtool-unknown-fact", "pricing/fixtures/parse-wrong-type"} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.hub.Add(fixture(t, name+".input.json"))
			r.hub.Add(fixture(t, "pricing/fixtures/park.input.json"))
			r.hub.FailSend(3, timeout())

			if got := r.w.Step(ctx); got != Retry {
				t.Fatalf("a failed report should ask for a retry, got %v", got)
			}
			if entries, _ := r.spool.List(); len(entries) != 1 {
				t.Fatalf("the finished report should be in the spool; got %d entries", len(entries))
			}
			// The hub is still failing: the entry stays, and no new item is
			// taken.
			for i := 0; i < 2; i++ {
				if got := r.w.Step(ctx); got != Retry {
					t.Fatalf("retry %d: got %v", i, got)
				}
			}
			if r.hub.Waiting() != 1 || len(r.hub.Completions()) != 0 {
				t.Fatalf("while a report is waiting no new work may be leased: waiting %d, completed %d", r.hub.Waiting(), len(r.hub.Completions()))
			}

			// The hub recovers. The spooled report goes first, unchanged.
			r.drain()
			want := itemOf(t, name+".input.json") + "," + itemOf(t, "pricing/fixtures/park.input.json")
			if got := items(r.hub.Completions()); got != want {
				t.Errorf("completions: got %s, want %s", got, want)
			}
			if first := r.hub.Completions()[0]; first.Call == muster.CallComplete &&
				compact(t, outcomeIn(t, first.Result)) != compact(t, fixture(t, name+".golden.json")) {
				t.Error("the spooled result changed on its way through the spool")
			}
			if left, _ := r.spool.List(); len(left) != 0 {
				t.Errorf("the spool should be empty, has %d", len(left))
			}
			if !strings.Contains(r.logs.String(), `"code":"E_TIMEOUT"`) || !strings.Contains(r.logs.String(), "report_spooled") {
				t.Errorf("the timeout should be logged with its code:\n%s", r.logs)
			}
		})
	}
}

// TestSpoolSurvivesARestart stops the worker with a report waiting and
// starts a fresh one over the same folder, as happens when the pod's
// container restarts.
func TestSpoolSurvivesARestart(t *testing.T) {
	r := newRig(t)
	r.hub.Add(fixture(t, "pricing/fixtures/build.input.json"))
	r.hub.FailSend(1, timeout())
	r.w.Step(ctx)

	fresh := *r.w
	fresh.Log = slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	if got := fresh.Step(ctx); got != Idle {
		t.Fatalf("the fresh worker should deliver the spooled report and then find nothing to do, got %v", got)
	}
	if got := items(r.hub.Completions()); got != itemOf(t, "pricing/fixtures/build.input.json") {
		t.Errorf("completions: %v", got)
	}
}

func TestLeaseTimeoutBacksOffAndLosesNothing(t *testing.T) {
	r := newRig(t)
	r.hub.Add(fixture(t, "pricing/fixtures/build.input.json"))
	r.hub.FailLease(2, timeout())
	for i := 0; i < 2; i++ {
		if r.w.Step(ctx) != Retry {
			t.Fatal("a failed lease should ask for a retry")
		}
	}
	if r.w.Step(ctx) != Worked {
		t.Fatal("the item should be leased once the hub answers")
	}
	if len(r.hub.Completions()) != 1 {
		t.Error("the item was lost")
	}
}

func TestReportForALeaseThatIsGoneIsLetGo(t *testing.T) {
	r := newRig(t)
	r.hub.Add(fixture(t, "pricing/fixtures/build.input.json"))
	r.hub.Add(fixture(t, "pricing/fixtures/park.input.json"))
	r.hub.FailSend(1, muster.ErrLeaseGone)
	if got := r.w.Step(ctx); got != Worked {
		t.Fatalf("got %v", got)
	}
	if entries, _ := r.spool.List(); len(entries) != 0 {
		t.Error("a report for a lease that is gone must not be spooled: it could never be delivered")
	}
	// A spooled report whose lease has since gone is dropped the same way,
	// and so is a spool entry that is not a report at all.
	stale, _ := json.Marshal(muster.Complete("PA-0001.1", "scored", []byte(`{}`)))
	if err := r.spool.Put("PA-0001.1", stale); err != nil {
		t.Fatal(err)
	}
	if err := r.spool.Put("PA-0002.1", []byte(`not a report`)); err != nil {
		t.Fatal(err)
	}
	if got := r.w.Step(ctx); got != Worked {
		t.Fatalf("got %v", got)
	}
	if entries, _ := r.spool.List(); len(entries) != 0 {
		t.Error("the stale entries should have been dropped")
	}
	if got := items(r.hub.Completions()); got != itemOf(t, "pricing/fixtures/park.input.json") {
		t.Errorf("only the second item was ever accepted, got %q", got)
	}
}

// TestPausedItemsScoreAfterANewProfileDeploys is the E_NEEDS_SUBTOOL row of
// the failure table. A build without the cyber profile pauses a cyber item;
// after a build with the profile is deployed and Muster replays the item,
// it is scored.
func TestPausedItemsScoreAfterANewProfileDeploys(t *testing.T) {
	// The same rules with the cyber profile taken out.
	without := fstest.MapFS{}
	err := fs.WalkDir(tools.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(path, "/cyber/") {
			return err
		}
		data, err := fs.ReadFile(tools.FS(), path)
		without[path] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	oldRules, err := tools.Load(without)
	if err != nil {
		t.Fatal(err)
	}

	r := newRig(t)
	r.w.Engine = engine.New(oldRules)
	r.hub.Add(fixture(t, "cyber/fixtures/build.input.json"))
	r.hub.Add(fixture(t, "pricing/fixtures/build.input.json"))
	r.drain()
	done := r.hub.Completions()
	if len(done) != 2 || done[0].Call != muster.CallNeedsSubtool || done[1].State != "scored" {
		t.Fatalf("before the profile exists: %+v", done)
	}
	if r.hub.Paused() != 1 {
		t.Fatal("the cyber item should be paused, and the rest should carry on")
	}

	// Deploy the build that has the profile; Muster replays.
	newRules, _ := tools.LoadEmbedded()
	r.w.Engine = engine.New(newRules)
	if r.hub.Replay() != 1 {
		t.Fatal("one item should be replayed")
	}
	r.drain()
	done = r.hub.Completions()
	if len(done) != 3 || done[2].Call != muster.CallComplete ||
		compact(t, outcomeIn(t, done[2].Result)) != compact(t, fixture(t, "cyber/fixtures/build.golden.json")) {
		t.Fatalf("after the profile deploys the item should be scored as its golden file says")
	}
}

// leakyHub fails the way a careless client would: with an error that names
// an address and a credential.
type leakyHub struct{ muster.Hub }

func (leakyHub) Lease(context.Context) (*muster.Lease, error) {
	return nil, errors.New(`Post "http://muster.clearedstack.svc.cluster.local/v1/lease": dial tcp 10.20.30.40:80: token=hunter2`)
}

func TestLogsNeverNameAMachineAnAddressOrACredential(t *testing.T) {
	r := newRig(t)
	r.w.Hub = leakyHub{}
	if r.w.Step(ctx) != Retry {
		t.Fatal("expected a retry")
	}
	logs := r.logs.String()
	for _, leak := range []string{"muster.clearedstack", "10.20.30.40", "hunter2", "http://", "dial tcp"} {
		if strings.Contains(logs, leak) {
			t.Errorf("the log leaks %q:\n%s", leak, logs)
		}
	}
	if !strings.Contains(logs, `"code":"E_TIMEOUT"`) {
		t.Errorf("an unrecognised hub failure should be logged as E_TIMEOUT:\n%s", logs)
	}

	// Nor do the logs for ordinary items carry anything from the lease
	// beyond the item identifier and the outcome.
	r2 := newRig(t)
	orig := fixture(t, "pricing/fixtures/build.input.json")
	rec := bytes.Replace(orig, []byte("made-up source"), []byte("Zap hunter2 at 10.20.30.40"), 1)
	if bytes.Equal(rec, orig) {
		t.Fatal("build.input.json no longer carries made-up source")
	}
	r2.hub.Add(rec)
	r2.w.Step(ctx)
	if strings.Contains(r2.logs.String(), "hunter2") || strings.Contains(r2.logs.String(), "10.20.30.40") {
		t.Errorf("the log repeats text from the lease:\n%s", r2.logs)
	}
}

// TestRunOverHTTPUntilCancelled runs the real loop against the fake Muster
// served over HTTP, with the hub failing at first, and stops it cleanly.
func TestRunOverHTTPUntilCancelled(t *testing.T) {
	r := newRig(t)
	names := []string{"pricing/fixtures/build", "proposal/fixtures/park", "devsecops/fixtures/screened-out-native",
		"market/fixtures/needs-subtool-unknown-value", "fixtures/parse-wrong-kind"}
	for _, name := range names {
		r.hub.Add(fixture(t, name+".input.json"))
	}
	r.hub.FailLease(2, timeout())
	r.hub.FailSend(2, timeout())

	srv := httptest.NewServer(r.hub.Handler("made-up-token"))
	defer srv.Close()
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("made-up-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	hub, err := muster.NewHTTP(srv.URL, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	r.w.Hub = hub

	runCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() { r.w.Run(runCtx); close(stopped) }()

	deadline := time.Now().Add(10 * time.Second)
	for len(r.hub.Completions()) < len(names) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when cancelled")
	}

	done := r.hub.Completions()
	if len(done) != len(names) {
		t.Fatalf("want %d closing calls, got %d\n%s", len(names), len(done), r.logs)
	}
	wantCalls := []muster.Call{muster.CallComplete, muster.CallComplete, muster.CallComplete, muster.CallNeedsSubtool, muster.CallFail}
	for i, name := range names {
		if done[i].Call != wantCalls[i] || done[i].ItemID != itemOf(t, name+".input.json") {
			t.Errorf("%s: got %s for %s", name, done[i].Call, done[i].ItemID)
		}
		if done[i].Call == muster.CallComplete && compact(t, outcomeIn(t, done[i].Result)) != compact(t, fixture(t, name+".golden.json")) {
			t.Errorf("%s: the result over HTTP differs from the golden file", name)
		}
	}
	if left, _ := r.spool.List(); len(left) != 0 {
		t.Errorf("spool not empty: %d", len(left))
	}
}

func TestWithoutASpoolAnUndeliveredReportIsDroppedNotLost(t *testing.T) {
	r := newRig(t)
	r.w.Spool = nil
	r.hub.Add(fixture(t, "pricing/fixtures/build.input.json"))
	r.hub.FailSend(1, timeout())
	if r.w.Step(ctx) != Retry {
		t.Fatal("expected a retry")
	}
	if !strings.Contains(r.logs.String(), "report_dropped") {
		t.Error("dropping a report must be logged")
	}
}

// TestEveryGoldenResultFitsInARequest holds the largest thing Assay sends
// to Muster's limit on a request body.
func TestEveryGoldenResultFitsInARequest(t *testing.T) {
	goldens, _ := filepath.Glob(filepath.Join(fixtures, "*/fixtures/*.golden.json"))
	if len(goldens) < 90 {
		t.Fatalf("found only %d golden files", len(goldens))
	}
	for _, g := range goldens {
		data, err := os.ReadFile(g)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 16<<10 {
			t.Errorf("%s is %d bytes; a result must stay far below the 64 KiB a request may be", g, len(data))
		}
	}
}
