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

	"github.com/ClearedStack/assay/internal/contract"
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

func itemIDs(t *testing.T, done []muster.Completion) []string {
	t.Helper()
	var ids []string
	for _, c := range done {
		var o contract.Outcome
		if err := json.Unmarshal(c.Outcome, &o); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, o.ItemID)
	}
	return ids
}

func timeout() error { return errs.New(errs.Timeout, errs.HubTimeout, "hub") }

func TestLanesAreDrainedUrgentThenNormalThenBackfill(t *testing.T) {
	r := newRig(t)
	// Queued in the wrong order on purpose.
	r.hub.Add(muster.Backfill, fixture(t, "market/fixtures/build.input.json"))
	r.hub.Add(muster.Normal, fixture(t, "pricing/fixtures/park.input.json"))
	r.hub.Add(muster.Normal, fixture(t, "pricing/fixtures/reject.input.json"))
	r.hub.Add(muster.Urgent, fixture(t, "cyber/fixtures/build.input.json"))

	if got := r.w.Step(ctx); got != Worked {
		t.Fatalf("first step: %v", got)
	}
	// An urgent record arriving mid-run jumps the queue: the lane order
	// starts again from urgent after every item.
	r.hub.Add(muster.Urgent, fixture(t, "proposal/fixtures/link.input.json"))
	for r.w.Step(ctx) == Worked {
	}

	want := []string{"cyber-build", "proposal-link", "pricing-park", "pricing-reject", "market-build"}
	got := itemIDs(t, r.hub.Completions())
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order: got %v, want %v", got, want)
	}
	if r.w.Step(ctx) != Idle {
		t.Error("with every lane empty the worker should be idle")
	}
}

func TestOutcomeHandedBackIsTheGoldenOutcome(t *testing.T) {
	r := newRig(t)
	cases := []string{
		"pricing/fixtures/build", "pricing/fixtures/link", "pricing/fixtures/park", "pricing/fixtures/reject",
		"pricing/fixtures/screened-out-all-gates", "pricing/fixtures/needs-subtool-unknown-fact",
		"pricing/fixtures/parse-wrong-type", "fixtures/parse-malformed-json", "fixtures/needs-subtool-unknown-category",
	}
	for _, name := range cases {
		r.hub.Add(muster.Normal, fixture(t, name+".input.json"))
	}
	for r.w.Step(ctx) == Worked {
	}
	done := r.hub.Completions()
	if len(done) != len(cases) {
		t.Fatalf("want %d completions, got %d", len(cases), len(done))
	}
	wantStates := []string{"scored", "scored", "scored", "scored", "screened-out", "needs_subtool", "", "", "needs_subtool"}
	wantCodes := []string{"", "", "", "", "", "E_NEEDS_SUBTOOL", "E_PARSE", "E_PARSE", "E_NEEDS_SUBTOOL"}
	for i, name := range cases {
		if !bytes.Equal(done[i].Outcome, fixture(t, name+".golden.json")) {
			t.Errorf("%s: the outcome handed to the hub is not the golden outcome", name)
		}
		if done[i].State != wantStates[i] || done[i].Code != wantCodes[i] {
			t.Errorf("%s: state %q code %q, want %q %q", name, done[i].State, done[i].Code, wantStates[i], wantCodes[i])
		}
	}
	if r.hub.Paused() != 2 {
		t.Errorf("two items should be paused in needs_subtool, got %d", r.hub.Paused())
	}
}

