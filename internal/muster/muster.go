// Package muster is Trawl's side of the queue lease contract with Muster,
// the hub. Muster is a separate component and does not exist yet, so this
// package holds three things: the small interface Trawl needs (Client), an
// in-memory stand-in that follows SPEC.md (Memory), and an HTTP client for
// a provisional wire format (HTTP).
//
// SPEC.md names the verbs lease, heartbeat, complete, fail and
// needs_subtool, and says deduplication checks state "via Muster" without
// naming a call. Lookup and Rate are the two read-and-count calls that were
// added to make that possible. They are proposals, listed in the README.
package muster

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ClearedStack/trawl/internal/dedupe"
	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/tool"
)

// ContractVersion is the version of the queue lease contract spoken here.
const ContractVersion = "v1"

// Worker is the name Trawl leases under.
const Worker = "trawl"

// Lane is a priority lane.
type Lane string

const (
	Urgent   Lane = "urgent"
	Normal   Lane = "normal"
	Backfill Lane = "backfill"
)

// Lanes lists the lanes in the order a worker must drain them.
var Lanes = []Lane{Urgent, Normal, Backfill}

// Item is one leased piece of work.
type Item struct {
	LeaseID string `json:"lease_id"`
	ItemID  string `json:"item_id"`
	Lane    Lane   `json:"lane"`
	// Source and Format say what kind of item this is. A sub-tool's
	// matching rule is checked against them.
	Source string `json:"source"`
	Format string `json:"format"`
	// Exactly one of Fetch and Payload is set: an address to read the
	// document from, or the document itself.
	Fetch   string          `json:"fetch,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	// ObservedAt is the hub's time for the item, in RFC 3339 form.
	ObservedAt string `json:"observed_at"`
	// TTLSeconds is how long the lease lasts without a heartbeat.
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

// LookupResult says which hashes are already recorded and what the newest
// sequence number is.
type LookupResult struct {
	Known   map[string]string `json:"known"`
	LastSeq int               `json:"last_seq"`
}

// Reject is a broken entry inside an otherwise good item.
type Reject struct {
	Code        errs.Code `json:"code"`
	Tool        string    `json:"tool,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Fingerprint string    `json:"fingerprint"`
}

// Dispositions explain a completion that recorded nothing on purpose.
const (
	RateLimited      = "rate_limited"
	RobotsDisallowed = "robots_disallowed"
)

// Completion reports a finished item. Records carry identifiers minted by
// Trawl on the understanding that the newest sequence number was
// ExpectLastSeq; the hub must refuse the whole completion if it was not.
type Completion struct {
	LeaseID       string            `json:"lease_id"`
	Tool          string            `json:"tool"`
	ExpectLastSeq int               `json:"expect_last_seq"`
	Records       []dedupe.Record   `json:"records,omitempty"`
	Sightings     []dedupe.Sighting `json:"sightings,omitempty"`
	Proposals     []tool.Proposal   `json:"proposals,omitempty"`
	Rejects       []Reject          `json:"rejects,omitempty"`
	Disposition   string            `json:"disposition,omitempty"`
}

// Failure reports an item that could not be finished.
type Failure struct {
	LeaseID     string    `json:"lease_id"`
	Code        errs.Code `json:"code"`
	Tool        string    `json:"tool,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Fingerprint string    `json:"fingerprint"`
}

// Errors a Client returns. They say what happened and nothing about where.
var (
	// ErrUnavailable means the hub could not be reached or is restarting.
	ErrUnavailable = errors.New("hub unavailable")
	// ErrConflict means the hub's state moved since it was read.
	ErrConflict = errors.New("hub state changed")
	// ErrGone means the lease no longer exists.
	ErrGone = errors.New("lease gone")
	// ErrProtocol means the hub refused the request as malformed.
	ErrProtocol = errors.New("hub refused the request")
)

// Client is everything Trawl asks of the hub.
type Client interface {
	// Lease takes the next item from one lane. It returns nil and no
	// error when the lane is empty.
	Lease(ctx context.Context, lane Lane) (*Item, error)
	// Heartbeat keeps a lease alive.
	Heartbeat(ctx context.Context, leaseID string) error
	// Lookup reads which of the hashes are recorded.
	Lookup(ctx context.Context, hashes []string) (LookupResult, error)
	// Rate adds member to the set named key and returns the size of the
	// set. Adding the same member twice counts once.
	Rate(ctx context.Context, key, member string) (int, error)
	// Complete reports a finished item.
	Complete(ctx context.Context, c Completion) error
	// Fail reports E_TIMEOUT or E_PARSE for an item.
	Fail(ctx context.Context, f Failure) error
	// NeedsSubtool pauses an item no rule covers.
	NeedsSubtool(ctx context.Context, f Failure) error
}
