package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
	"github.com/ClearedStack/assay/internal/muster"
	"github.com/ClearedStack/assay/internal/spool"
	"github.com/ClearedStack/assay/internal/worker"
)

// Defaults for the worker. Each can be overridden by the environment
// variable named beside it.
const (
	defaultSpoolDir     = "/var/spool/cs"                                       // ASSAY_SPOOL_DIR
	defaultTokenFile    = "/var/run/secrets/kubernetes.io/serviceaccount/token" // ASSAY_TOKEN_FILE
	defaultPollInterval = 5 * time.Second                                       // ASSAY_POLL_INTERVAL
	defaultHubTimeout   = 10 * time.Second                                      // ASSAY_HUB_TIMEOUT
	backoffMin          = 1 * time.Second
	backoffMax          = 60 * time.Second
)

func cmdRun(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	standin := fs.String("standin", "", "run over the records in this folder, using an in-memory stand-in for Muster")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		say(stderr, usage)
		return exitUsage
	}
	eng := newEngine(stderr)
	if eng == nil {
		return exitFailure
	}
	if *standin != "" {
		return runStandin(ctx, *standin, eng, stdout, stderr)
	}

	// CONTRACT_VERSION is how the platform says which contract it expects.
	// This build speaks one version and refuses to start under another.
	if v := getenv("CONTRACT_VERSION"); v != "" && v != contract.Version {
		say(stderr, "assay: CONTRACT_VERSION is not", contract.Version)
		return exitFailure
	}
	if getenv("MUSTER_URL") == "" {
		say(stderr, "assay: MUSTER_URL is not set")
		return exitFailure
	}
	tokenFile := defaultTokenFile
	if v, ok := os.LookupEnv("ASSAY_TOKEN_FILE"); ok {
		tokenFile = v
	}
	hub, err := muster.NewHTTP(getenv("MUSTER_URL"), tokenFile)
	if err != nil {
		say(stderr, "assay: MUSTER_URL:", err)
		return exitFailure
	}
	spoolDir := getenv("ASSAY_SPOOL_DIR")
	if spoolDir == "" {
		spoolDir = defaultSpoolDir
	}
	sp, err := spool.Open(spoolDir)
	if err != nil {
		say(stderr, "assay:", err)
		return exitFailure
	}
	poll, ok1 := duration(getenv("ASSAY_POLL_INTERVAL"), defaultPollInterval)
	timeout, ok2 := duration(getenv("ASSAY_HUB_TIMEOUT"), defaultHubTimeout)
	if !ok1 || !ok2 {
		say(stderr, "assay: ASSAY_POLL_INTERVAL and ASSAY_HUB_TIMEOUT must be durations such as 5s")
		return exitFailure
	}

	log := slog.New(slog.NewJSONHandler(stdout, nil))
	w := &worker.Worker{
		Hub: hub, Engine: eng, Spool: sp, Log: log,
		HubTimeout: timeout, PollInterval: poll, BackoffMin: backoffMin, BackoffMax: backoffMax,
	}
	log.Info("worker_started", "worker", contract.Worker, "version", version, "contract", contract.Version)
	w.Run(ctx)
	log.Info("worker_stopped")
	return exitOK
}

func duration(value string, fallback time.Duration) (time.Duration, bool) {
	if value == "" {
		return fallback, true
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 10*time.Millisecond || d > time.Hour {
		return 0, false
	}
	return d, true
}

// runStandin runs the real worker loop against the in-memory stand-in for
// Muster, so Assay can be seen working with nothing else installed.
//
// Each .json file in dir is one lease reply, as Muster would send it. Each
// joins the lane it names. The worker takes them urgent lane first, and
// what Assay reported for each is printed in the order it was reported.
func runStandin(ctx context.Context, dir string, eng *engine.Engine, stdout, stderr io.Writer) int {
	hub := muster.NewMemory()
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		say(stderr, "assay: no records found")
		return exitFailure
	}
	sort.Strings(paths)
	for _, path := range paths {
		data, err := readRecord(path, nil)
		if err != nil {
			say(stderr, "assay: a record could not be read")
			return exitFailure
		}
		hub.Add(data)
	}

	w := &worker.Worker{
		Hub: hub, Engine: eng, Log: slog.New(slog.NewJSONHandler(stderr, nil)),
		HubTimeout: defaultHubTimeout, BackoffMin: backoffMin, BackoffMax: backoffMax,
	}
	for ctx.Err() == nil && w.Step(ctx) != worker.Idle {
	}
	for _, c := range hub.Completions() {
		switch c.Call {
		case muster.CallComplete:
			if _, err := stdout.Write(c.Result); err != nil {
				return exitFailure
			}
		case muster.CallNeedsSubtool:
			sayf(stdout, "{\n  \"id\": %q,\n  \"call\": \"needs_subtool\"\n}\n", c.ItemID)
		case muster.CallFail:
			sayf(stdout, "{\n  \"id\": %q,\n  \"call\": \"fail\",\n  \"code\": %q\n}\n", c.ItemID, string(c.Code))
		}
	}
	return exitOK
}
