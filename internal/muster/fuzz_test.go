package muster

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/errs"
)

func FuzzHTTPClientRefusal(f *testing.F) {
	paths, _ := filepath.Glob("../../testdata/contract/*.json")
	statuses := []uint16{200, 204, 400, 401, 403, 404, 409, 422, 500, 502, 503}
	for i, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			f.Add(statuses[i%len(statuses)], data)
		}
	}
	f.Add(uint16(409), []byte(`{"error":"E_PARSE","cause":"SECRET_HUB_MARKER_9917"}`))
	f.Add(uint16(500), []byte(`{"error":"E_TIMEOUT","cause":"SECRET_HUB_MARKER_9917"}`))
	f.Add(uint16(400), []byte(`{"error":"E_PARSE","reason":"SECRET_HUB_MARKER_9917"}`))
	f.Add(uint16(422), []byte(`SECRET_HUB_MARKER_9917`))

	var (
		mu       sync.Mutex
		nextCode = http.StatusNoContent
		nextBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		code := nextCode
		body := append([]byte(nil), nextBody...)
		mu.Unlock()

		w.WriteHeader(code)
		if code != http.StatusNoContent && code != http.StatusNotModified && (code < 100 || code >= 200) {
			_, _ = w.Write(body)
		}
	}))
	f.Cleanup(srv.Close)

	hub, err := NewHTTP(srv.URL, "")
	if err != nil {
		f.Fatal(err)
	}

	const marker = "SECRET_HUB_MARKER_9917"

	f.Fuzz(func(t *testing.T, rawStatus uint16, body []byte) {
		// Restrict to non-1xx HTTP status codes (200..599) while keeping 200..599
		// seed values unchanged.
		code := int(rawStatus)
		if code < 200 || code > 599 {
			code = 200 + code%400
		}

		mu.Lock()
		nextCode = code
		nextBody = body
		mu.Unlock()

		ctx := context.Background()
		_, lerr := hub.Lease(ctx)
		herr := hub.Heartbeat(ctx, "PA-0001.1")
		serr := hub.Send(ctx, Report{
			Lease:  "PA-0001.1",
			Call:   CallComplete,
			State:  contract.StateScored,
			Result: []byte(`{"state":"scored"}`),
		})

		hasMarker := bytes.Contains(body, []byte(marker))
		for _, callErr := range []error{lerr, herr, serr} {
			if callErr == nil {
				continue
			}
			if hasMarker && strings.Contains(callErr.Error(), marker) {
				t.Fatalf("HTTP client error leaked upstream body marker: %q", callErr.Error())
			}
			if errors.Is(callErr, ErrLeaseGone) {
				if code != http.StatusConflict {
					t.Fatalf("ErrLeaseGone returned on status %d, want 409", code)
				}
				continue
			}
			var pubErr *errs.Error
			if !errors.As(callErr, &pubErr) {
				t.Fatalf("HTTP client returned non-public error type %T: %v", callErr, callErr)
			}
			if pubErr.Code == "" || pubErr.Reason == "" || len(pubErr.Fingerprint) != 16 {
				t.Fatalf("malformed public error: %+v", pubErr)
			}
		}
	})
}
