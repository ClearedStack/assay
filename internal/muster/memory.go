package muster

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/ClearedStack/assay/internal/contract"
)

// Memory is an in-memory stand-in for Muster. It keeps the three lanes,
// loans records out under a lease, files each outcome under the state it
// names, and can replay paused items, as SPEC.md says Muster does once a new
// profile has been deployed.
//
// It is for tests and for running Assay alone. It keeps nothing on disk.
type Memory struct {
	mu        sync.Mutex
	seq       int
	lanes     map[Lane][]*memItem
	leased    map[string]*memItem
	paused    []*memItem
	completed []Completion

	leaseFaults    []error
	completeFaults []error
}

type memItem struct {
	lane   Lane
	record []byte
}

// Completion is one outcome the stand-in has accepted.
type Completion struct {
	LeaseID string
	Lane    Lane
	// State is the outbound state the outcome named. It is empty for an
	// E_PARSE failure, which names none.
	State string
	// Code is the public error code, if the outcome carried one.
	Code    string
	Outcome []byte
}

// NewMemory returns an empty stand-in.
func NewMemory() *Memory {
	return &Memory{lanes: map[Lane][]*memItem{}, leased: map[string]*memItem{}}
}

// Add puts one observed record at the back of a lane.
func (m *Memory) Add(lane Lane, record []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lanes[lane] = append(m.lanes[lane], &memItem{lane: lane, record: append([]byte(nil), record...)})
}

// Lease implements Hub.
func (m *Memory) Lease(ctx context.Context, lane Lane) (*Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, timeoutError()
	}
	if len(m.leaseFaults) > 0 {
		err := m.leaseFaults[0]
		m.leaseFaults = m.leaseFaults[1:]
		return nil, err
	}
	queue := m.lanes[lane]
	if len(queue) == 0 {
		return nil, nil
	}
	item := queue[0]
	m.lanes[lane] = queue[1:]
	m.seq++
	id := fmt.Sprintf("lease-%06d", m.seq)
	m.leased[id] = item
	return &Lease{ID: id, Lane: lane, Record: append([]byte(nil), item.record...)}, nil
}

// Complete implements Hub. An outcome in needs_subtool pauses the item; any
// other outcome finishes it.
func (m *Memory) Complete(ctx context.Context, leaseID string, outcome []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return timeoutError()
	}
	if len(m.completeFaults) > 0 {
		err := m.completeFaults[0]
		m.completeFaults = m.completeFaults[1:]
		return err
	}
	item, ok := m.leased[leaseID]
	if !ok {
		return ErrLeaseGone
	}
	var head struct {
		State string `json:"state"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(outcome, &head); err != nil {
		return replyInvalid()
	}
	delete(m.leased, leaseID)
	c := Completion{LeaseID: leaseID, Lane: item.lane, State: head.State, Outcome: append([]byte(nil), outcome...)}
	if head.Error != nil {
		c.Code = head.Error.Code
	}
	m.completed = append(m.completed, c)
	if head.State == contract.StateNeedsSubtool {
		m.paused = append(m.paused, item)
	}
	return nil
}

// Replay puts every paused item back at the end of its lane and returns how
// many there were. Muster does this once a new profile and its fixtures have
// been deployed.
func (m *Memory) Replay() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.paused)
	for _, item := range m.paused {
		m.lanes[item.lane] = append(m.lanes[item.lane], item)
	}
	m.paused = nil
	return n
}

// Completions returns every outcome accepted so far, in order.
func (m *Memory) Completions() []Completion {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Completion(nil), m.completed...)
}

// Waiting returns how many records are still queued in the lanes.
func (m *Memory) Waiting() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, q := range m.lanes {
		n += len(q)
	}
	return n
}

// Paused returns how many items are paused in needs_subtool.
func (m *Memory) Paused() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.paused)
}

// FailLease makes the next n Lease calls fail with err.
func (m *Memory) FailLease(n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < n; i++ {
		m.leaseFaults = append(m.leaseFaults, err)
	}
}

// FailComplete makes the next n Complete calls fail with err.
func (m *Memory) FailComplete(n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < n; i++ {
		m.completeFaults = append(m.completeFaults, err)
	}
}
