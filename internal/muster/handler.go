package muster

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ClearedStack/assay/internal/contract"
)

// Handler serves a Hub over the provisional v1 HTTP shape, so that the HTTP
// client can be exercised end to end against the in-memory stand-in:
//
//	POST /v1/leases                      {"worker","state","lane"} -> 200, Lease-Id header, body = the record; or 204
//	POST /v1/leases/{lease_id}/complete  the outcome               -> 204, or 404 if the lease is gone
//
// When token is not empty, requests must present it as a bearer token.
func Handler(hub Hub, token string) http.Handler {
	prefix := "/" + contract.Version + "/leases"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Contract-Version") != contract.Version {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxReplyBytes))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case r.URL.Path == prefix:
			var req leaseRequest
			if err := json.Unmarshal(body, &req); err != nil || req.State != contract.StateObserved {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			lease, err := hub.Lease(r.Context(), req.Lane)
			if err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if lease == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// The record is passed through byte for byte, broken or not.
			// Judging it is Assay's job.
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(LeaseHeader, lease.ID)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(lease.Record)
		case strings.HasPrefix(r.URL.Path, prefix+"/") && strings.HasSuffix(r.URL.Path, "/complete"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix+"/"), "/complete")
			err := hub.Complete(r.Context(), id, body)
			switch {
			case err == nil:
				w.WriteHeader(http.StatusNoContent)
			case errors.Is(err, ErrLeaseGone):
				w.WriteHeader(http.StatusNotFound)
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}
