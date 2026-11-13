package muster

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ClearedStack/assay/internal/errs"
)

var ctx = context.Background()

func TestMemoryLeasesEachLaneInOrder(t *testing.T) {
	m := NewMemory()
	m.Add(Normal, []byte(`{"n":1}`))
	m.Add(Normal, []byte(`{"n":2}`))
	m.Add(Urgent, []byte(`{"u":1}`))

	if l, err := m.Lease(ctx, Backfill); l != nil || err != nil {
		t.Fatalf("empty lane: got %v, %v", l, err)
	}
	first, _ := m.Lease(ctx, Normal)
	second, _ := m.Lease(ctx, Normal)
	if string(first.Record) != `{"n":1}` || string(second.Record) != `{"n":2}` || first.ID == second.ID {
		t.Fatalf("leases out of order: %s, %s", first.Record, second.Record)
	}
	if first.Lane != Normal || m.Waiting() != 1 {
		t.Errorf("lane %q, waiting %d", first.Lane, m.Waiting())
	}
}

func TestMemoryFilesOutcomesByState(t *testing.T) {
	m := NewMemory()
	for i := 0; i < 3; i++ {
		m.Add(Normal, []byte(`{}`))
	}
	a, _ := m.Lease(ctx, Normal)
	b, _ := m.Lease(ctx, Normal)
	c, _ := m.Lease(ctx, Normal)

	if err := m.Complete(ctx, a.ID, []byte(`{"state":"scored"}`)); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(ctx, b.ID, []byte(`{"state":"needs_subtool","error":{"code":"E_NEEDS_SUBTOOL"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(ctx, c.ID, []byte(`{"error":{"code":"E_PARSE"}}`)); err != nil {
		t.Fatal(err)
	}
	// A lease can be completed once. After that it is gone.
	if err := m.Complete(ctx, a.ID, []byte(`{"state":"scored"}`)); !errors.Is(err, ErrLeaseGone) {
		t.Errorf("second completion: got %v", err)
	}
	if err := m.Complete(ctx, "lease-unknown", []byte(`{}`)); !errors.Is(err, ErrLeaseGone) {
		t.Errorf("unknown lease: got %v", err)
	}

	done := m.Completions()
	if len(done) != 3 || done[0].State != "scored" || done[1].Code != "E_NEEDS_SUBTOOL" || done[2].State != "" || done[2].Code != "E_PARSE" {
		t.Fatalf("unexpected completions: %+v", done)
	}
	if m.Paused() != 1 {
		t.Fatalf("paused: %d", m.Paused())
	}

	// Replay puts the paused item back in its lane.
	if n := m.Replay(); n != 1 || m.Paused() != 0 || m.Waiting() != 1 {
		t.Errorf("replay: %d replayed, %d paused, %d waiting", n, m.Paused(), m.Waiting())
	}
}

func TestMemoryFaultsAndCancelledContext(t *testing.T) {
	m := NewMemory()
	m.Add(Normal, []byte(`{}`))
	m.FailLease(1, timeoutError())
	if _, err := m.Lease(ctx, Normal); err == nil {
		t.Fatal("expected the injected fault")
	}
	l, err := m.Lease(ctx, Normal)
	if err != nil || l == nil {
		t.Fatalf("after the fault: %v, %v", l, err)
	}
	m.FailComplete(1, timeoutError())
	if err := m.Complete(ctx, l.ID, []byte(`{}`)); err == nil {
		t.Fatal("expected the injected fault")
	}
	// The lease survives a failed completion.
	if err := m.Complete(ctx, l.ID, []byte(`{"state":"scored"}`)); err != nil {
		t.Fatal(err)
	}

	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Lease(gone, Normal); code(err) != errs.Timeout {
		t.Errorf("cancelled context: got %v", err)
	}
}

func code(err error) errs.Code {
	var pub *errs.Error
	if errors.As(err, &pub) {
		return pub.Code
	}
	return ""
}

func reason(err error) errs.Reason {
	var pub *errs.Error
	if errors.As(err, &pub) {
		return pub.Reason
	}
	return ""
}

func tokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHTTPRoundTripAgainstTheStandIn(t *testing.T) {
	m := NewMemory()
	// A record that is not even JSON must still arrive byte for byte.
	broken := []byte(`{"contract_version": "v1", "item_id": `)
	m.Add(Urgent, broken)
	m.Add(Normal, []byte(`{"n":1}`))

	srv := httptest.NewServer(Handler(m, "made-up-token"))
	defer srv.Close()
	hub, err := NewHTTP(srv.URL+"/", tokenFile(t, "made-up-token"))
	if err != nil {
		t.Fatal(err)
	}

	if l, err := hub.Lease(ctx, Backfill); l != nil || err != nil {
		t.Fatalf("empty lane: %v, %v", l, err)
	}
	l, err := hub.Lease(ctx, Urgent)
	if err != nil || l == nil || string(l.Record) != string(broken) || l.Lane != Urgent {
		t.Fatalf("lease: %+v, %v", l, err)
	}
	if err := hub.Complete(ctx, l.ID, []byte(`{"error":{"code":"E_PARSE"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := hub.Complete(ctx, l.ID, []byte(`{}`)); !errors.Is(err, ErrLeaseGone) {
		t.Errorf("second completion: got %v", err)
	}
	if done := m.Completions(); len(done) != 1 || done[0].Code != "E_PARSE" {
		t.Errorf("completions: %+v", done)
	}
}

func TestHTTPRereadsTheTokenForEveryRequest(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	path := tokenFile(t, "first-token")
	hub, _ := NewHTTP(srv.URL, path)
	_, _ = hub.Lease(ctx, Normal)
	if err := os.WriteFile(path, []byte("rotated-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = hub.Lease(ctx, Normal)
	if len(seen) != 2 || seen[0] != "Bearer first-token" || seen[1] != "Bearer rotated-token" {
		t.Errorf("tokens presented: %q", seen)
	}

	// A missing token file means the pod cannot prove who it is. The call
	// is not attempted at all.
	before := len(seen)
	missing, _ := NewHTTP(srv.URL, filepath.Join(t.TempDir(), "absent"))
	if _, err := missing.Lease(ctx, Normal); code(err) != errs.Timeout || len(seen) != before {
		t.Errorf("missing token: err %v, requests %d", err, len(seen)-before)
	}
}

func TestHTTPStatusMapping(t *testing.T) {
	cases := []struct {
		status   int
		leaseID  string
		code     errs.Code
		reason   errs.Reason
		gone     bool
		complete bool
	}{
		{status: 500, code: errs.Timeout, reason: errs.HubUnavailable},
		{status: 503, code: errs.Timeout, reason: errs.HubUnavailable},
		{status: 429, code: errs.Timeout, reason: errs.HubUnavailable},
		{status: 401, code: errs.Timeout, reason: errs.HubUnavailable},
		{status: 403, code: errs.Timeout, reason: errs.HubUnavailable},
		{status: 400, code: errs.Parse, reason: errs.HubRejectedRequest},
		{status: 422, code: errs.Parse, reason: errs.HubRejectedRequest},
		{status: 302, code: errs.Parse, reason: errs.HubReplyInvalid},
		{status: 200, leaseID: "", code: errs.Parse, reason: errs.HubReplyInvalid},
		{status: 200, leaseID: "has spaces", code: errs.Parse, reason: errs.HubReplyInvalid},
		{status: 404, complete: true, gone: true},
		{status: 409, complete: true, gone: true},
		{status: 410, complete: true, gone: true},
		{status: 500, complete: true, code: errs.Timeout, reason: errs.HubUnavailable},
		{status: 400, complete: true, code: errs.Parse, reason: errs.HubRejectedRequest},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.status == 302 {
				w.Header().Set("Location", "http://elsewhere.invalid/")
			}
			if tc.leaseID != "" {
				w.Header().Set(LeaseHeader, tc.leaseID)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{}`))
		}))
		hub, _ := NewHTTP(srv.URL, "")
		var err error
		if tc.complete {
			err = hub.Complete(ctx, "lease-1", []byte(`{}`))
		} else {
			_, err = hub.Lease(ctx, Normal)
		}
		srv.Close()
		if tc.gone {
			if !errors.Is(err, ErrLeaseGone) {
				t.Errorf("status %d: want ErrLeaseGone, got %v", tc.status, err)
			}
			continue
		}
		if code(err) != tc.code || reason(err) != tc.reason {
			t.Errorf("status %d (complete=%v): got %v, want %s %s", tc.status, tc.complete, err, tc.code, tc.reason)
		}
	}
}

func TestHTTPNeverFollowsARedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	hub, _ := NewHTTP(srv.URL, tokenFile(t, "made-up-token"))
	if _, err := hub.Lease(ctx, Normal); err == nil {
		t.Error("a redirect must be an error")
	}
	if elsewhere.Load() != 0 {
		t.Error("the client followed a redirect and would have carried the token with it")
	}
}

// TestHTTPErrorsNameNothing makes each kind of transport failure happen and
// checks that the error says only E_TIMEOUT: no host, no port, no path, no
// token.
func TestHTTPErrorsNameNothing(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	defer close(release) // runs first, so Close does not wait on the handlers

	// A port nothing listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + ln.Addr().String()
	ln.Close()

	token := "made-up-token-8842"
	for name, base := range map[string]string{"slow hub": slow.URL, "nothing listening": dead} {
		hub, _ := NewHTTP(base, tokenFile(t, token))
		short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		_, leaseErr := hub.Lease(short, Normal)
		completeErr := hub.Complete(short, "lease-1", []byte(`{}`))
		cancel()
		for _, err := range []error{leaseErr, completeErr} {
			if code(err) != errs.Timeout {
				t.Errorf("%s: want E_TIMEOUT, got %v", name, err)
				continue
			}
			text := err.Error()
			for _, leak := range []string{"127.0.0.1", "http", "://", "lease", token, strings.TrimPrefix(base, "http://")} {
				if strings.Contains(text, leak) {
					t.Errorf("%s: error %q leaks %q", name, text, leak)
				}
			}
		}
	}
}

func TestNewHTTPRefusesABadAddressWithoutEchoingIt(t *testing.T) {
	for _, bad := range []string{"", "muster", "ftp://muster.example", "http://user:hunter2@muster.example", "http://", "://x"} {
		_, err := NewHTTP(bad, "")
		if err == nil {
			t.Errorf("%q should be refused", bad)
			continue
		}
		if bad != "" && strings.Contains(err.Error(), bad) || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("the error echoes the address: %v", err)
		}
	}
}

func TestHandlerRefusesWhatIsNotTheContract(t *testing.T) {
	srv := httptest.NewServer(Handler(NewMemory(), "made-up-token"))
	defer srv.Close()
	do := func(method, path, auth, version string) int {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"worker":"assay","state":"observed","lane":"normal"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if version != "" {
			req.Header.Set("Contract-Version", version)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := do("POST", "/v1/leases", "Bearer made-up-token", "v1"); got != http.StatusNoContent {
		t.Errorf("good request: %d", got)
	}
	if got := do("GET", "/v1/leases", "Bearer made-up-token", "v1"); got != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", got)
	}
	if got := do("POST", "/v1/leases", "Bearer wrong", "v1"); got != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", got)
	}
	if got := do("POST", "/v1/leases", "Bearer made-up-token", "v2"); got != http.StatusBadRequest {
		t.Errorf("wrong contract: %d", got)
	}
	if got := do("POST", "/v1/elsewhere", "Bearer made-up-token", "v1"); got != http.StatusNotFound {
		t.Errorf("unknown path: %d", got)
	}
}
