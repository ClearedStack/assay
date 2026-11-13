// Package muster is the edge between Assay and Muster, the hub that owns
// every item's state.
//
// Muster is a separate component and is not built here. Assay sees it only
// through the Hub interface: lease one observed record from a lane, and
// later hand back the outcome. This package has two implementations: HTTP,
// for a real Muster, and Memory, an in-memory stand-in that follows SPEC.md
// so that Assay runs and is tested alone.
//
// The wire details (paths, field names, status codes) are provisional.
// SPEC.md does not define them; the proposal is written up there under
// "Proposed changes".
package muster

import (
	"context"
	"errors"
)

// Lane is one of the three queues Muster keeps for observed records.
type Lane string

const (
	Urgent   Lane = "urgent"
	Normal   Lane = "normal"
	Backfill Lane = "backfill"
)

// Lanes lists the lanes in the order Assay drains them.
var Lanes = []Lane{Urgent, Normal, Backfill}

// Lease is one observed record on loan to Assay.
type Lease struct {
	// ID names the lease when the outcome is handed back.
	ID string
	// Lane is the lane the record came from.
	Lane Lane
	// Record is the observed record, exactly as Muster sent it.
	Record []byte
}

// Hub is everything Assay asks of Muster.
//
// Both calls return an *errs.Error when they fail: E_TIMEOUT when the hub
// did not answer in time or is not serving, E_PARSE when the hub's reply or
// its view of our request was broken. Complete may also return ErrLeaseGone.
type Hub interface {
	// Lease asks for the next observed record in a lane. It returns nil and
	// no error when the lane is empty.
	Lease(ctx context.Context, lane Lane) (*Lease, error)
	// Complete hands back the outcome for a lease. It must be safe to call
	// more than once for the same lease.
	Complete(ctx context.Context, leaseID string, outcome []byte) error
}

// ErrLeaseGone means the hub no longer holds the lease, so the outcome has
// nowhere to go. The hub will lease the record again, and because scoring is
// deterministic the same outcome will be produced then.
var ErrLeaseGone = errors.New("lease is no longer held")
