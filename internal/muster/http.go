package muster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/errs"
)

// maxReplyBytes bounds a hub reply: one lease, with the results of earlier
// workers inside it.
const maxReplyBytes = contract.MaxLeaseBytes

// The five worker calls of contract v1.
const (
	pathLease        = "/v1/lease"
	pathHeartbeat    = "/v1/heartbeat"
	pathComplete     = "/v1/complete"
	pathFail         = "/v1/fail"
	pathNeedsSubtool = "/v1/needs_subtool"
)

// HTTP talks to a real Muster over cluster-private HTTP, presenting the
// pod's ServiceAccount token.
//
// Errors from this type never carry the hub's address, the request, the
// token or anything the hub wrote. Whatever went wrong is reduced to a
// public code and a fixed reason.
type HTTP struct {
	base      *url.URL
	tokenFile string
	client    *http.Client
}

// NewHTTP returns a client for the hub at baseURL. tokenFile is the path of
// the ServiceAccount token, read afresh for every call because the kubelet
// rotates it; an empty path sends no token.
func NewHTTP(baseURL, tokenFile string) (*HTTP, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		// The value is not echoed: it is configuration, and may be wrong in
		// ways that should not be printed.
		return nil, errors.New("the hub address is not a plain http or https URL")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	transport := &http.Transport{
		Proxy:                 nil, // the hub is cluster-private; never go through a proxy
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableCompression:    true,
	}
	client := &http.Client{
		Transport: transport,
		// A redirect could carry the token to another host. Never follow one.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &HTTP{base: u, tokenFile: tokenFile, client: client}, nil
}

// request is the body of every worker call. Each call uses the fields it
// defines and no others; Muster refuses a field a call does not define.
type request struct {
	Contract string          `json:"contract"`
	Lease    string          `json:"lease,omitempty"`
	State    string          `json:"state,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Code     errs.Code       `json:"code,omitempty"`
}

// refusal is the body Muster sends with any status but 200 and 204. Only
// the code is read. The cause and the reason are for people; they are never
// logged or passed on from here.
type refusal struct {
	Error string `json:"error"`
}

// Lease implements Hub.
func (h *HTTP) Lease(ctx context.Context) (*Lease, error) {
	status, reply, perr := h.post(ctx, pathLease, request{Contract: contract.Version})
	if perr != nil {
		return nil, perr
	}
	switch status {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		// The lease is read out of the reply and nothing else is judged
		// here. A reply that is broken in any other way still reaches the
		// engine whole, so it can be failed against the right lease.
		id, ok := contract.LeaseID(reply)
		if !ok {
			return nil, replyInvalid()
		}
		return &Lease{ID: id, Body: reply}, nil
	}
	return nil, refused(status, reply)
}

// Heartbeat implements Hub.
func (h *HTTP) Heartbeat(ctx context.Context, lease string) error {
	return h.call(ctx, pathHeartbeat, request{Contract: contract.Version, Lease: lease})
}

// Send implements Hub.
func (h *HTTP) Send(ctx context.Context, r Report) error {
	req := request{Contract: contract.Version, Lease: r.Lease}
	switch r.Call {
	case CallComplete:
		if r.State == "" || (len(r.Result) > 0 && !json.Valid(r.Result)) {
			return rejected()
		}
		req.State, req.Result = r.State, r.Result
		return h.call(ctx, pathComplete, req)
	case CallFail:
		// E_NEEDS_SUBTOOL has a call of its own and is refused here.
		if r.Code != errs.Timeout && r.Code != errs.Parse {
			return rejected()
		}
		req.Code = r.Code
		return h.call(ctx, pathFail, req)
	case CallNeedsSubtool:
		return h.call(ctx, pathNeedsSubtool, req)
	}
	return rejected()
}

// call makes a call whose only answer that matters is "acknowledged".
func (h *HTTP) call(ctx context.Context, path string, req request) error {
	if req.Lease == "" {
		return rejected()
	}
	status, reply, perr := h.post(ctx, path, req)
	if perr != nil {
		return perr
	}
	if status == http.StatusOK {
		return nil
	}
	return refused(status, reply)
}

// post sends one request and returns the status and the body.
func (h *HTTP) post(ctx context.Context, path string, body request) (int, []byte, *errs.Error) {
	data, err := json.Marshal(body)
	if err != nil || len(data) > contract.MaxRequestBytes {
		return 0, nil, rejected()
	}
	target := *h.base
	target.Path += path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(data))
	if err != nil {
		return 0, nil, unavailable()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if h.tokenFile != "" {
		token, err := os.ReadFile(h.tokenFile)
		if err != nil || len(bytes.TrimSpace(token)) == 0 {
			return 0, nil, unavailable()
		}
		req.Header.Set("Authorization", "Bearer "+string(bytes.TrimSpace(token)))
	}
	resp, err := h.client.Do(req)
	if err != nil {
		// err names the hub's address. It is dropped here, on purpose.
		return 0, nil, timeoutError()
	}
	defer func() { _ = resp.Body.Close() }()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes+1))
	if err != nil {
		return 0, nil, timeoutError()
	}
	if len(reply) > maxReplyBytes {
		return 0, nil, replyInvalid()
	}
	return resp.StatusCode, reply, nil
}

// refused turns a refusal into the error the worker acts on.
//
// Muster sends one of the three codes with every refusal, and that code
// decides: E_TIMEOUT means wait and try again; E_PARSE means trying again
// will not help. There are two exceptions. A refused credential (401, 403)
// is waited out, because the token is rotated and work must not be dropped
// over it. And a 409 means the lease is not held or the move is not
// allowed, which the worker handles as a lease that is gone.
//
// A reply with no readable code is judged by its status alone.
func refused(status int, reply []byte) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return unavailable()
	}
	if status == http.StatusConflict {
		return ErrLeaseGone
	}
	var r refusal
	_ = json.Unmarshal(reply, &r)
	switch errs.Code(r.Error) {
	case errs.Timeout:
		return unavailable()
	case errs.Parse:
		return rejected()
	case errs.NeedsSubtool:
		return contractUnknown()
	}
	switch {
	case status >= 500, status == http.StatusTooManyRequests, status == http.StatusRequestTimeout:
		return unavailable()
	case status >= 400:
		return rejected()
	}
	return replyInvalid()
}
