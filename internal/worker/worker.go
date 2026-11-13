// Package worker is the loop that ties Trawl together: lease an item from
// the hub, find its sub-tool, extract and normalize candidates, stage the
// result on local disk, then deduplicate, mint identifiers and acknowledge.
package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ClearedStack/trawl/internal/dedupe"
	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/intake"
	"github.com/ClearedStack/trawl/internal/muster"
	"github.com/ClearedStack/trawl/internal/spool"
	"github.com/ClearedStack/trawl/internal/tool"
)

// Document is something read from a public address.
type Document struct {
	// URL is the canonical address the document was finally read from.
	URL         string
	ContentType string
	Body        []byte
}

// ErrRobotsDisallowed is returned by a Fetcher when the site's robots.txt
// does not allow the address to be read.
var ErrRobotsDisallowed = errors.New("robots.txt does not allow this address")

// Fetcher reads public addresses. Apart from ErrRobotsDisallowed, the
// errors it returns are public errors.
type Fetcher interface {
	Fetch(ctx context.Context, address string) (Document, error)
}

// Kinds of staged outcome, one per verb of the lease contract.
const (
	KindComplete     = "complete"
	KindFail         = "fail"
	KindNeedsSubtool = "needs_subtool"
)

// Staged is the outcome of one item as it is written to the spool. It
// holds everything needed to finish the item with the hub later, in this
// process or the next.
type Staged struct {
	Kind        string        `json:"kind"`
	LeaseID     string        `json:"lease_id"`
	ItemID      string        `json:"item_id"`
	Tool        string        `json:"tool,omitempty"`
	Result      intake.Result `json:"result"`
	Disposition string        `json:"disposition,omitempty"`
	Code        errs.Code     `json:"code,omitempty"`
	Reason      string        `json:"reason,omitempty"`
	Fingerprint string        `json:"fingerprint,omitempty"`
}

// Limits and timings. They are fixed here rather than read from outside so
// that every Trawl behaves the same.
const (
	// MaxInline is the largest inline payload accepted, in bytes.
	MaxInline = 256 << 10
	// conflictRetries is how often one completion is re-decided when
	// another writer moved the hub's state in between.
	conflictRetries = 5
	defaultPoll     = 5 * time.Second
	defaultBackoff  = 5 * time.Second
	defaultCall     = 10 * time.Second
	defaultBeat     = 20 * time.Second
)

var (
	errHubDown = errors.New("hub unavailable, staged work is waiting")
	errBusy    = errors.New("hub state keeps changing, staged work is waiting")
	errSpool   = errors.New("the spool could not be written")
)

// Worker processes items. Hub, Spool and Tools are required.
type Worker struct {
	Hub     muster.Client
	Spool   *spool.Spool
	Tools   *tool.Set
	Fetcher Fetcher
	Log     *slog.Logger

	// Poll is the wait when every lane is empty, Backoff the wait when
	// the hub cannot be reached, CallTimeout the limit on one hub call,
	// and HeartbeatEvery the heartbeat interval when a lease names no
	// lifetime. Zero values mean the defaults.
	Poll           time.Duration
	Backoff        time.Duration
	CallTimeout    time.Duration
	HeartbeatEvery time.Duration
}

func (w *Worker) log() *slog.Logger {
	if w.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return w.Log
}

func pick(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

func (w *Worker) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, pick(w.CallTimeout, defaultCall))
}

