package muster

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/errs"
)

// laneOrder is the order Muster offers work in.
var laneOrder = []string{"urgent", "normal", "backfill"}

// Memory is an in-memory stand-in for Muster, as far as Assay can see it.
// It offers queued lease replies lane by lane, keeps track of which leases
// are held, files each closing call, and can replay paused items the way
// Muster does when a new build of a worker is deployed.
//
// It is for tests and for running Assay alone, and keeps nothing on disk.
// Lease replies are served byte for byte as they were queued, broken ones
// included, so one simplification follows: an item offered a second time
// carries the same lease identifier, where Muster would issue a new one.
type Memory struct {
	mu        sync.Mutex
	lanes     map[string][]*memItem
	held      map[string]*memItem
	closed    map[string]Report
	paused    []*memItem
	completed []Completion

	leaseFaults []error
	sendFaults  []error
}

type memItem struct {
	body  []byte
	lease string // empty when the body has no readable lease
	id    string
	lane  string
}

// Completion is one closing call the stand-in has accepted.
type Completion struct {
	Lease  string
	ItemID string
	Lane   string
	Call   Call
	// State is the state the item is in afterwards, where the call says:
	// the new state for complete, needs_subtool for a pause, and empty for
	// fail, which leaves the item in the state it had.
	State string
	// Code is the error code of a fail call.
	Code errs.Code
	// Result is the result of a complete call.
	Result []byte
}

// NewMemory returns an empty stand-in.
func NewMemory() *Memory {
	return &Memory{lanes: map[string][]*memItem{}, held: map[string]*memItem{}, closed: map[string]Report{}}
}

// Add queues one lease reply. It joins the lane the reply names, or the
// normal lane when the reply is too broken to say.
func (m *Memory) Add(body []byte) {
	item := &memItem{body: append([]byte(nil), body...), lane: "normal"}
	item.lease, _ = contract.LeaseID(body)
	var head struct {
		ID   string `json:"id"`
		Lane string `json:"lane"`
	}
	if json.Unmarshal(body, &head) == nil {
		item.id = head.ID
		for _, lane := range laneOrder {
			if head.Lane == lane {
				item.lane = lane
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lanes[item.lane] = append(m.lanes[item.lane], item)
}

// next takes the first item of the first lane that has one and marks its
// lease as held. The caller holds the lock.
func (m *Memory) next() *memItem {
	for _, lane := range laneOrder {
		queue := m.lanes[lane]
		if len(queue) == 0 {
			continue
		}
		item := queue[0]
		m.lanes[lane] = queue[1:]
		if item.lease != "" {
			m.held[item.lease] = item
			delete(m.closed, item.lease)
		}
		return item
	}
	return nil
}

// Lease implements Hub.
func (m *Memory) Lease(ctx context.Context) (*Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		return nil, timeoutError()
	}
	if err := takeFault(&m.leaseFaults); err != nil {
		return nil, err
	}
	item := m.next()
	if item == nil {
		return nil, nil
	}
	if item.lease == "" {
		// Exactly what the HTTP client makes of such a reply.
		return nil, replyInvalid()
	}
	return &Lease{ID: item.lease, Body: append([]byte(nil), item.body...)}, nil
}

// Heartbeat implements Hub.
func (m *Memory) Heartbeat(ctx context.Context, lease string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		return timeoutError()
	}
	if _, ok := m.held[lease]; !ok {
		return ErrLeaseGone
	}
	return nil
}

// Send implements Hub. Assay holds items in observed, so it may complete
// one to scored or screened_out, fail it, or pause it.
func (m *Memory) Send(ctx context.Context, r Report) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		return timeoutError()
	}
	if err := takeFault(&m.sendFaults); err != nil {
		return err
	}
	if before, ok := m.closed[r.Lease]; ok {
		// The call that ended a lease, made again, gets the same answer
		// and does nothing twice. Any other call on it is refused.
		if before.Call == r.Call && before.State == r.State && before.Code == r.Code && bytes.Equal(before.Result, r.Result) {
			return nil
		}
		return ErrLeaseGone
	}
	item, ok := m.held[r.Lease]
	if !ok {
		return ErrLeaseGone
	}

	c := Completion{Lease: r.Lease, ItemID: item.id, Lane: item.lane, Call: r.Call}
	switch r.Call {
	case CallComplete:
		if r.State != contract.StateScored && r.State != contract.StateScreenedOut {
			return errMoveNotAllowed
		}
		if len(r.Result) > 0 {
			var obj map[string]json.RawMessage
			if json.Unmarshal(r.Result, &obj) != nil {
				return rejected()
			}
		}
		c.State, c.Result = r.State, append([]byte(nil), r.Result...)
	case CallFail:
		switch r.Code {
		case errs.Timeout:
			// The step stays in the queue, in its place, to be tried again.
			m.lanes[item.lane] = append([]*memItem{item}, m.lanes[item.lane]...)
		case errs.Parse:
			// Work on the item stops. It is not offered again.
		default:
			return rejected()
		}
		c.Code = r.Code
	case CallNeedsSubtool:
		c.State = contract.StateNeedsSubtool
		m.paused = append(m.paused, item)
	default:
		return rejected()
	}
	delete(m.held, r.Lease)
	m.closed[r.Lease] = r
	m.completed = append(m.completed, c)
	return nil
}

// Replay puts every paused item into the urgent lane, in the order the
// items were paused, and returns how many there were. Muster does this when
// Shipkeeper reports a new image digest for the worker that paused them.
func (m *Memory) Replay() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.paused)
	for _, item := range m.paused {
		item.lane = "urgent"
		m.lanes["urgent"] = append(m.lanes["urgent"], item)
	}
	m.paused = nil
	return n
}

// Completions returns every closing call accepted so far, in order.
func (m *Memory) Completions() []Completion {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Completion(nil), m.completed...)
}

// Waiting returns how many items are still queued in the lanes.
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

// FailLease makes the next n lease calls fail with err.
func (m *Memory) FailLease(n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < n; i++ {
		m.leaseFaults = append(m.leaseFaults, err)
	}
}

// FailSend makes the next n closing calls fail with err.
func (m *Memory) FailSend(n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < n; i++ {
		m.sendFaults = append(m.sendFaults, err)
	}
}

func takeFault(faults *[]error) error {
	if len(*faults) == 0 {
		return nil
	}
	err := (*faults)[0]
	*faults = (*faults)[1:]
	return err
}
