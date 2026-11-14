// Package worker is Assay's polling loop: lease an item from Muster,
// evaluate it, report what was decided, and never lose a finished report on
// the way.
package worker

import (
	"context"
	"encoding/json"
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
	// Worked: an item was evaluated and reported. Look for more at once.
	Worked Status = iota
	// Idle: the hub had nothing for Assay. Wait for the poll interval.
	Idle
	// Retry: the hub did not take or give something. Back off.
	Retry
)

// Worker holds what the loop needs. Spool may be nil, in which case a
// report the hub cannot take is dropped and the hub offers the item again
// later.
type Worker struct {
	Hub    muster.Hub
	Engine *engine.Engine
	Spool  *spool.Spool
	Log    *slog.Logger

	// HubTimeout bounds each call to the hub.
	HubTimeout time.Duration
	// PollInterval is the wait after the hub had nothing to offer.
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
// Reports waiting in the spool go first: nothing new is leased until the
// hub has taken them. That keeps the spool to a single entry in normal
// running, and means a hub that is down is never asked for more work.
// Then one item is leased and evaluated. Muster chooses which: the urgent
// lane first, then normal, then backfill.
//
// Evaluating an item takes far less than a lease lasts, so the loop never
// needs to send a heartbeat.
func (w *Worker) Step(ctx context.Context) Status {
	if !w.flushSpool(ctx) {
		return Retry
	}
	lease, err := w.lease(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.logHubError("lease_failed", err)
		}
		return Retry
	}
	if lease == nil {
		return Idle
	}
	return w.process(ctx, lease)
}

func (w *Worker) lease(ctx context.Context) (*muster.Lease, error) {
	cctx, cancel := context.WithTimeout(ctx, w.HubTimeout)
	defer cancel()
	return w.Hub.Lease(cctx)
}

func (w *Worker) send(ctx context.Context, r muster.Report) error {
	// The report is finished work. Deliver it even if shutdown has begun.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.HubTimeout)
	defer cancel()
	return w.Hub.Send(cctx, r)
}

// reportFor chooses the call that ends the lease. A decided item is
// completed to its new state with contract.Result as the result. An
// unfamiliar one is paused. A broken one is failed with E_PARSE. Muster's
// fail and needs_subtool calls carry no detail, so the reason and the
// fingerprint of an error are in Assay's own log line and nowhere else.
func reportFor(lease string, out *contract.Outcome) muster.Report {
	switch {
	case out.Error == nil:
		return muster.Complete(lease, out.State, contract.Result(out))
	case out.Error.Code == errs.NeedsSubtool:
		return muster.NeedsSubtool(lease)
	}
	return muster.Fail(lease, errs.Parse)
}

func (w *Worker) process(ctx context.Context, lease *muster.Lease) Status {
	out := w.Engine.Evaluate(lease.Body)
	report := reportFor(lease.ID, out)

	attrs := []any{"call", string(report.Call), "state", out.State}
	if out.ItemID != "" {
		attrs = append(attrs, "id", out.ItemID)
	}
	if out.Error != nil {
		attrs = append(attrs, "code", string(out.Error.Code), "reason", string(out.Error.Reason), "fingerprint", out.Error.Fingerprint)
		if out.Error.Where != "" {
			attrs = append(attrs, "where", out.Error.Where)
		}
	}
	w.Log.Info("item_evaluated", attrs...)

	err := w.send(ctx, report)
	if err == nil {
		return Worked
	}
	if w.deliveryIsHopeless(err) {
		return Worked
	}
	// The hub did not answer. Keep the finished report and try again.
	w.logHubError("report_failed", err)
	if w.Spool == nil {
		w.Log.Warn("report_dropped")
		return Retry
	}
	payload, err := json.Marshal(report)
	if err == nil {
		err = w.Spool.Put(report.Lease, payload)
	}
	if err != nil {
		w.Log.Error("spool_write_failed")
		return Retry
	}
	w.Log.Info("report_spooled")
	return Retry
}

// flushSpool hands every waiting report to the hub. It reports false if
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
		var report muster.Report
		delivered := false
		if json.Unmarshal(entry.Payload, &report) != nil || report.Lease != entry.Lease {
			// Not a report this build wrote. It can never be delivered.
			w.Log.Warn("spool_entry_unreadable")
		} else if err := w.send(ctx, report); err == nil {
			delivered = true
		} else if !w.deliveryIsHopeless(err) {
			w.logHubError("report_failed", err)
			return false
		}
		if err := w.Spool.Remove(entry); err != nil {
			w.Log.Error("spool_remove_failed")
			return false
		}
		if delivered {
			w.Log.Info("spooled_report_delivered")
		}
	}
	return true
}

// deliveryIsHopeless reports whether a failed report can never succeed by
// trying again: the lease is gone, or the hub says the report itself is
// wrong. Either way the report is let go. The hub still holds the item,
// and the same item always produces the same outcome.
func (w *Worker) deliveryIsHopeless(err error) bool {
	if errors.Is(err, muster.ErrLeaseGone) {
		w.Log.Info("lease_gone")
		return true
	}
	var pub *errs.Error
	if errors.As(err, &pub) && pub.Code == errs.Parse {
		w.logHubError("report_refused", err)
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
