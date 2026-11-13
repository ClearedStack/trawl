package muster

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/ClearedStack/trawl/internal/dedupe"
	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/tool"
)

// Item states kept by the stand-in.
const (
	StateQueued       = "queued"
	StateLeased       = "leased"
	StateCompleted    = "completed"
	StateFailed       = "failed"
	StateNeedsSubtool = "needs_subtool"
)

// Record states. A record is written in a single step that takes it from
// discovered to recorded, so recorded is the only state that is ever stored.
const StateRecorded = "recorded"

// SightingLog is a sighting as the stand-in stores it.
type SightingLog struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	ItemID string `json:"item_id"`
	Via    string `json:"via,omitempty"`
}

// ItemLog is the stand-in's view of one item.
type ItemLog struct {
	ItemID      string    `json:"item_id"`
	State       string    `json:"state"`
	Attempts    int       `json:"attempts"`
	Tool        string    `json:"tool,omitempty"`
	Code        errs.Code `json:"code,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Disposition string    `json:"disposition,omitempty"`
	Rejects     []Reject  `json:"rejects,omitempty"`
}

// Snapshot is everything the stand-in holds, in a stable order.
type Snapshot struct {
	Records   []dedupe.Record `json:"records"`
	Sightings []SightingLog   `json:"sightings"`
	Proposals []tool.Proposal `json:"proposals"`
	// Sextant lists the identifiers queued for the next stage, in order.
	Sextant []string  `json:"sextant"`
	Items   []ItemLog `json:"items"`
}

type entry struct {
	item    Item
	log     ItemLog
	leaseID string
}

// Memory is an in-memory stand-in for Muster. It follows SPEC.md: three
// priority lanes, leases, a needs_subtool pause with replay, retry after
// E_TIMEOUT, a halt after E_PARSE, and records that are written once.
// It is safe for use from several goroutines.
type Memory struct {
	mu        sync.Mutex
	available bool
	lanes     map[Lane][]*entry
	leased    map[string]*entry
	all       []*entry
	nextLease int

	records   []dedupe.Record
	byHash    map[string]string
	sightings []SightingLog
	sighted   map[string]bool
	proposals []tool.Proposal
	sextant   []string
	rates     map[string]map[string]bool
}

var _ Client = (*Memory)(nil)

// NewMemory returns an empty, available stand-in.
func NewMemory() *Memory {
	return &Memory{
		available: true,
		lanes:     map[Lane][]*entry{},
		leased:    map[string]*entry{},
		byHash:    map[string]string{},
		sighted:   map[string]bool{},
		rates:     map[string]map[string]bool{},
	}
}

// Enqueue adds an item to a lane. The lease fields of the item are ignored.
func (m *Memory) Enqueue(lane Lane, it Item) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it.Lane = lane
	it.LeaseID = ""
	e := &entry{item: it, log: ItemLog{ItemID: it.ItemID, State: StateQueued}}
	m.all = append(m.all, e)
	m.lanes[lane] = append(m.lanes[lane], e)
}

// SetAvailable turns the hub off or on. While it is off every call returns
// ErrUnavailable and nothing changes, as during a restart.
func (m *Memory) SetAvailable(up bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.available = up
}

// ExpireLeases ends every open lease and puts its item back at the front
// of its lane, as happens when heartbeats stop.
func (m *Memory) ExpireLeases() {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.leased {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := m.leased[id]
		delete(m.leased, id)
		e.leaseID = ""
		e.log.State = StateQueued
		m.lanes[e.item.Lane] = append([]*entry{e}, m.lanes[e.item.Lane]...)
	}
}

// Replay puts every item paused as needs_subtool back in its lane and
// returns how many there were.
func (m *Memory) Replay() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.all {
		if e.log.State == StateNeedsSubtool {
			e.log = ItemLog{ItemID: e.item.ItemID, State: StateQueued, Attempts: e.log.Attempts}
			m.lanes[e.item.Lane] = append(m.lanes[e.item.Lane], e)
			n++
		}
	}
	return n
}

// Snapshot copies the stand-in's state.
func (m *Memory) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Snapshot{
		Records:   append([]dedupe.Record{}, m.records...),
		Sightings: append([]SightingLog{}, m.sightings...),
		Proposals: append([]tool.Proposal{}, m.proposals...),
		Sextant:   append([]string{}, m.sextant...),
		Items:     []ItemLog{},
	}
	for _, e := range m.all {
		s.Items = append(s.Items, e.log)
	}
	return s
}

// Queued reports how many items wait in a lane.
func (m *Memory) Queued(lane Lane) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.lanes[lane])
}

func (m *Memory) Lease(_ context.Context, lane Lane) (*Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return nil, ErrUnavailable
	}
	valid := false
	for _, l := range Lanes {
		valid = valid || l == lane
	}
	if !valid {
		return nil, ErrProtocol
	}
	queue := m.lanes[lane]
	if len(queue) == 0 {
		return nil, nil
	}
	e := queue[0]
	m.lanes[lane] = queue[1:]
	m.nextLease++
	e.leaseID = fmt.Sprintf("lease-%d", m.nextLease)
	e.log.State = StateLeased
	e.log.Attempts++
	m.leased[e.leaseID] = e
	it := e.item
	it.LeaseID = e.leaseID
	return &it, nil
}

func (m *Memory) Heartbeat(_ context.Context, leaseID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return ErrUnavailable
	}
	if _, ok := m.leased[leaseID]; !ok {
		return ErrGone
	}
	return nil
}

func (m *Memory) Lookup(_ context.Context, hashes []string) (LookupResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return LookupResult{}, ErrUnavailable
	}
	res := LookupResult{Known: map[string]string{}, LastSeq: len(m.records)}
	for _, h := range hashes {
		if id, ok := m.byHash[h]; ok {
			res.Known[h] = id
		}
	}
	return res, nil
}

func (m *Memory) Rate(_ context.Context, key, member string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return 0, ErrUnavailable
	}
	if key == "" || member == "" {
		return 0, ErrProtocol
	}
	if m.rates[key] == nil {
		m.rates[key] = map[string]bool{}
	}
	m.rates[key][member] = true
	return len(m.rates[key]), nil
}

func (m *Memory) Complete(_ context.Context, c Completion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return ErrUnavailable
	}
	e, ok := m.leased[c.LeaseID]
	if !ok {
		return ErrGone
	}

	// Check everything before changing anything, so that a refused
	// completion leaves no trace.
	last := len(m.records)
	if len(c.Records) > 0 && c.ExpectLastSeq != last {
		return ErrConflict
	}
	inBatch := map[string]bool{}
	for i, r := range c.Records {
		seq, ok := dedupe.ParseID(r.ID)
		if !ok || seq != last+i+1 || dedupe.Hash(r.URL) != r.SHA256 || inBatch[r.SHA256] {
			return ErrProtocol
		}
		if _, known := m.byHash[r.SHA256]; known {
			return ErrConflict
		}
		inBatch[r.SHA256] = true
	}
	for _, s := range c.Sightings {
		if id, known := m.byHash[s.SHA256]; !known || id != s.ID {
			return ErrProtocol
		}
	}
	for _, r := range c.Rejects {
		if !r.Code.Valid() {
			return ErrProtocol
		}
	}
	switch c.Disposition {
	case "", RateLimited, RobotsDisallowed:
	default:
		return ErrProtocol
	}

	for _, r := range c.Records {
		m.records = append(m.records, r)
		m.byHash[r.SHA256] = r.ID
		m.sextant = append(m.sextant, r.ID)
	}
	for _, s := range c.Sightings {
		key := e.item.ItemID + "\n" + s.SHA256
		if m.sighted[key] {
			continue
		}
		m.sighted[key] = true
		m.sightings = append(m.sightings, SightingLog{ID: s.ID, SHA256: s.SHA256, ItemID: e.item.ItemID, Via: s.Via})
	}
	m.proposals = append(m.proposals, c.Proposals...)

	delete(m.leased, c.LeaseID)
	e.leaseID = ""
	e.log.State = StateCompleted
	e.log.Tool = c.Tool
	e.log.Disposition = c.Disposition
	e.log.Rejects = append([]Reject(nil), c.Rejects...)
	return nil
}

func (m *Memory) Fail(_ context.Context, f Failure) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return ErrUnavailable
	}
	e, ok := m.leased[f.LeaseID]
	if !ok {
		return ErrGone
	}
	switch f.Code {
	case errs.Timeout:
		// Back to the end of its lane for another try.
		e.log.State = StateQueued
		m.lanes[e.item.Lane] = append(m.lanes[e.item.Lane], e)
	case errs.Parse:
		e.log.State = StateFailed
	default:
		return ErrProtocol
	}
	delete(m.leased, f.LeaseID)
	e.leaseID = ""
	e.log.Tool, e.log.Code, e.log.Reason, e.log.Fingerprint = f.Tool, f.Code, f.Reason, f.Fingerprint
	return nil
}

func (m *Memory) NeedsSubtool(_ context.Context, f Failure) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.available {
		return ErrUnavailable
	}
	e, ok := m.leased[f.LeaseID]
	if !ok {
		return ErrGone
	}
	if f.Code != errs.NeedsSubtool {
		return ErrProtocol
	}
	delete(m.leased, f.LeaseID)
	e.leaseID = ""
	e.log.State = StateNeedsSubtool
	e.log.Tool, e.log.Code, e.log.Reason, e.log.Fingerprint = f.Tool, f.Code, f.Reason, f.Fingerprint
	return nil
}
