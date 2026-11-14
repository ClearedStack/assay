package assay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
	"github.com/ClearedStack/assay/internal/errs"
	"github.com/ClearedStack/assay/internal/muster"
	"github.com/ClearedStack/assay/tools"
)

// example is one of Muster's saved contract examples, as described in
// testdata/contract/README.md.
type example struct {
	name  string
	About string `json:"about"`
	Given struct {
		Calls     []json.RawMessage `json:"calls"`
		Condition string            `json:"condition"`
	} `json:"given"`
	Request struct {
		As          string          `json:"as"`
		Method      string          `json:"method"`
		Path        string          `json:"path"`
		ContentType string          `json:"content_type"`
		Body        json.RawMessage `json:"body"`
		Raw         *string         `json:"raw"`
	} `json:"request"`
	Expect struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"expect"`
}

func loadExamples(t *testing.T) []example {
	t.Helper()
	paths, err := filepath.Glob("testdata/contract/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("Muster's contract examples have not been copied into testdata/contract yet")
	}
	sort.Strings(paths)
	var out []example
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		ex := example{name: strings.TrimSuffix(filepath.Base(path), ".json")}
		if err := json.Unmarshal(data, &ex); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if ex.Request.Method == "" || ex.Request.Path == "" || ex.Expect.Status == 0 {
			t.Fatalf("%s: not an example this test understands", path)
		}
		out = append(out, ex)
	}
	return out
}

// compactJSON removes the white space between fields, which is how
// Muster's examples say bodies are compared.
func compactJSON(t *testing.T, data []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return buf.String()
}

var workerPaths = map[string]bool{
	"/v1/lease": true, "/v1/heartbeat": true, "/v1/complete": true, "/v1/fail": true, "/v1/needs_subtool": true,
}

// answering is a fake Muster that gives one fixed answer and writes down
// what it was sent.
type answering struct {
	status int
	body   string
	seen   []string // "METHOD path body"
}

func (a *answering) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	a.seen = append(a.seen, r.Method+" "+r.URL.Path+" "+string(data))
	w.WriteHeader(a.status)
	_, _ = io.WriteString(w, a.body)
}

// theCall makes, through Assay's own client, the call an example's request
// describes. made is false when the request is one Assay's client has no
// way to send (a field it never sends, a body that is not JSON, the wrong
// method), in which case the ordinary call for that path is made instead,
// so that the answer is still acted on.
func theCall(t *testing.T, hub *muster.HTTP, ex example) (lease *muster.Lease, err error, exact bool) {
	t.Helper()
	ctx := context.Background()
	var body struct {
		Contract string          `json:"contract"`
		Lease    string          `json:"lease"`
		State    string          `json:"state"`
		Result   json.RawMessage `json:"result"`
		Code     string          `json:"code"`
	}
	exact = ex.Request.Method == "POST" && ex.Request.Raw == nil && ex.Request.ContentType == "" && ex.Request.As != ""
	if exact {
		dec := json.NewDecoder(bytes.NewReader(ex.Request.Body))
		dec.DisallowUnknownFields()
		exact = dec.Decode(&body) == nil && body.Contract == contract.Version
	}
	path := ex.Request.Path
	if !workerPaths[path] {
		path, exact = "/v1/lease", false
	}
	if !exact {
		body.Lease, body.State, body.Code, body.Result = "PA-0001.1", "scored", "E_PARSE", nil
	}
	switch path {
	case "/v1/lease":
		exact = exact && body.Lease == "" && body.State == "" && body.Code == "" && body.Result == nil
		lease, err = hub.Lease(ctx)
	case "/v1/heartbeat":
		exact = exact && body.Lease != "" && body.State == "" && body.Code == "" && body.Result == nil
		if body.Lease == "" {
			body.Lease = "PA-0001.1"
		}
		err = hub.Heartbeat(ctx, body.Lease)
	case "/v1/complete":
		exact = exact && body.Code == ""
		err = hub.Send(ctx, muster.Complete(body.Lease, body.State, body.Result))
	case "/v1/fail":
		exact = exact && body.State == "" && body.Result == nil
		err = hub.Send(ctx, muster.Fail(body.Lease, errs.Code(body.Code)))
	case "/v1/needs_subtool":
		exact = exact && body.State == "" && body.Code == "" && body.Result == nil
		err = hub.Send(ctx, muster.NeedsSubtool(body.Lease))
	}
	return lease, err, exact
}

// TestContractExamples replays every one of Muster's saved examples through
// Assay's hub client.
//
// Where the example's request is one Assay can make, the request Assay
// sends must match the example's to the byte. In every case the example's
// answer is given back, and Assay must act on it as the contract says: take
// the lease, accept the acknowledgement, wait and retry, give the call up,
// or treat the lease as gone.
func TestContractExamples(t *testing.T) {
	examples := loadExamples(t)
	exactCalls := map[string]int{}
	for _, ex := range examples {
		t.Run(ex.name, func(t *testing.T) {
			fake := &answering{status: ex.Expect.Status}
			if len(ex.Expect.Body) > 0 {
				fake.body = compactJSON(t, ex.Expect.Body)
			}
			srv := httptest.NewServer(fake)
			defer srv.Close()
			hub, err := muster.NewHTTP(srv.URL, "")
			if err != nil {
				t.Fatal(err)
			}
			lease, err, exact := theCall(t, hub, ex)

			// What Assay sent.
			if exact && len(fake.seen) == 1 {
				want := ex.Request.Method + " " + ex.Request.Path + " " + compactJSON(t, ex.Request.Body)
				if fake.seen[0] != want {
					t.Errorf("request differs from the example:\n got %s\nwant %s", fake.seen[0], want)
				}
				if ex.Expect.Status == 200 || ex.Expect.Status == 204 {
					exactCalls[ex.Request.Path]++
				}
			}
			if len(fake.seen) == 0 {
				// The client refused to send it. That is only right when
				// Muster would have refused it as a bad request too.
				var pub *errs.Error
				if ex.Expect.Status != 400 || !errors.As(err, &pub) || pub.Code != errs.Parse {
					t.Fatalf("the client would not send this request, but Muster answers it with %d", ex.Expect.Status)
				}
				return
			}

			// What Assay made of the answer.
			var refusal struct {
				Error string `json:"error"`
				Cause string `json:"cause"`
			}
			_ = json.Unmarshal(ex.Expect.Body, &refusal)
			var pub *errs.Error
			errors.As(err, &pub)
			switch {
			case ex.Expect.Status == 204:
				if lease != nil || err != nil {
					t.Errorf("204 means nothing to do; got %v, %v", lease, err)
				}
			case ex.Expect.Status == 200 && ex.Request.Path == "/v1/lease" && workerPaths[ex.Request.Path]:
				var want struct {
					Lease string `json:"lease"`
				}
				_ = json.Unmarshal(ex.Expect.Body, &want)
				if err != nil || lease == nil || lease.ID != want.Lease || string(lease.Body) != fake.body {
					t.Errorf("the lease %q should be taken, with its body untouched; got %+v, %v", want.Lease, lease, err)
				}
			case ex.Expect.Status == 200 && workerPaths[ex.Request.Path]:
				if err != nil {
					t.Errorf("200 is an acknowledgement; got %v", err)
				}
			case ex.Expect.Status >= 200 && ex.Expect.Status < 300:
				// An answer to a call Assay never makes (items, health,
				// edits, the feed). Given to a lease call it is not a
				// lease, and must not be taken for one.
				if lease != nil {
					t.Errorf("an answer that is not a lease was taken for one: %+v", lease)
				}
			case ex.Expect.Status == 401 || ex.Expect.Status == 403:
				if pub == nil || pub.Code != errs.Timeout {
					t.Errorf("a refused credential is waited out, not given up on; got %v", err)
				}
			case ex.Expect.Status == 409:
				if !errors.Is(err, muster.ErrLeaseGone) {
					t.Errorf("409 (%s) means the report has nowhere to go; got %v", refusal.Cause, err)
				}
			case refusal.Error == "E_TIMEOUT":
				if pub == nil || pub.Code != errs.Timeout {
					t.Errorf("E_TIMEOUT (%s) means wait and try again; got %v", refusal.Cause, err)
				}
			case refusal.Error == "E_PARSE":
				if pub == nil || pub.Code != errs.Parse {
					t.Errorf("E_PARSE (%s) means trying again will not help; got %v", refusal.Cause, err)
				}
			case refusal.Error == "E_NEEDS_SUBTOOL":
				if pub == nil || pub.Code != errs.NeedsSubtool {
					t.Errorf("E_NEEDS_SUBTOOL (%s) means the two sides disagree about the contract; got %v", refusal.Cause, err)
				}
			default:
				t.Errorf("status %d with code %q: this test does not know what the contract asks here", ex.Expect.Status, refusal.Error)
			}
			// Nothing Muster wrote in a refusal may be repeated by Assay.
			if err != nil && refusal.Cause != "" && strings.Contains(err.Error(), refusal.Cause) {
				t.Errorf("the error repeats the hub's words: %v", err)
			}
		})
	}
	// Every one of the five worker calls must have been matched, to the
	// byte, against at least one of Muster's own examples.
	for path := range workerPaths {
		if exactCalls[path] == 0 {
			t.Errorf("no example of %s was matched exactly; the examples or the client have changed", path)
		}
	}
}

// TestContractExampleLeases takes every lease Muster's examples hand out
// and gives it to Assay's engine. A lease for anything but an observed item
// must be failed, never scored. The one lease the examples address to
// Assay carries a made-up observation with no category, which Assay has no
// rule for, so it is paused.
func TestContractExampleLeases(t *testing.T) {
	set, err := tools.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(set)
	forAssay := 0
	for _, ex := range loadExamples(t) {
		if ex.Request.Path != "/v1/lease" || ex.Expect.Status != 200 {
			continue
		}
		body := []byte(compactJSON(t, ex.Expect.Body))
		var head struct {
			Lease string `json:"lease"`
			Kind  string `json:"kind"`
			State string `json:"state"`
			ID    string `json:"id"`
		}
		_ = json.Unmarshal(body, &head)
		if id, ok := contract.LeaseID(body); !ok || id != head.Lease {
			t.Errorf("%s: the lease %q could not be read, so Assay could never answer it", ex.name, head.Lease)
		}
		out := eng.Evaluate(body)
		if out.State == contract.StateScored || out.State == contract.StateScreenedOut {
			t.Errorf("%s: made-up example data was scored", ex.name)
		}
		if ex.Request.As == contract.Worker {
			forAssay++
			if head.Kind != contract.KindItem || head.State != contract.StateObserved {
				t.Errorf("%s: Assay is leased a %s in %s", ex.name, head.Kind, head.State)
			}
			if out.Error == nil || out.Error.Code != errs.NeedsSubtool || out.Error.Reason != errs.UnknownCategory || out.ItemID != head.ID {
				t.Errorf("%s: want the item paused for want of a category, got %+v", ex.name, out.Error)
			}
			continue
		}
		if out.Error == nil || out.Error.Code != errs.Parse {
			t.Errorf("%s: a %s in %s is not Assay's to work and must be failed, got %+v", ex.name, head.Kind, head.State, out.Error)
		}
	}
	if forAssay == 0 {
		t.Error("no example leases an item to Assay")
	}
}

// TestStandInAnswersAsTheContractSays replays, against Assay's own stand-in
// for Muster, the examples whose answer does not depend on what Muster has
// on record. The stand-in must give the same status and the same body, to
// the byte. This keeps the thing Assay is tested against honest.
func TestStandInAnswersAsTheContractSays(t *testing.T) {
	stateless := map[string]bool{
		"lease": true, "lease-nothing": true,
		"refused-400-not_json": true, "refused-400-unknown_field": true, "refused-400-missing_field": true,
		"refused-400-bad_field": true, "refused-400-bad_field-use-needs_subtool": true,
		"refused-401-no_credential": true, "refused-404-no_such_call": true, "refused-405-wrong_method": true,
		"refused-409-lease_not_held": true, "refused-415-wrong_type": true, "refused-422-unknown_contract": true,
	}
	const token = "made-up-token"
	for _, ex := range loadExamples(t) {
		if !stateless[ex.name] {
			continue
		}
		delete(stateless, ex.name)
		t.Run(ex.name, func(t *testing.T) {
			hub := muster.NewMemory()
			if ex.Expect.Status == 200 {
				hub.Add([]byte(compactJSON(t, ex.Expect.Body)))
			}
			srv := httptest.NewServer(hub.Handler(token))
			defer srv.Close()

			var body io.Reader
			switch {
			case ex.Request.Raw != nil:
				body = strings.NewReader(*ex.Request.Raw)
			case len(ex.Request.Body) > 0:
				body = bytes.NewReader(ex.Request.Body)
			}
			req, err := http.NewRequest(ex.Request.Method, srv.URL+ex.Request.Path, body)
			if err != nil {
				t.Fatal(err)
			}
			ctype := ex.Request.ContentType
			if ctype == "" {
				ctype = "application/json"
			}
			req.Header.Set("Content-Type", ctype)
			if ex.Request.As != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			got, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != ex.Expect.Status {
				t.Fatalf("status %d, the contract says %d (%s)", resp.StatusCode, ex.Expect.Status, got)
			}
			if len(ex.Expect.Body) == 0 {
				if len(got) != 0 {
					t.Errorf("expected no body, got %s", got)
				}
				return
			}
			if compactJSON(t, got) != compactJSON(t, ex.Expect.Body) {
				t.Errorf("body differs from the contract:\n got %s\nwant %s", compactJSON(t, got), compactJSON(t, ex.Expect.Body))
			}
		})
	}
	for name := range stateless {
		t.Errorf("the example %s is no longer in testdata/contract", name)
	}
}