// Run works until ctx is cancelled. With drain set it returns as soon as
// every lane and the spool are empty. An item in progress when ctx is
// cancelled is finished and staged before Run returns.
func (w *Worker) Run(ctx context.Context, drain bool) error {
	for ctx.Err() == nil {
		worked, err := w.Once(ctx)
		switch {
		case errors.Is(err, errSpool):
			return err
		case err != nil:
			w.log().Warn("waiting", "why", err.Error())
			sleep(ctx, pick(w.Backoff, defaultBackoff))
		case !worked:
			if drain {
				return nil
			}
			sleep(ctx, pick(w.Poll, defaultPoll))
		}
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Once flushes the spool, then leases and processes at most one item. It
// reports whether an item was processed.
func (w *Worker) Once(ctx context.Context) (bool, error) {
	// Work already begun is carried through even if a shutdown starts.
	ctx = context.WithoutCancel(ctx)

	if err := w.Flush(ctx); err != nil {
		return false, err
	}
	item, err := w.lease(ctx)
	if err != nil {
		return false, err
	}
	if item == nil {
		return false, nil
	}
	staged, held := w.process(ctx, item)
	if !held {
		// The lease was lost while working; the hub will hand the item
		// out again, so there is nothing to report.
		w.log().Info("lease_lost", "item", item.ItemID)
		return true, nil
	}
	if _, err := w.Spool.Append(staged); err != nil {
		// Not staged, so not acknowledged: the lease runs out and the
		// item is retried.
		w.log().Error("spool_write_failed", "item", item.ItemID)
		return true, errSpool
	}
	w.log().Info("staged", "item", staged.ItemID, "kind", staged.Kind, "tool", staged.Tool,
		"code", string(staged.Code), "reason", staged.Reason, "fp", staged.Fingerprint)
	return true, w.Flush(ctx)
}

// lease asks each lane in priority order and returns the first item.
func (w *Worker) lease(ctx context.Context) (*muster.Item, error) {
	for _, lane := range muster.Lanes {
		cctx, cancel := w.call(ctx)
		item, err := w.Hub.Lease(cctx, lane)
		cancel()
		if err != nil {
			return nil, errHubDown
		}
		if item != nil {
			return item, nil
		}
	}
	return nil, nil
}

// process turns a leased item into a staged outcome. While it runs, the
// lease is kept alive with heartbeats. It reports false when the hub says
// the lease no longer exists.
func (w *Worker) process(ctx context.Context, item *muster.Item) (Staged, bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	every := pick(w.HeartbeatEvery, defaultBeat)
	if item.TTLSeconds > 0 {
		every = time.Duration(item.TTLSeconds) * time.Second / 3
	}
	var gone atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cctx, ccancel := w.call(ctx)
				err := w.Hub.Heartbeat(cctx, item.LeaseID)
				ccancel()
				if errors.Is(err, muster.ErrGone) {
					gone.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	staged := w.extract(ctx, item)
	cancel()
	<-done
	return staged, !gone.Load()
}

func (w *Worker) extract(ctx context.Context, item *muster.Item) Staged {
	st := Staged{LeaseID: item.LeaseID, ItemID: item.ItemID}
	failed := func(e *errs.Error) Staged {
		st.Kind = KindFail
		if e.Code == errs.NeedsSubtool {
			st.Kind = KindNeedsSubtool
		}
		if e.Tool != "" {
			st.Tool = e.Tool
		}
		st.Code, st.Reason, st.Fingerprint = e.Code, e.Reason, e.Fingerprint
		return st
	}
	kind := []byte(item.Source + "\n" + item.Format)

	hasFetch := item.Fetch != ""
	hasPayload := len(item.Payload) > 0 && string(item.Payload) != "null"
	if hasFetch == hasPayload {
		return failed(errs.New(errs.Parse, "envelope", kind))
	}
	if _, err := time.Parse(time.RFC3339, item.ObservedAt); err != nil {
		return failed(errs.New(errs.Parse, "envelope_time", kind))
	}

	entry, err := w.Tools.Match(item.Source, item.Format)
	if err != nil {
		e, _ := errs.As(err)
		return failed(e)
	}
	st.Tool = entry.Rule.Tool
	mode := entry.Rule.Input
	if hasFetch && mode == tool.Inline || hasPayload && mode == tool.Fetch {
		return failed(errs.New(errs.NeedsSubtool, "input_mode", kind).WithTool(st.Tool))
	}

	in := tool.Input{ItemID: item.ItemID, Source: item.Source, Format: item.Format, ObservedAt: item.ObservedAt}
	if hasFetch {
		if w.Fetcher == nil {
			return failed(errs.New(errs.NeedsSubtool, "no_fetcher", kind).WithTool(st.Tool))
		}
		doc, err := w.Fetcher.Fetch(ctx, item.Fetch)
		if errors.Is(err, ErrRobotsDisallowed) {
			st.Kind = KindComplete
			st.Disposition = muster.RobotsDisallowed
			return st
		}
		if err != nil {
			if e, ok := errs.As(err); ok {
				return failed(e.WithTool(st.Tool))
			}
			// Never repeat an outside error: it may name an address.
			return failed(errs.New(errs.Timeout, "fetch_failed", kind).WithTool(st.Tool))
		}
		in.URL, in.ContentType, in.Body = doc.URL, doc.ContentType, doc.Body
	} else {
		if len(item.Payload) > MaxInline {
			return failed(errs.New(errs.Parse, "too_large", item.Payload).WithTool(st.Tool))
		}
		in.ContentType, in.Body = "application/json", item.Payload
	}

	res, err := intake.Process(entry.Tool, in)
	if err != nil {
		e, _ := errs.As(err)
		return failed(e)
	}
	st.Kind = KindComplete
	st.Result = res
	return st
}

// Flush sends staged outcomes to the hub, oldest first, and removes each
// one once the hub has it. It stops at the first outcome the hub cannot
// take yet, so the order is kept. A nil return means the spool is empty.
func (w *Worker) Flush(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	names, err := w.Spool.Pending()
	if err != nil {
		return errSpool
	}
	for _, name := range names {
		var st Staged
		if err := w.Spool.Read(name, &st); err != nil {
			w.log().Error("spool_entry_damaged", "code", string(errs.Parse), "fp", errs.Fingerprint([]byte(name)))
			if err := w.Spool.SetAside(name); err != nil {
				return errSpool
			}
			continue
		}

		err := w.send(ctx, st)
		switch {
		case err == nil:
			w.log().Info("acknowledged", "item", st.ItemID, "kind", st.Kind)
		case errors.Is(err, muster.ErrGone):
			// The lease ended while the outcome waited. The hub hands
			// the item out again and it is processed afresh.
			w.log().Info("lease_gone", "item", st.ItemID)
		case errors.Is(err, muster.ErrConflict):
			return errBusy
		case errors.Is(err, muster.ErrProtocol):
			w.log().Error("hub_refused", "item", st.ItemID, "kind", st.Kind)
			if err := w.Spool.SetAside(name); err != nil {
				return errSpool
			}
			continue
		default:
			return errHubDown
		}
		if err := w.Spool.Remove(name); err != nil {
			return errSpool
		}
	}
	return nil
}

func (w *Worker) send(ctx context.Context, st Staged) error {
	failure := muster.Failure{LeaseID: st.LeaseID, Code: st.Code, Tool: st.Tool, Reason: st.Reason, Fingerprint: st.Fingerprint}
	switch st.Kind {
	case KindFail:
		cctx, cancel := w.call(ctx)
		defer cancel()
		return w.Hub.Fail(cctx, failure)
	case KindNeedsSubtool:
		cctx, cancel := w.call(ctx)
		defer cancel()
		return w.Hub.NeedsSubtool(cctx, failure)
	case KindComplete:
		return w.complete(ctx, st)
	default:
		return muster.ErrProtocol
	}
}

// complete is where an address becomes a record. It reads what the hub
// already knows, mints identifiers for what is new, and reports both in one
// completion that the hub accepts only if nothing changed in between.
func (w *Worker) complete(ctx context.Context, st Staged) error {
	done := muster.Completion{LeaseID: st.LeaseID, Tool: st.Tool, Disposition: st.Disposition}
	res := st.Result

	if res.RateKey != "" {
		cctx, cancel := w.call(ctx)
		count, err := w.Hub.Rate(cctx, res.RateKey, st.ItemID)
		cancel()
		if err != nil {
			return err
		}
		if count > res.RateLimit {
			done.Disposition = muster.RateLimited
			cctx, cancel := w.call(ctx)
			defer cancel()
			return w.Hub.Complete(cctx, done)
		}
	}

	done.Proposals = res.Proposals
	done.Rejects = res.Rejects

	var hashes []string
	seen := map[string]bool{}
	for _, e := range res.Entries {
		if !seen[e.SHA256] {
			seen[e.SHA256] = true
			hashes = append(hashes, e.SHA256)
		}
	}

	for attempt := 0; attempt < conflictRetries; attempt++ {
		var known muster.LookupResult
		if len(hashes) > 0 {
			cctx, cancel := w.call(ctx)
			var err error
			known, err = w.Hub.Lookup(cctx, hashes)
			cancel()
			if err != nil {
				return err
			}
		}
		done.ExpectLastSeq = known.LastSeq
		done.Records, done.Sightings = dedupe.Decide(res.Entries, known.Known, known.LastSeq)

		cctx, cancel := w.call(ctx)
		err := w.Hub.Complete(cctx, done)
		cancel()
		if !errors.Is(err, muster.ErrConflict) {
			return err
		}
	}
	return muster.ErrConflict
}