// TestCompletionTimeoutIsSpooledAndRetried is the E_TIMEOUT row of the
// failure table: the finished score is kept on disk and delivered once the
// hub answers again, and nothing new is leased in the meantime.
func TestCompletionTimeoutIsSpooledAndRetried(t *testing.T) {
	r := newRig(t)
	r.hub.Add(muster.Normal, fixture(t, "pricing/fixtures/build.input.json"))
	r.hub.Add(muster.Normal, fixture(t, "pricing/fixtures/park.input.json"))
	r.hub.FailComplete(3, timeout())

	if got := r.w.Step(ctx); got != Retry {
		t.Fatalf("a failed completion should ask for a retry, got %v", got)
	}
	entries, _ := r.spool.List()
	if len(entries) != 1 || !bytes.Equal(entries[0].Outcome, fixture(t, "pricing/fixtures/build.golden.json")) {
		t.Fatalf("the finished outcome should be in the spool; got %d entries", len(entries))
	}

	// The hub is still failing: the entry stays, and no new record is taken.
	for i := 0; i < 2; i++ {
		if got := r.w.Step(ctx); got != Retry {
			t.Fatalf("retry %d: got %v", i, got)
		}
	}
	if r.hub.Waiting() != 1 || len(r.hub.Completions()) != 0 {
		t.Fatalf("while outcomes are waiting no new work may be leased: waiting %d, completed %d", r.hub.Waiting(), len(r.hub.Completions()))
	}

	// The hub recovers. The spooled outcome goes first, then work resumes.
	for r.w.Step(ctx) == Worked {
	}
	if got := itemIDs(t, r.hub.Completions()); strings.Join(got, ",") != "pricing-build,pricing-park" {
		t.Errorf("completions: %v", got)
	}
	if left, _ := r.spool.List(); len(left) != 0 {
		t.Errorf("the spool should be empty, has %d", len(left))
	}
	if !strings.Contains(r.logs.String(), `"code":"E_TIMEOUT"`) || !strings.Contains(r.logs.String(), "outcome_spooled") {
		t.Errorf("the timeout should be logged with its code:\n%s", r.logs)
	}
}

// TestSpoolSurvivesARestart stops the worker with an outcome waiting and
// starts a fresh one over the same folder, as happens when the pod's
// container restarts.
func TestSpoolSurvivesARestart(t *testing.T) {
	r := newRig(t)
	r.hub.Add(muster.Normal, fixture(t, "pricing/fixtures/build.input.json"))
	r.hub.FailComplete(1, timeout())
	r.w.Step(ctx)

	fresh := *r.w
	fresh.Log = slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	if got := fresh.Step(ctx); got != Idle {
		t.Fatalf("the fresh worker should deliver the spooled outcome and then find nothing to do, got %v", got)
	}
	if got := itemIDs(t, r.hub.Completions()); len(got) != 1 || got[0] != "pricing-build" {
		t.Errorf("completions: %v", got)
	}
}

func TestLeaseTimeoutBacksOffAndLosesNothing(t *testing.T) {
	r := newRig(t)
	r.hub.Add(muster.Urgent, fixture(t, "pricing/fixtures/build.input.json"))
	r.hub.FailLease(2, timeout())
	for i := 0; i < 2; i++ {
		if r.w.Step(ctx) != Retry {
			t.Fatal("a failed lease should ask for a retry")
		}
	}
	if r.w.Step(ctx) != Worked {
		t.Fatal("the record should be leased once the hub answers")
	}
	if len(r.hub.Completions()) != 1 {
		t.Error("the record was lost")
	}
}

