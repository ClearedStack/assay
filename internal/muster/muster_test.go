package muster

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

// reply builds a lease reply the way Muster sends one to Assay.
func reply(item, lane string) []byte {
	return []byte(`{"lease":"` + item + `.1","kind":"item","id":"` + item + `","state":"observed","lane":"` + lane +
		`","attempt":1,"expires":"2026-01-05T09:01:00Z","results":{"sextant":{"category":"pricing","facts":{}}}}`)
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

func TestMemoryOffersUrgentThenNormalThenBackfill(t *testing.T) {
	m := NewMemory()
	m.Add(reply("PA-0003", "backfill"))
	m.Add(reply("PA-0002", "normal"))
	m.Add(reply("PA-0004", "normal"))
	m.Add(reply("PA-0001", "urgent"))

	var got []string
	for {
		l, err := m.Lease(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if l == nil {
			break
		}
		got = append(got, l.ID)
		if !strings.Contains(string(l.Body), `"lease":"`+l.ID+`"`) {
			t.Errorf("%s: the reply was not served as queued", l.ID)
		}
	}
	want := "PA-0001.1,PA-0002.1,PA-0004.1,PA-0003.1"
	if strings.Join(got, ",") != want {
		t.Errorf("order: got %v, want %s", got, want)
	}
}

func TestMemoryFilesEachClosingCall(t *testing.T) {
	m := NewMemory()
	for _, id := range []string{"PA-0001", "PA-0002", "PA-0003", "PA-0004"} {
		m.Add(reply(id, "normal"))
	}
	for i := 0; i < 4; i++ {
		if l, err := m.Lease(ctx); err != nil || l == nil {
			t.Fatal("lease failed")
		}
	}
	if err := m.Heartbeat(ctx, "PA-0001.1"); err != nil {
		t.Errorf("heartbeat on a held lease: %v", err)
	}

	scored := Complete("PA-0001.1", "scored", []byte(`{"total":86}`))
	if err := m.Send(ctx, scored); err != nil {
		t.Fatal(err)
	}
	if err := m.Send(ctx, Complete("PA-0002.1", "screened_out", []byte(`{}`))); err != nil {
		t.Fatal(err)
	}
	if err := m.Send(ctx, NeedsSubtool("PA-0003.1")); err != nil {
		t.Fatal(err)
	}
	if err := m.Send(ctx, Fail("PA-0004.1", errs.Parse)); err != nil {
		t.Fatal(err)
	}

	// The call that ended a lease, made again, gets the same answer and
	// does nothing twice.
	if err := m.Send(ctx, scored); err != nil {
		t.Errorf("a repeated call must be acknowledged again: %v", err)
	}
	// Any other call on a closed lease is refused, as is a heartbeat.
	if err := m.Send(ctx, Complete("PA-0001.1", "screened_out", nil)); !errors.Is(err, ErrLeaseGone) {
		t.Errorf("a different call on a closed lease: got %v", err)
	}
	if err := m.Heartbeat(ctx, "PA-0001.1"); !errors.Is(err, ErrLeaseGone) {
		t.Errorf("heartbeat on a closed lease: got %v", err)
	}
	if err := m.Send(ctx, NeedsSubtool("PA-9999.1")); !errors.Is(err, ErrLeaseGone) {
		t.Errorf("unknown lease: got %v", err)
	}

	done := m.Completions()
	if len(done) != 4 {
		t.Fatalf("want 4 closing calls filed once each, got %d", len(done))
	}
	want := []Completion{
		{Lease: "PA-0001.1", ItemID: "PA-0001", Lane: "normal", Call: CallComplete, State: "scored", Result: []byte(`{"total":86}`)},
		{Lease: "PA-0002.1", ItemID: "PA-0002", Lane: "normal", Call: CallComplete, State: "screened_out", Result: []byte(`{}`)},
		{Lease: "PA-0003.1", ItemID: "PA-0003", Lane: "normal", Call: CallNeedsSubtool, State: "needs_subtool"},
		{Lease: "PA-0004.1", ItemID: "PA-0004", Lane: "normal", Call: CallFail, Code: errs.Parse},
	}
	for i, w := range want {
		g := done[i]
		if g.Lease != w.Lease || g.ItemID != w.ItemID || g.Lane != w.Lane || g.Call != w.Call || g.State != w.State || g.Code != w.Code || string(g.Result) != string(w.Result) {
			t.Errorf("completion %d: got %+v, want %+v", i, g, w)
		}
	}
	if m.Paused() != 1 || m.Waiting() != 0 {
		t.Errorf("paused %d, waiting %d", m.Paused(), m.Waiting())
	}
}

func TestMemoryRefusesMovesAssayMayNotMake(t *testing.T) {
	m := NewMemory()
	m.Add(reply("PA-0001", "normal"))
	l, _ := m.Lease(ctx)
	for _, state := range []string{"approved", "observed", "screened-out", "needs_subtool", ""} {
		if err := m.Send(ctx, Complete(l.ID, state, nil)); !errors.Is(err, ErrLeaseGone) {
			t.Errorf("complete to %q: got %v", state, err)
		}
	}
	if err := m.Send(ctx, Fail(l.ID, errs.NeedsSubtool)); code(err) != errs.Parse {
		t.Errorf("fail with E_NEEDS_SUBTOOL must be refused: got %v", err)
	}
	if err := m.Send(ctx, Complete(l.ID, "scored", []byte(`[1,2]`))); code(err) != errs.Parse {
		t.Errorf("a result that is not an object must be refused: got %v", err)
	}
	// None of that closed the lease.
	if err := m.Send(ctx, Complete(l.ID, "scored", nil)); err != nil {
		t.Errorf("the lease should still be held: %v", err)
	}
}

func TestMemoryRequeuesATimeoutAndReplaysPauses(t *testing.T) {
	m := NewMemory()
	m.Add(reply("PA-0001", "normal"))
	m.Add(reply("PA-0002", "normal"))
	m.Add(reply("PA-0003", "backfill"))

	// fail with E_TIMEOUT: the step stays in the queue, in its place.
	first, _ := m.Lease(ctx)
	if err := m.Send(ctx, Fail(first.ID, errs.Timeout)); err != nil {
		t.Fatal(err)
	}
	again, _ := m.Lease(ctx)
	if again == nil || again.ID != first.ID {
		t.Fatalf("after E_TIMEOUT the same item should be offered next, got %+v", again)
	}

	// Pause two items, then replay: both come back in the urgent lane, in
	// the order they were paused, ahead of what was already waiting.
	if err := m.Send(ctx, NeedsSubtool(again.ID)); err != nil {
		t.Fatal(err)
	}
	second, _ := m.Lease(ctx)
	if err := m.Send(ctx, NeedsSubtool(second.ID)); err != nil {
		t.Fatal(err)
	}
	if n := m.Replay(); n != 2 || m.Paused() != 0 {
		t.Fatalf("replay: %d replayed, %d still paused", n, m.Paused())
	}
	var order []string
	for {
		l, _ := m.Lease(ctx)
		if l == nil {
			break
		}
		order = append(order, l.ID)
	}
	if strings.Join(order, ",") != "PA-0001.1,PA-0002.1,PA-0003.1" {
		t.Errorf("after replay: got %v", order)
	}
}

func TestMemoryFaultsAndCancelledContext(t *testing.T) {
	m := NewMemory()
	m.Add(reply("PA-0001", "normal"))
	m.FailLease(1, timeoutError())
	if _, err := m.Lease(ctx); err == nil {
		t.Fatal("expected the injected fault")
	}
	l, err := m.Lease(ctx)
	if err != nil || l == nil {
		t.Fatalf("after the fault: %v, %v", l, err)
	}
	m.FailSend(1, timeoutError())
	if err := m.Send(ctx, Complete(l.ID, "scored", nil)); err == nil {
		t.Fatal("expected the injected fault")
	}
	// The lease survives a failed call.
	if err := m.Send(ctx, Complete(l.ID, "scored", nil)); err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Lease(gone); code(err) != errs.Timeout {
		t.Errorf("cancelled context: got %v", err)
	}

	// A queued reply with no readable lease is what the HTTP client would
	// call an invalid reply.
	m.Add([]byte(`{"kind":"item"`))
	if _, err := m.Lease(ctx); reason(err) != errs.HubReplyInvalid {
		t.Errorf("unreadable reply: got %v", err)
	}
}

// recorder is a fake Muster that writes down exactly what it was sent.
type recorder struct {
	t      *testing.T
	seen   []string // "METHOD path body"
	auth   []string
	ctype  []string
	status int
	body   string
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	data, _ := io.ReadAll(req.Body)
	r.seen = append(r.seen, req.Method+" "+req.URL.RequestURI()+" "+string(data))
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	r.ctype = append(r.ctype, req.Header.Get("Content-Type"))
	if req.Header.Get("Lease-Id") != "" || req.Header.Get("Contract-Version") != "" {
		r.t.Error("the contract is carried in the body; no such header may be sent")
	}
	w.WriteHeader(r.status)
	_, _ = w.Write([]byte(r.body))
}

// TestHTTPSendsExactlyWhatTheContractDefines pins every request to the
// byte: the path, and a body with the fields that call defines and no
// others, because Muster refuses a body with any other field.
func TestHTTPSendsExactlyWhatTheContractDefines(t *testing.T) {
	rec := &recorder{t: t, status: 200, body: string(reply("PA-0001", "normal"))}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	hub, err := NewHTTP(srv.URL+"/", tokenFile(t, "made-up-token"))
	if err != nil {
		t.Fatal(err)
	}

	l, err := hub.Lease(ctx)
	if err != nil || l == nil || l.ID != "PA-0001.1" || string(l.Body) != rec.body {
		t.Fatalf("lease: %+v, %v", l, err)
	}
	calls := []error{
		hub.Heartbeat(ctx, l.ID),
		hub.Send(ctx, Complete(l.ID, "scored", []byte("{\n  \"total\": 86,\n  \"note\": \"a <b> & c\"\n}\n"))),
		hub.Send(ctx, Complete(l.ID, "screened_out", nil)),
		hub.Send(ctx, Fail(l.ID, errs.Parse)),
		hub.Send(ctx, Fail(l.ID, errs.Timeout)),
		hub.Send(ctx, NeedsSubtool(l.ID)),
	}
	for i, err := range calls {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}

	want := []string{
		`POST /v1/lease {"contract":"v1"}`,
		`POST /v1/heartbeat {"contract":"v1","lease":"PA-0001.1"}`,
		`POST /v1/complete {"contract":"v1","lease":"PA-0001.1","state":"scored","result":{"total":86,"note":"a \u003cb\u003e \u0026 c"}}`,
		`POST /v1/complete {"contract":"v1","lease":"PA-0001.1","state":"screened_out"}`,
		`POST /v1/fail {"contract":"v1","lease":"PA-0001.1","code":"E_PARSE"}`,
		`POST /v1/fail {"contract":"v1","lease":"PA-0001.1","code":"E_TIMEOUT"}`,
		`POST /v1/needs_subtool {"contract":"v1","lease":"PA-0001.1"}`,
	}
	if len(rec.seen) != len(want) {
		t.Fatalf("want %d requests, got %d: %v", len(want), len(rec.seen), rec.seen)
	}
	for i := range want {
		if rec.seen[i] != want[i] {
			t.Errorf("request %d:\n got %s\nwant %s", i, rec.seen[i], want[i])
		}
		if rec.auth[i] != "Bearer made-up-token" || rec.ctype[i] != "application/json" {
			t.Errorf("request %d: authorization %q, content type %q", i, rec.auth[i], rec.ctype[i])
		}
	}
}

func TestHTTPRefusesToSendWhatMusterWouldRefuse(t *testing.T) {
	rec := &recorder{t: t, status: 200, body: `{}`}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	hub, _ := NewHTTP(srv.URL, "")

	bad := map[string]Report{
		"fail with E_NEEDS_SUBTOOL":  Fail("PA-0001.1", errs.NeedsSubtool),
		"fail with no code":          Fail("PA-0001.1", ""),
		"complete with no state":     Complete("PA-0001.1", "", nil),
		"complete with broken JSON":  Complete("PA-0001.1", "scored", []byte(`{"total":`)),
		"a result over 64 KiB":       Complete("PA-0001.1", "scored", []byte(`{"pad":"`+strings.Repeat("x", 70000)+`"}`)),
		"no lease":                   NeedsSubtool(""),
		"a call that does not exist": {Call: "reopen", Lease: "PA-0001.1"},
	}
	for name, r := range bad {
		if err := hub.Send(ctx, r); code(err) != errs.Parse {
			t.Errorf("%s: want E_PARSE without a request, got %v", name, err)
		}
	}
	if err := hub.Heartbeat(ctx, ""); code(err) != errs.Parse {
		t.Errorf("heartbeat with no lease: got %v", err)
	}
	if len(rec.seen) != 0 {
		t.Errorf("nothing should have been sent, but the hub saw %v", rec.seen)
	}
}

func TestHTTPRoundTripAgainstTheStandIn(t *testing.T) {
	m := NewMemory()
	// A reply that is broken, but whose lease can still be read, must
	// arrive byte for byte so that Assay can fail it.
	broken := []byte(`{"lease":"PA-0009.1","kind":"item","id":"PA-0009","state":"observed","results":"nonsense"}`)
	m.Add(reply("PA-0002", "normal"))
	m.Add(broken)
	m.Add(reply("PA-0001", "urgent"))

	srv := httptest.NewServer(m.Handler("made-up-token"))
	defer srv.Close()
	hub, err := NewHTTP(srv.URL, tokenFile(t, "made-up-token"))
	if err != nil {
		t.Fatal(err)
	}

	first, err := hub.Lease(ctx)
	if err != nil || first == nil || first.ID != "PA-0001.1" || string(first.Body) != string(reply("PA-0001", "urgent")) {
		t.Fatalf("the urgent item should come first, untouched: %+v, %v", first, err)
	}
	if err := hub.Heartbeat(ctx, first.ID); err != nil {
		t.Errorf("heartbeat: %v", err)
	}
	done := Complete(first.ID, "scored", []byte(`{"total": 86}`))
	if err := hub.Send(ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := hub.Send(ctx, done); err != nil {
		t.Errorf("a repeated call must be acknowledged again: %v", err)
	}
	if err := hub.Heartbeat(ctx, first.ID); !errors.Is(err, ErrLeaseGone) {
		t.Errorf("heartbeat on a closed lease: got %v", err)
	}

	second, _ := hub.Lease(ctx)
	if err := hub.Send(ctx, NeedsSubtool(second.ID)); err != nil {
		t.Fatal(err)
	}
	third, err := hub.Lease(ctx)
	if err != nil || third == nil || string(third.Body) != string(broken) {
		t.Fatalf("the broken reply should arrive whole: %+v, %v", third, err)
	}
	if err := hub.Send(ctx, Fail(third.ID, errs.Parse)); err != nil {
		t.Fatal(err)
	}
	if l, err := hub.Lease(ctx); l != nil || err != nil {
		t.Fatalf("with nothing left the answer is 204: %v, %v", l, err)
	}

	got := m.Completions()
	if len(got) != 3 || got[0].State != "scored" || string(got[0].Result) != `{"total":86}` ||
		got[1].Call != CallNeedsSubtool || got[2].Call != CallFail || got[2].Code != errs.Parse {
		t.Errorf("completions: %+v", got)
	}
}

func TestHTTPRefusesAReplyWithNoReadableLease(t *testing.T) {
	for _, body := range []string{``, `not json`, `[]`, `{"kind":"item"}`, `{"lease":7}`, `{"lease":"a b"}`, `{"lease":"x","lease":"y"}`} {
		srv := httptest.NewServer(&recorder{t: t, status: 200, body: body})
		hub, _ := NewHTTP(srv.URL, "")
		l, err := hub.Lease(ctx)
		srv.Close()
		if l != nil || code(err) != errs.Parse || reason(err) != errs.HubReplyInvalid {
			t.Errorf("body %q: got %+v, %v", body, l, err)
		}
	}
}

func TestHTTPRereadsTheTokenForEveryCall(t *testing.T) {
	rec := &recorder{t: t, status: 204}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	path := tokenFile(t, "first-token")
	hub, _ := NewHTTP(srv.URL, path)
	_, _ = hub.Lease(ctx)
	if err := os.WriteFile(path, []byte("rotated-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = hub.Lease(ctx)
	if len(rec.auth) != 2 || rec.auth[0] != "Bearer first-token" || rec.auth[1] != "Bearer rotated-token" {
		t.Errorf("tokens presented: %q", rec.auth)
	}

	// A missing token file means the pod cannot prove who it is. The call
	// is not attempted at all.
	missing, _ := NewHTTP(srv.URL, filepath.Join(t.TempDir(), "absent"))
	if _, err := missing.Lease(ctx); code(err) != errs.Timeout || len(rec.auth) != 2 {
		t.Errorf("missing token: err %v, requests %d", err, len(rec.auth))
	}
}

// TestHTTPActsOnARefusal walks the refusals Muster's contract lists and
// checks what the client makes of each: wait and retry, give up, or treat
// the lease as gone.
func TestHTTPActsOnARefusal(t *testing.T) {
	body := func(code string) string {
		return `{"error":"` + code + `","cause":"made_up_cause","reason":"a fixed sentence naming host db01.internal"}`
	}
	cases := []struct {
		name   string
		status int
		body   string
		code   errs.Code
		reason errs.Reason
		gone   bool
	}{
		{"not JSON, an unknown field", 400, body("E_PARSE"), errs.Parse, errs.HubRejectedRequest, false},
		{"no valid credential", 401, body("E_PARSE"), errs.Timeout, errs.HubUnavailable, false},
		{"not this caller's call", 403, body("E_PARSE"), errs.Timeout, errs.HubUnavailable, false},
		{"no such call", 404, body("E_PARSE"), errs.Parse, errs.HubRejectedRequest, false},
		{"wrong method", 405, body("E_PARSE"), errs.Parse, errs.HubRejectedRequest, false},
		{"request too large", 413, body("E_PARSE"), errs.Parse, errs.HubRejectedRequest, false},
		{"wrong content type", 415, body("E_PARSE"), errs.Parse, errs.HubRejectedRequest, false},
		{"lease not held, or move not allowed", 409, body("E_PARSE"), "", "", true},
		{"contract version not known", 422, body("E_NEEDS_SUBTOOL"), errs.NeedsSubtool, errs.HubContractUnknown, false},
		{"too many", 429, body("E_TIMEOUT"), errs.Timeout, errs.HubUnavailable, false},
		{"store too slow", 503, body("E_TIMEOUT"), errs.Timeout, errs.HubUnavailable, false},
		{"a record set aside", 500, body("E_PARSE"), errs.Parse, errs.HubRejectedRequest, false},
		{"a 500 with no body", 500, ``, errs.Timeout, errs.HubUnavailable, false},
		{"a 502 from something in between", 502, `<html>bad gateway</html>`, errs.Timeout, errs.HubUnavailable, false},
		{"a 400 with no body", 400, ``, errs.Parse, errs.HubRejectedRequest, false},
		{"a redirect", 302, ``, errs.Parse, errs.HubReplyInvalid, false},
		{"a status that is not an answer", 201, `{}`, errs.Parse, errs.HubReplyInvalid, false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(&recorder{t: t, status: tc.status, body: tc.body})
		hub, _ := NewHTTP(srv.URL, "")
		_, leaseErr := hub.Lease(ctx)
		results := map[string]error{
			"lease":     leaseErr,
			"heartbeat": hub.Heartbeat(ctx, "PA-0001.1"),
			"complete":  hub.Send(ctx, Complete("PA-0001.1", "scored", nil)),
			"fail":      hub.Send(ctx, Fail("PA-0001.1", errs.Parse)),
			"pause":     hub.Send(ctx, NeedsSubtool("PA-0001.1")),
		}
		srv.Close()
		for call, err := range results {
			if tc.gone {
				if !errors.Is(err, ErrLeaseGone) {
					t.Errorf("%s, %s: want the lease treated as gone, got %v", tc.name, call, err)
				}
				continue
			}
			if code(err) != tc.code || reason(err) != tc.reason {
				t.Errorf("%s, %s: got %v, want %s %s", tc.name, call, err, tc.code, tc.reason)
			}
			// Nothing the hub wrote may be repeated.
			if err != nil && (strings.Contains(err.Error(), "db01") || strings.Contains(err.Error(), "made_up_cause") || strings.Contains(err.Error(), "sentence")) {
				t.Errorf("%s, %s: the error repeats the hub's words: %v", tc.name, call, err)
			}
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
	if _, err := hub.Lease(ctx); err == nil {
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
	_ = ln.Close()

	token := "made-up-token-8842"
	for name, base := range map[string]string{"slow hub": slow.URL, "nothing listening": dead} {
		hub, _ := NewHTTP(base, tokenFile(t, token))
		short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		_, leaseErr := hub.Lease(short)
		sendErr := hub.Send(short, Complete("PA-0001.1", "scored", nil))
		beatErr := hub.Heartbeat(short, "PA-0001.1")
		cancel()
		for _, err := range []error{leaseErr, sendErr, beatErr} {
			if code(err) != errs.Timeout {
				t.Errorf("%s: want E_TIMEOUT, got %v", name, err)
				continue
			}
			text := err.Error()
			for _, leak := range []string{"127.0.0.1", "http", "://", "lease", "PA-", token, strings.TrimPrefix(base, "http://")} {
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

// TestStandInIsAsStrictAsTheContract checks the fake server itself, since
// the client is only as well tested as the thing it is tested against:
// unknown fields, a missing or unknown contract, the wrong method, type or
// credential are all refused, and every refusal is {error, cause, reason}.
func TestStandInIsAsStrictAsTheContract(t *testing.T) {
	m := NewMemory()
	m.Add(reply("PA-0001", "normal"))
	srv := httptest.NewServer(m.Handler("made-up-token"))
	defer srv.Close()

	type refusalBody struct {
		Error  string `json:"error"`
		Cause  string `json:"cause"`
		Reason string `json:"reason"`
	}
	do := func(method, path, auth, ctype, body string) (int, refusalBody) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(resp.Body)
		var rb refusalBody
		if resp.StatusCode != 200 && resp.StatusCode != 204 {
			dec := json.NewDecoder(strings.NewReader(string(data)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&rb); err != nil || rb.Error == "" || rb.Cause == "" || rb.Reason == "" {
				t.Errorf("%s %s: a refusal must be {error, cause, reason}, got %s", method, path, data)
			}
		}
		return resp.StatusCode, rb
	}
	const good, json = "Bearer made-up-token", "application/json"

	cases := []struct {
		name               string
		method, path, auth string
		ctype, body        string
		status             int
		code               string
	}{
		{"an unknown field", "POST", "/v1/lease", good, json, `{"contract":"v1","lane":"urgent"}`, 400, "E_PARSE"},
		{"the old lease body", "POST", "/v1/lease", good, json, `{"worker":"assay","state":"observed","lane":"urgent"}`, 400, "E_PARSE"},
		{"no contract", "POST", "/v1/lease", good, json, `{}`, 400, "E_PARSE"},
		{"not JSON", "POST", "/v1/lease", good, json, `contract=v1`, 400, "E_PARSE"},
		{"two values", "POST", "/v1/lease", good, json, `{"contract":"v1"}{"contract":"v1"}`, 400, "E_PARSE"},
		{"a contract Muster does not know", "POST", "/v1/lease", good, json, `{"contract":"v2"}`, 422, "E_NEEDS_SUBTOOL"},
		{"no credential", "POST", "/v1/lease", "", json, `{"contract":"v1"}`, 401, "E_PARSE"},
		{"the wrong credential", "POST", "/v1/lease", "Bearer other", json, `{"contract":"v1"}`, 401, "E_PARSE"},
		{"the wrong method", "GET", "/v1/lease", good, json, ``, 405, "E_PARSE"},
		{"the wrong content type", "POST", "/v1/lease", good, "text/plain", `{"contract":"v1"}`, 415, "E_PARSE"},
		{"too large", "POST", "/v1/complete", good, json, `{"contract":"v1","lease":"x","state":"scored","result":{"pad":"` + strings.Repeat("x", 70000) + `"}}`, 413, "E_PARSE"},
		{"the old path", "POST", "/v1/leases", good, json, `{"contract":"v1"}`, 404, "E_PARSE"},
		{"the old completion path", "POST", "/v1/leases/PA-0001.1/complete", good, json, `{"contract":"v1"}`, 404, "E_PARSE"},
		{"complete with no state", "POST", "/v1/complete", good, json, `{"contract":"v1","lease":"PA-0001.1"}`, 400, "E_PARSE"},
		{"complete with an unknown field", "POST", "/v1/complete", good, json, `{"contract":"v1","lease":"PA-0001.1","state":"scored","code":"E_PARSE"}`, 400, "E_PARSE"},
		{"fail with no code", "POST", "/v1/fail", good, json, `{"contract":"v1","lease":"PA-0001.1"}`, 400, "E_PARSE"},
		{"fail with E_NEEDS_SUBTOOL", "POST", "/v1/fail", good, json, `{"contract":"v1","lease":"PA-0001.1","code":"E_NEEDS_SUBTOOL"}`, 400, "E_PARSE"},
		{"needs_subtool with a reason", "POST", "/v1/needs_subtool", good, json, `{"contract":"v1","lease":"PA-0001.1","reason":"unknown_category"}`, 400, "E_PARSE"},
		{"heartbeat with no lease", "POST", "/v1/heartbeat", good, json, `{"contract":"v1"}`, 400, "E_PARSE"},
		{"a lease that is not held", "POST", "/v1/heartbeat", good, json, `{"contract":"v1","lease":"PA-0001.1"}`, 409, "E_PARSE"},
		{"a closing call on a lease not held", "POST", "/v1/needs_subtool", good, json, `{"contract":"v1","lease":"PA-0001.1"}`, 409, "E_PARSE"},
	}
	for _, tc := range cases {
		status, rb := do(tc.method, tc.path, tc.auth, tc.ctype, tc.body)
		if status != tc.status || rb.Error != tc.code {
			t.Errorf("%s: got %d %s, want %d %s", tc.name, status, rb.Error, tc.status, tc.code)
		}
	}
	// None of the refusals took the queued item.
	if status, _ := do("POST", "/v1/lease", good, "application/json; charset=utf-8", `{"contract":"v1"}`); status != 200 {
		t.Errorf("a good lease call after all that: %d", status)
	}
	// With the lease held, an evaluation Logbook would refuse is refused on
	// complete without closing the lease, and a well-formed one succeeds.
	if status, rb := do("POST", "/v1/complete", good, json,
		`{"contract":"v1","lease":"PA-0001.1","state":"scored","result":{"gates":[{"id":"g","pass":true}]}}`); status != 400 || rb.Error != "E_PARSE" || rb.Cause != "bad_field" {
		t.Errorf("malformed evaluation: got %d %+v", status, rb)
	}
	if status, rb := do("POST", "/v1/complete", good, json,
		`{"contract":"v1","lease":"PA-0001.1","state":"scored","result":{"profile":"pricing","gates":{"public_source":{"passed":true}},"factors":{"need":{"rating":4,"weight":4}},"score":16,"recommendation":"Build"}}`); status != 422 || rb.Error != "E_NEEDS_SUBTOOL" || rb.Cause != "unknown_value" {
		t.Errorf("unknown recommendation: got %d %+v", status, rb)
	}
	if status, _ := do("POST", "/v1/complete", good, json,
		`{"contract":"v1","lease":"PA-0001.1","state":"scored","result":{"profile":"pricing","gates":{"public_source":{"passed":true}},"factors":{"need":{"rating":4,"weight":4}},"score":16,"recommendation":"reject"}}`); status != 200 {
		t.Errorf("valid evaluation: got %d", status)
	}
	if status, _ := do("POST", "/v1/lease", good, json, `{"contract":"v1"}`); status != 204 {
		t.Errorf("an empty queue answers 204, got %d", status)
	}
}
