// Package muster is Trawl's side of contract v1 with Muster, the hub.
//
// It holds the small interface Trawl needs (Client), an HTTP client for
// the contract (HTTP), and a fake hub that follows the contract closely
// enough to test against (Fake). Muster itself lives in its own
// repository; nothing here is taken from its code.
//
// Contract v1 as Trawl uses it: every call is a POST of a JSON object that
// carries "contract": "v1", with the caller's Kubernetes ServiceAccount
// token as a bearer token. The hub refuses a body with a field the call
// does not define, so the request types below hold exactly the fields of
// each call and nothing else.
//
//	POST /v1/lease          {contract}
//	POST /v1/heartbeat      {contract, lease}
//	POST /v1/complete       {contract, lease, state, result}
//	POST /v1/fail           {contract, lease, code}
//	POST /v1/needs_subtool  {contract, lease}
//	POST /v1/items          {contract, source, title, address, result}
//
// The contract itself is contract/v1/CONTRACT.md in the Muster repository.
// A copy of its saved examples is under testdata and is replayed by the
// tests here.
package muster

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/ClearedStack/trawl/internal/errs"
)

// ContractVersion is the version of the contract spoken here.
const ContractVersion = "v1"

// Worker is Trawl's name in the naming registry, and the key under which
// the hub keeps the results Trawl reports.
const Worker = "trawl"

// States Trawl meets. Trawl holds an item in discovered and completes it
// to recorded or rejected.
const (
	StateDiscovered = "discovered"
	StateRecorded   = "recorded"
	StateRejected   = "rejected"
)

// KindItem is the kind of a leased step that concerns an item.
const KindItem = "item"

var itemID = regexp.MustCompile(`^PA-([0-9]{4}|[1-9][0-9]{4,8})$`)

// ValidItemID reports whether id has the form of an item identifier:
// PA- and four digits, or five to nine digits with no leading zero.
func ValidItemID(id string) bool { return itemID.MatchString(id) }

// Lease is one leased step, as the hub answers a lease call.
type Lease struct {
	Lease   string `json:"lease"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	State   string `json:"state"`
	Lane    string `json:"lane"`
	Attempt int    `json:"attempt"`
	Expires string `json:"expires"`
	// Found is where the item was found, as it was recorded through
	// /v1/items. It is nil for an item that was not recorded that way.
	Found *Found `json:"found,omitempty"`
	// Results holds the latest result from each caller, by caller. For
	// a discovered item that is whoever filed it, if it gave one.
	Results map[string]json.RawMessage `json:"results,omitempty"`
}

// Found is the source, title and address an item was recorded with.
type Found struct {
	Source  string `json:"source"`
	Title   string `json:"title"`
	Address string `json:"address"`
}

// Limits the hub puts on a filing, in bytes.
const (
	MaxTitle   = 200
	MaxAddress = 2000
)

// Sources the hub knows. A filing from any other source is refused.
const (
	SourceSAMGov       = "sam-gov"
	SourceGitHubIssues = "github-issues"
	SourceWebSweep     = "web-sweep"
	SourceSiteForm     = "site-form"
)

// Filing is a candidate to file with the hub. Address is the cleaned
// address; the hub compares addresses exactly as sent, so making
// equivalent addresses equal is Trawl's job. Title must not be empty.
//
// Trawl cannot file with the source site-form: the hub requires a
// visitor's proof of work with it, which only Conn has.
type Filing struct {
	Source  string
	Title   string
	Address string
	Result  any
}

// Filed is the hub's answer to a filing.
type Filed struct {
	// New is true when the hub created an item, and false when it
	// already knew the address.
	New bool
	// ID is the item's identifier, when the hub gives one.
	ID string
}

// Errors a Client returns. They say what happened and nothing about where.
var (
	// ErrUnavailable means the hub could not be reached, is busy or
	// restarting, or did not accept the caller's token. The call is safe
	// to repeat later.
	ErrUnavailable = errors.New("hub unavailable")
	// ErrConflict means the lease is not held or the move is not
	// allowed. Repeating the call cannot succeed.
	ErrConflict = errors.New("hub refused: lease not held or move not allowed")
	// ErrRefused means the hub refused the request for any other reason.
	// Repeating it unchanged cannot succeed.
	ErrRefused = errors.New("hub refused the request")
)

// Client is everything Trawl asks of the hub.
type Client interface {
	// Lease takes Trawl's next step. It returns nil and no error when
	// there is nothing to do. The hub chooses the order: urgent first,
	// then normal, then backfill.
	Lease(ctx context.Context) (*Lease, error)
	// Heartbeat keeps a lease alive.
	Heartbeat(ctx context.Context, lease string) error
	// Complete ends a lease by moving the item to state. result is a
	// JSON object, or nil for none.
	Complete(ctx context.Context, lease, state string, result any) error
	// Fail ends a lease with E_TIMEOUT or E_PARSE.
	Fail(ctx context.Context, lease string, code errs.Code) error
	// NeedsSubtool pauses an item no rule covers.
	NeedsSubtool(ctx context.Context, lease string) error
	// File hands the hub a candidate. The hub creates an item or says
	// the address is already known.
	File(ctx context.Context, f Filing) (Filed, error)
}
