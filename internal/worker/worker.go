// Package worker is Assay's polling loop: lease an observed record from
// Muster, evaluate it, hand the outcome back, and never lose a finished
// outcome on the way.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
	"github.com/ClearedStack/assay/internal/errs"
	"github.com/ClearedStack/assay/internal/muster"
	"github.com/ClearedStack/assay/internal/spool"
)

// Status says what one step of the loop did.
type Status int

const (
	// Worked: an item was evaluated and handed back. Look for more at once.
	Worked Status = iota
	// Idle: every lane was empty. Wait for the poll interval.
	Idle
	// Retry: the hub did not take or give something. Back off.
	Retry
)

// Worker holds what the loop needs. Spool may be nil, in which case an
// outcome the hub cannot take is dropped and the hub leases the record
// again later.
type Worker struct {
	Hub    muster.Hub
	Engine *engine.Engine
	Spool  *spool.Spool
	Log    *slog.Logger

	// HubTimeout bounds each call to the hub.
	HubTimeout time.Duration
	// PollInterval is the wait after finding every lane empty.
	PollInterval time.Duration
	// BackoffMin and BackoffMax bound the wait after a failed hub call. The
	// wait doubles with each failure in a row and is not randomised.
	BackoffMin time.Duration
	BackoffMax time.Duration
}

// Run loops until ctx is cancelled. It finishes the item in hand first.
func (w *Worker) Run(ctx context.Context) {
	backoff := w.BackoffMin
	for ctx.Err() == nil {
		switch w.Step(ctx) {
		case Worked:
			backoff = w.BackoffMin
		case Idle:
			backoff = w.BackoffMin
			sleep(ctx, w.PollInterval)
		case Retry:
			sleep(ctx, backoff)
			backoff *= 2
			if backoff > w.BackoffMax {
				backoff = w.BackoffMax
			}
		}
	}
}

// Step does one unit of work.
//
// Outcomes waiting in the spool go first: nothing new is leased until the
// hub has taken them. That keeps the spool to a single entry in normal
// running, and means a hub that is down is never asked for more work.
// Then the lanes are tried in order, urgent before normal before backfill,
// and the first record found is evaluated. After each item the order starts
// again from urgent.
func (w *Worker) Step(ctx context.Context) Status {
	if !w.flushSpool(ctx) {
		return Retry
	}
	for _, lane := range muster.Lanes {
		lease, err := w.lease(ctx, lane)
		if err != nil {
			if ctx.Err() == nil {
				w.logHubError("lease_failed", err)
			}
			return Retry
		}
		if lease == nil {
			continue
		}
		return w.process(ctx, lease)
	}
	return Idle
}

func (w *Worker) lease(ctx context.Context, lane muster.Lane) (*muster.Lease, error) {
	cctx, cancel := context.WithTimeout(ctx, w.HubTimeout)
	defer cancel()
	return w.Hub.Lease(cctx, lane)
}

func (w *Worker) complete(ctx context.Context, leaseID string, outcome []byte) error {
	// The outcome is finished work. Deliver it even if shutdown has begun.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.HubTimeout)
	defer cancel()
	return w.Hub.Complete(cctx, leaseID, outcome)
}

func (w *Worker) process(ctx context.Context, lease *muster.Lease) Status {
	out := w.Engine.Evaluate(lease.Record)
	payload := contract.Encode(out)

	attrs := []any{"lane", string(lease.Lane), "state", out.State}
	if out.ItemID != "" {
		attrs = append(attrs, "item_id", out.ItemID)
	}
	if out.Error != nil {
		attrs = append(attrs, "code", string(out.Error.Code), "reason", string(out.Error.Reason), "fingerprint", out.Error.Fingerprint)
	}
	w.Log.Info("item_evaluated", attrs...)

	err := w.complete(ctx, lease.ID, payload)
	if err == nil {
		return Worked
	}
	if w.deliveryIsHopeless(err) {
		return Worked
	}
	// The hub did not answer. Keep the finished outcome and try again.
	w.logHubError("complete_failed", err)
	if w.Spool == nil {
		w.Log.Warn("outcome_dropped")
		return Retry
	}
	if err := w.Spool.Put(lease.ID, payload); err != nil {
		w.Log.Error("spool_write_failed")
		return Retry
	}
	w.Log.Info("outcome_spooled")
	return Retry
}

// flushSpool hands every waiting outcome to the hub. It reports false if
// any is still waiting afterwards.
func (w *Worker) flushSpool(ctx context.Context) bool {
	if w.Spool == nil {
		return true
	}
	entries, err := w.Spool.List()
	if err != nil {
		w.Log.Error("spool_read_failed")
		return false
	}
	for _, entry := range entries {
		err := w.complete(ctx, entry.LeaseID, entry.Outcome)
		if err != nil && !w.deliveryIsHopeless(err) {
			w.logHubError("complete_failed", err)
			return false
		}
		if err := w.Spool.Remove(entry); err != nil {
			w.Log.Error("spool_remove_failed")
			return false
		}
		if err == nil {
			w.Log.Info("spooled_outcome_delivered")
		}
	}
	return true
}

// deliveryIsHopeless reports whether a failed completion can never succeed
// by retrying: the lease is gone, or the hub says the outcome itself is
// wrong. Either way the outcome is let go. The hub still holds the record,
// and the same record always produces the same outcome.
func (w *Worker) deliveryIsHopeless(err error) bool {
	if errors.Is(err, muster.ErrLeaseGone) {
		w.Log.Info("lease_gone")
		return true
	}
	var pub *errs.Error
	if errors.As(err, &pub) && pub.Code == errs.Parse {
		w.logHubError("outcome_refused", err)
		return true
	}
	return false
}

// logHubError logs a failed hub call. Only an *errs.Error is ever printed;
// anything else might name an address, so it is reduced to E_TIMEOUT.
func (w *Worker) logHubError(event string, err error) {
	var pub *errs.Error
	if !errors.As(err, &pub) {
		pub = errs.New(errs.Timeout, errs.HubUnavailable, "hub")
	}
	w.Log.Warn(event, "code", string(pub.Code), "reason", string(pub.Reason), "fingerprint", pub.Fingerprint)
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
