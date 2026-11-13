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
	"regexp"
	"strings"
	"time"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/errs"
)

// maxReplyBytes bounds a hub reply: one record plus a small envelope.
const maxReplyBytes = contract.MaxRecordBytes + 4096

// LeaseHeader is the reply header that names a lease.
const LeaseHeader = "Lease-Id"

var leaseIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

// HTTP talks to a real Muster over cluster-private HTTP, presenting the
// pod's ServiceAccount token.
//
// Errors from this type never carry the hub's address, the request or the
// token. Whatever went wrong underneath is reduced to a public code and a
// fixed reason.
type HTTP struct {
	base      *url.URL
	tokenFile string
	client    *http.Client
}

// NewHTTP returns a client for the hub at baseURL. tokenFile is the path of
// the ServiceAccount token, read afresh for every request because the
// kubelet rotates it; an empty path sends no token.
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

type leaseRequest struct {
	Worker string `json:"worker"`
	State  string `json:"state"`
	Lane   Lane   `json:"lane"`
}

// Lease implements Hub.
func (h *HTTP) Lease(ctx context.Context, lane Lane) (*Lease, error) {
	body, _ := json.Marshal(leaseRequest{Worker: contract.Worker, State: contract.StateObserved, Lane: lane})
	status, leaseID, reply, perr := h.post(ctx, "/"+contract.Version+"/leases", body)
	if perr != nil {
		return nil, perr
	}
	switch status {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		// The body is the record itself, untouched, and the lease is named
		// in a header. A record that is not even JSON still arrives whole,
		// so Assay can refuse it against the right lease.
		if !leaseIDPattern.MatchString(leaseID) {
			return nil, replyInvalid()
		}
		return &Lease{ID: leaseID, Lane: lane, Record: reply}, nil
	}
	return nil, statusError(status)
}

// Complete implements Hub.
func (h *HTTP) Complete(ctx context.Context, leaseID string, outcome []byte) error {
	if !leaseIDPattern.MatchString(leaseID) {
		return replyInvalid()
	}
	status, _, _, perr := h.post(ctx, "/"+contract.Version+"/leases/"+url.PathEscape(leaseID)+"/complete", outcome)
	if perr != nil {
		return perr
	}
	switch status {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusNotFound, http.StatusConflict, http.StatusGone:
		return ErrLeaseGone
	}
	return statusError(status)
}

// post sends one request and returns the status, the Lease-Id header and
// the body.
func (h *HTTP) post(ctx context.Context, path string, body []byte) (int, string, []byte, *errs.Error) {
	target := *h.base
	target.Path += path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return 0, "", nil, unavailable()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Contract-Version", contract.Version)
	if h.tokenFile != "" {
		token, err := os.ReadFile(h.tokenFile)
		if err != nil || len(bytes.TrimSpace(token)) == 0 {
			return 0, "", nil, unavailable()
		}
		req.Header.Set("Authorization", "Bearer "+string(bytes.TrimSpace(token)))
	}
	resp, err := h.client.Do(req)
	if err != nil {
		// err names the hub's address. It is dropped here, on purpose.
		return 0, "", nil, timeoutError()
	}
	defer func() { _ = resp.Body.Close() }()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes+1))
	if err != nil {
		return 0, "", nil, timeoutError()
	}
	if len(reply) > maxReplyBytes {
		return 0, "", nil, replyInvalid()
	}
	return resp.StatusCode, resp.Header.Get(LeaseHeader), reply, nil
}

// statusError turns an unexpected HTTP status into a public error. A hub
// that is failing, busy or refusing our token is "not serving": wait and try
// again. Any other refusal means the two sides disagree about the contract.
func statusError(status int) *errs.Error {
	switch {
	case status >= 500, status == http.StatusTooManyRequests, status == http.StatusRequestTimeout,
		status == http.StatusUnauthorized, status == http.StatusForbidden:
		return unavailable()
	case status >= 400:
		return errs.New(errs.Parse, errs.HubRejectedRequest, "hub")
	}
	return replyInvalid()
}

func timeoutError() *errs.Error { return errs.New(errs.Timeout, errs.HubTimeout, "hub") }
func unavailable() *errs.Error  { return errs.New(errs.Timeout, errs.HubUnavailable, "hub") }
func replyInvalid() *errs.Error { return errs.New(errs.Parse, errs.HubReplyInvalid, "hub") }
