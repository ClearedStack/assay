// Package muster is the edge between Assay and Muster, the hub that owns
// every item's state.
//
// Muster is a separate component and is not built here. Assay sees it only
// through the Hub interface, which follows Muster's contract v1: lease the
// next item, and later make the one call that ends the lease. This package
// has two implementations: HTTP, for a real Muster, and Memory, an
// in-memory stand-in so that Assay runs and is tested alone.
package muster

import (
	"context"
	"errors"

	"github.com/ClearedStack/assay/internal/errs"
)

// Lease is one item on loan to Assay.
type Lease struct {
	// ID names the lease in every later call.
	ID string
	// Body is the lease reply exactly as Muster sent it: lease, kind, id,
	// state, lane, attempt, expires and results.
	Body []byte
}

// Call is one of the three calls that end a lease.
type Call string

const (
	// CallComplete moves the item to a new state and stores a result.
	CallComplete Call = "complete"
	// CallFail reports E_TIMEOUT or E_PARSE. The item keeps its state.
	CallFail Call = "fail"
	// CallNeedsSubtool pauses the item until a build with a rule for it is
	// deployed.
	CallNeedsSubtool Call = "needs_subtool"
)

// Report is the call that ends a lease, with what it carries. It is plain
// data so that it can wait in the spool and be sent again unchanged.
type Report struct {
	Call  Call   `json:"call"`
	Lease string `json:"lease"`
	// State and Result belong to a complete call. Result is a JSON object.
	State  string `json:"state,omitempty"`
	Result []byte `json:"result,omitempty"`
	// Code belongs to a fail call: E_TIMEOUT or E_PARSE.
	Code errs.Code `json:"code,omitempty"`
}

// Complete builds the report that moves an item to state with a result.
func Complete(lease, state string, result []byte) Report {
	return Report{Call: CallComplete, Lease: lease, State: state, Result: result}
}

// Fail builds the report that an item could not be worked.
func Fail(lease string, code errs.Code) Report {
	return Report{Call: CallFail, Lease: lease, Code: code}
}

// NeedsSubtool builds the report that pauses an item for want of a rule.
func NeedsSubtool(lease string) Report {
	return Report{Call: CallNeedsSubtool, Lease: lease}
}

// Hub is everything Assay asks of Muster.
//
// Each call returns an *errs.Error when it fails. E_TIMEOUT means the hub
// did not answer or is not serving: wait and try again. E_PARSE means the
// hub's reply was broken, or the hub says our request was: trying again
// will not help. Heartbeat and Send may also return ErrLeaseGone.
type Hub interface {
	// Lease asks for the next item. Muster offers the urgent lane first,
	// then normal, then backfill. It returns nil and no error when there is
	// nothing to do.
	Lease(ctx context.Context) (*Lease, error)
	// Heartbeat extends a lease that is still being worked.
	Heartbeat(ctx context.Context, lease string) error
	// Send makes the call that ends a lease. Muster answers a repeated call
	// the same way and does nothing twice, so it is safe to send again.
	Send(ctx context.Context, r Report) error
}

// ErrLeaseGone means the hub no longer holds the lease, or refuses the move,
// so the report has nowhere to go. The hub offers the item again, and
// because scoring is deterministic the same outcome will be produced then.
var ErrLeaseGone = errors.New("lease is no longer held")

func timeoutError() *errs.Error { return errs.New(errs.Timeout, errs.HubTimeout, "hub") }
func unavailable() *errs.Error  { return errs.New(errs.Timeout, errs.HubUnavailable, "hub") }
func replyInvalid() *errs.Error { return errs.New(errs.Parse, errs.HubReplyInvalid, "hub") }
func rejected() *errs.Error     { return errs.New(errs.Parse, errs.HubRejectedRequest, "hub") }

// contractUnknown means the hub does not speak contract v1. Nothing will
// work until one side is redeployed, so the caller keeps its work and waits.
func contractUnknown() *errs.Error {
	return errs.New(errs.NeedsSubtool, errs.HubContractUnknown, "hub")
}
