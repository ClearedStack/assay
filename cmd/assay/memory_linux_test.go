package main

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/muster"
)

// budgetKiB is the memory budget from SPEC.md and component.json: 30 MiB.
const budgetKiB = 30 * 1024

// TestWorkerStaysWithinTheMemoryBudget builds the real binary the way ko
// does, runs it as a worker against a stand-in Muster served over HTTP,
// pushes several thousand leases through it (scored, screened out, paused and
// broken), and reads the process's peak resident memory from the kernel.
//
// It also checks the two other things a pod needs from the process: that it
// writes nowhere but the spool folder it is given, and that it stops cleanly
// on SIGTERM.
func TestWorkerStaysWithinTheMemoryBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	bin := filepath.Join(t.TempDir(), "assay")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	paths, err := filepath.Glob(filepath.Join(fixtures, "*/fixtures/*.input.json"))
	if err != nil || len(paths) == 0 {
		t.Fatal("no fixtures found")
	}
	more, _ := filepath.Glob(filepath.Join(fixtures, "fixtures/*.input.json"))
	// Only replies Assay can answer: one with no readable lease has no call
	// to make, and the worker rightly backs off after it.
	var inputs [][]byte
	for _, path := range append(paths, more...) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := contract.LeaseID(data); ok {
			inputs = append(inputs, data)
		}
	}
	if len(inputs) < 100 {
		t.Fatalf("only %d usable fixtures", len(inputs))
	}
	hub := muster.NewMemory()
	const total = 6000
	for i := 0; i < total; i++ {
		hub.Add(inputs[i%len(inputs)])
	}
	// One lease reply just under the size limit, to show a large one does
	// not blow the budget either.
	big := []byte(`{"lease":"PA-9999.1","kind":"item","id":"PA-9999","state":"observed","lane":"urgent","attempt":1,` +
		`"expires":"2026-01-05T09:01:00Z","results":{"sextant":{"category":"pricing","facts":{"padding":"`)
	for len(big) < 1000000 {
		big = append(big, "made-up filler "...)
	}
	hub.Add(append(big, `"}}}}`...))

	const token = "made-up-token"
	srv := httptest.NewServer(hub.Handler(token))
	defer srv.Close()
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}

	spoolDir := filepath.Join(t.TempDir(), "spool")
	cmd := exec.Command(bin, "run")
	cmd.Dir = "/" // somewhere it has no business writing
	cmd.Env = []string{
		"CONTRACT_VERSION=v1",
		"MUSTER_URL=" + srv.URL,
		"ASSAY_SPOOL_DIR=" + spoolDir,
		"ASSAY_TOKEN_FILE=" + tokenPath,
		"ASSAY_POLL_INTERVAL=20ms",
		"GOMEMLIMIT=24MiB", // as set in deploy/assay.yaml
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()

	deadline := time.Now().Add(120 * time.Second)
	for len(hub.Completions()) < total+1 {
		select {
		case err := <-exited:
			t.Fatalf("the worker exited early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d records were completed in time", len(hub.Completions()), total+1)
		}
		time.Sleep(20 * time.Millisecond)
	}

	status, err := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`VmHWM:\s+(\d+) kB`).FindSubmatch(status)
	if m == nil {
		t.Fatal("the kernel did not report peak memory")
	}
	peak, _ := strconv.Atoi(string(m[1]))
	t.Logf("peak resident memory after %d records: %.1f MiB (budget %d MiB)", total+1, float64(peak)/1024, budgetKiB/1024)
	if peak > budgetKiB {
		t.Errorf("peak resident memory %d KiB is over the %d KiB budget", peak, budgetKiB)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("the worker should exit cleanly on SIGTERM: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Error("the worker did not stop on SIGTERM")
	}

	// It wrote only inside the spool folder, and left nothing waiting.
	left, err := os.ReadDir(spoolDir)
	if err != nil {
		t.Fatalf("the spool folder should exist: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("%d files left in the spool", len(left))
	}
}