func TestOutcomeForALeaseThatIsGoneIsLetGo(t *testing.T) {
	r := newRig(t)
	r.hub.Add(muster.Normal, fixture(t, "pricing/fixtures/build.input.json"))
	r.hub.Add(muster.Normal, fixture(t, "pricing/fixtures/park.input.json"))
	r.hub.FailComplete(1, muster.ErrLeaseGone)
	if got := r.w.Step(ctx); got != Worked {
		t.Fatalf("got %v", got)
	}
	if entries, _ := r.spool.List(); len(entries) != 0 {
		t.Error("an outcome for a lease that is gone must not be spooled: it could never be delivered")
	}
	// A spooled outcome whose lease has since gone is dropped the same way.
	if err := r.spool.Put("lease-from-before", []byte(`{"state":"scored"}`)); err != nil {
		t.Fatal(err)
	}
	if got := r.w.Step(ctx); got != Worked {
		t.Fatalf("got %v", got)
	}
	if entries, _ := r.spool.List(); len(entries) != 0 {
		t.Error("the stale entry should have been dropped")
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
	r.hub.Add(muster.Normal, fixture(t, "cyber/fixtures/build.input.json"))
	r.hub.Add(muster.Normal, fixture(t, "pricing/fixtures/build.input.json"))
	for r.w.Step(ctx) == Worked {
	}
	done := r.hub.Completions()
	if len(done) != 2 || done[0].State != contract.StateNeedsSubtool || done[0].Code != "E_NEEDS_SUBTOOL" || done[1].State != contract.StateScored {
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
	for r.w.Step(ctx) == Worked {
	}
	done = r.hub.Completions()
	if len(done) != 3 || !bytes.Equal(done[2].Outcome, fixture(t, "cyber/fixtures/build.golden.json")) {
		t.Fatalf("after the profile deploys the item should be scored as its golden file says")
	}
}

// leakyHub fails the way a careless client would: with an error that names
// an address and a credential.
type leakyHub struct{ muster.Hub }

func (leakyHub) Lease(context.Context, muster.Lane) (*muster.Lease, error) {
	return nil, errors.New(`Post "http://muster.clearedstack.svc.cluster.local/v1/leases": dial tcp 10.20.30.40:80: token=hunter2`)
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

	// Nor do the logs for ordinary items carry anything from the record
	// beyond the item ID and the outcome.
	r2 := newRig(t)
	rec := bytes.Replace(fixture(t, "pricing/fixtures/build.input.json"), []byte("Compute a loaded hourly rate"), []byte("Zap hunter2 at 10.20.30.40"), 1)
	r2.hub.Add(muster.Normal, rec)
	r2.w.Step(ctx)
	if strings.Contains(r2.logs.String(), "hunter2") || strings.Contains(r2.logs.String(), "10.20.30.40") {
		t.Errorf("the log repeats text from the record:\n%s", r2.logs)
	}
}

// TestRunOverHTTPUntilCancelled runs the real loop against the stand-in
// served over HTTP, with the hub failing at first, and stops it cleanly.
func TestRunOverHTTPUntilCancelled(t *testing.T) {
	r := newRig(t)
	names := []string{"pricing/fixtures/build", "proposal/fixtures/park", "devsecops/fixtures/screened-out-native", "fixtures/parse-duplicate-key"}
	for _, name := range names {
		r.hub.Add(muster.Normal, fixture(t, name+".input.json"))
	}
	r.hub.FailLease(2, timeout())
	r.hub.FailComplete(2, timeout())

	srv := httptest.NewServer(muster.Handler(r.hub, ""))
	defer srv.Close()
	hub, err := muster.NewHTTP(srv.URL, "")
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
		t.Fatalf("want %d completions, got %d\n%s", len(names), len(done), r.logs)
	}
	for i, name := range names {
		if !bytes.Equal(done[i].Outcome, fixture(t, name+".golden.json")) {
			t.Errorf("%s: outcome over HTTP differs from the golden file", name)
		}
	}
	if left, _ := r.spool.List(); len(left) != 0 {
		t.Errorf("spool not empty: %d", len(left))
	}
}

func TestWithoutASpoolAnUndeliveredOutcomeIsDroppedNotLost(t *testing.T) {
	r := newRig(t)
	r.w.Spool = nil
	r.hub.Add(muster.Normal, fixture(t, "pricing/fixtures/build.input.json"))
	r.hub.FailComplete(1, timeout())
	if r.w.Step(ctx) != Retry {
		t.Fatal("expected a retry")
	}
	if !strings.Contains(r.logs.String(), "outcome_dropped") {
		t.Error("dropping an outcome must be logged")
	}
}
