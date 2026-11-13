package muster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/errs"
)

// Handler serves the stand-in over HTTP in the shape of Muster's contract
// v1, so that the HTTP client is exercised end to end against something
// that is as strict as the contract says Muster is:
//
//	POST /v1/lease          {contract}                        200 the lease, or 204
//	POST /v1/heartbeat      {contract, lease}                 200 {lease, expires}
//	POST /v1/complete       {contract, lease, state, result}  200 {id, state, next}
//	POST /v1/fail           {contract, lease, code}           200 {id, state, next or held}
//	POST /v1/needs_subtool  {contract, lease}                 200 {id, state}
//
// A body with a field the call does not define is refused. Every refusal
// is a status and a body of {error, cause, reason}. When token is not
// empty, calls must present it as a bearer token.
func (m *Memory) Handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls := map[string]func(http.ResponseWriter, []byte){
			pathLease:        m.serveLease,
			pathHeartbeat:    m.serveHeartbeat,
			pathComplete:     m.serveComplete,
			pathFail:         m.serveFail,
			pathNeedsSubtool: m.serveNeedsSubtool,
		}
		serve, ok := calls[r.URL.Path]
		if !ok {
			refuse(w, http.StatusNotFound, errs.Parse, "no_such_call", "no such call")
			return
		}
		if r.Method != http.MethodPost {
			refuse(w, http.StatusMethodNotAllowed, errs.Parse, "wrong_method", "this call does not accept that method")
			return
		}
		if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
			refuse(w, http.StatusUnsupportedMediaType, errs.Parse, "wrong_type", "the content type must be application/json")
			return
		}
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			refuse(w, http.StatusUnauthorized, errs.Parse, "no_credential", "no valid credential was presented")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, contract.MaxRequestBytes+1))
		if err != nil {
			refuse(w, http.StatusBadRequest, errs.Parse, "unreadable", "the request could not be read")
			return
		}
		if len(body) > contract.MaxRequestBytes {
			refuse(w, http.StatusRequestEntityTooLarge, errs.Parse, "too_large", "the request is too large")
			return
		}
		serve(w, body)
	})
}

// decode reads a request body into the fields its call defines. An unknown
// field, a missing contract or one Muster does not know is refused.
func decode(w http.ResponseWriter, body []byte, into interface{ version() string }) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil || dec.More() {
		refuse(w, http.StatusBadRequest, errs.Parse, "malformed", "a required field is missing or malformed")
		return false
	}
	switch into.version() {
	case contract.Version:
		return true
	case "":
		refuse(w, http.StatusBadRequest, errs.Parse, "malformed", "a required field is missing or malformed")
	default:
		refuse(w, http.StatusUnprocessableEntity, errs.NeedsSubtool, "unknown_version", "this contract version is not known")
	}
	return false
}

type versioned struct {
	Contract string `json:"contract"`
}

func (v versioned) version() string { return v.Contract }

type leased struct {
	versioned
	Lease string `json:"lease"`
}

func (m *Memory) serveLease(w http.ResponseWriter, body []byte) {
	var req versioned
	if !decode(w, body, &req) {
		return
	}
	m.mu.Lock()
	err := takeFault(&m.leaseFaults)
	var item *memItem
	if err == nil {
		item = m.next()
	}
	m.mu.Unlock()
	switch {
	case err != nil:
		refuseError(w, err)
	case item == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		// Served byte for byte, broken or not. Judging it is Assay's job.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(item.body)
	}
}

func (m *Memory) serveHeartbeat(w http.ResponseWriter, body []byte) {
	var req leased
	if !decode(w, body, &req) {
		return
	}
	if req.Lease == "" {
		refuse(w, http.StatusBadRequest, errs.Parse, "malformed", "a required field is missing or malformed")
		return
	}
	if err := m.Heartbeat(context.Background(), req.Lease); err != nil {
		refuseError(w, err)
		return
	}
	answer(w, map[string]any{"lease": req.Lease, "expires": "2026-01-05T09:02:00Z"})
}

func (m *Memory) serveComplete(w http.ResponseWriter, body []byte) {
	var req struct {
		leased
		State  string          `json:"state"`
		Result json.RawMessage `json:"result"`
	}
	if !decode(w, body, &req) {
		return
	}
	if req.Lease == "" || req.State == "" {
		refuse(w, http.StatusBadRequest, errs.Parse, "malformed", "a required field is missing or malformed")
		return
	}
	id := m.itemID(req.Lease)
	if err := m.Send(context.Background(), Complete(req.Lease, req.State, req.Result)); err != nil {
		refuseError(w, err)
		return
	}
	answer(w, map[string]any{"id": id, "state": req.State, "next": "countersign"})
}

func (m *Memory) serveFail(w http.ResponseWriter, body []byte) {
	var req struct {
		leased
		Code errs.Code `json:"code"`
	}
	if !decode(w, body, &req) {
		return
	}
	if req.Lease == "" || req.Code == "" {
		refuse(w, http.StatusBadRequest, errs.Parse, "malformed", "a required field is missing or malformed")
		return
	}
	if req.Code == errs.NeedsSubtool {
		refuse(w, http.StatusBadRequest, errs.Parse, "use_needs_subtool", "report a missing rule with the needs_subtool call")
		return
	}
	id := m.itemID(req.Lease)
	if err := m.Send(context.Background(), Fail(req.Lease, req.Code)); err != nil {
		refuseError(w, err)
		return
	}
	if req.Code == errs.Parse {
		answer(w, map[string]any{"id": id, "state": contract.StateObserved, "held": true})
		return
	}
	answer(w, map[string]any{"id": id, "state": contract.StateObserved, "next": contract.Worker})
}

func (m *Memory) serveNeedsSubtool(w http.ResponseWriter, body []byte) {
	var req leased
	if !decode(w, body, &req) {
		return
	}
	if req.Lease == "" {
		refuse(w, http.StatusBadRequest, errs.Parse, "malformed", "a required field is missing or malformed")
		return
	}
	id := m.itemID(req.Lease)
	if err := m.Send(context.Background(), NeedsSubtool(req.Lease)); err != nil {
		refuseError(w, err)
		return
	}
	answer(w, map[string]any{"id": id, "state": contract.StateNeedsSubtool})
}

// itemID names the item behind a lease, held or already closed.
func (m *Memory) itemID(lease string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if item, ok := m.held[lease]; ok {
		return item.id
	}
	for _, c := range m.completed {
		if c.Lease == lease {
			return c.ItemID
		}
	}
	return ""
}

func answer(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// refuse writes a refusal: a status and {error, cause, reason}. The cause
// is a fixed word and the reason a fixed sentence; neither is built from
// the request.
func refuse(w http.ResponseWriter, status int, code errs.Code, cause, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": string(code), "cause": cause, "reason": reason})
}

// refuseError turns an error from the stand-in into the refusal Muster
// would send for it.
func refuseError(w http.ResponseWriter, err error) {
	var pub *errs.Error
	switch {
	case errors.Is(err, ErrLeaseGone):
		refuse(w, http.StatusConflict, errs.Parse, "lease_not_held", "the lease is not held")
	case errors.As(err, &pub) && pub.Code == errs.Parse:
		refuse(w, http.StatusBadRequest, errs.Parse, "malformed", "a required field is missing or malformed")
	default:
		refuse(w, http.StatusServiceUnavailable, errs.Timeout, "busy", "too busy; try again")
	}
}
