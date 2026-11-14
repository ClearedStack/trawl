// Package worker is the loop that ties Trawl together. It has two duties.
//
// Sweep: read each source named in the sources file, run its sub-tool,
// clean every address found and file it with the hub. The hub creates a
// discovered item for an address it has not seen, and says so when it has.
//
// Record: lease Trawl's next discovered item, check what was filed, and
// complete it to recorded or rejected.
//
// Everything bound for the hub is first staged in the spool, so nothing
// is lost while the hub is away.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/fetch"
	"github.com/ClearedStack/trawl/internal/intake"
	"github.com/ClearedStack/trawl/internal/muster"
	"github.com/ClearedStack/trawl/internal/spool"
	"github.com/ClearedStack/trawl/internal/tool"
)

// Document is something read from a public address.
type Document = fetch.Document

// ErrRobotsDisallowed is returned by a Fetcher when the site's robots.txt
// does not allow the address to be read.
var ErrRobotsDisallowed = fetch.ErrRobotsDisallowed

// Fetcher reads public addresses. Apart from ErrRobotsDisallowed, the
// errors it returns are public errors.
type Fetcher interface {
	Fetch(ctx context.Context, address string) (Document, error)
}

// Record is what Trawl tells the hub about a candidate. It is the result
// sent with a filing, and again when the item is completed to recorded,
// so the next worker finds the clean address under results.trawl.
type Record struct {
	Source  string `json:"source"`
	Address string `json:"address"`
	Title   string `json:"title,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Via     string `json:"via,omitempty"`
}

// Rejection is the result sent when an item is completed to rejected.
type Rejection struct {
	// Reason is a token from a fixed list, never text from outside.
	Reason      string    `json:"reason"`
	Code        errs.Code `json:"code,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	// Address is the cleaned address, and Item the identifier of the
	// item that holds it, when the reason is that another item does.
	Address string `json:"address,omitempty"`
	Item    string `json:"item,omitempty"`
}

// Reasons for a rejection that are not error reasons.
const (
	// Duplicate: the cleaned address was already known to the hub.
	Duplicate = "duplicate"
	// Superseded: cleaning changed the address, so the clean address was
	// filed as an item of its own and this one ends here.
	Superseded = "superseded"
)

// Kinds of staged entry.
const (
	KindFile         = "file"
	KindComplete     = "complete"
	KindFail         = "fail"
	KindNeedsSubtool = "needs_subtool"
)

// Staged is one thing bound for the hub, as it is written to the spool.
// It holds all that is needed to finish with the hub later, in this
// process or the next.
type Staged struct {
	Kind string `json:"kind"`

	// A batch of candidates to file (KindFile).
	Source     string             `json:"source,omitempty"`
	Candidates []intake.Candidate `json:"candidates,omitempty"`

	// The outcome of a leased item (the other kinds).
	Lease  string `json:"lease,omitempty"`
	ItemID string `json:"item,omitempty"`
	Tool   string `json:"tool,omitempty"`
	// State and Result are what the item is completed with.
	State  string          `json:"state,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	// Refile, when set, is a cleaned address that differs from the one
	// the item was recorded under. It is filed first, and what the hub
	// answers decides how the item is completed.
	Refile *Record `json:"refile,omitempty"`
	// Code, Reason and Fingerprint describe a failure. Only the code is
	// sent to the hub; the rest is for the log.
	Code        errs.Code `json:"code,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}

const (
	defaultPoll    = 5 * time.Second
	defaultBackoff = 5 * time.Second
	defaultCall    = 10 * time.Second
	defaultBeat    = 20 * time.Second
)

var (
	errHubDown = errors.New("hub unavailable, staged work is waiting")
	errSpool   = errors.New("the spool could not be written")
)

// Worker does Trawl's work. Hub, Spool and Tools are required.
type Worker struct {
	Hub     muster.Client
	Spool   *spool.Spool
	Tools   *tool.Set
	Fetcher Fetcher
	Log     *slog.Logger

	// Sources are read on every sweep. SweepEvery is the time between
	// sweeps; zero means sweep once, when Run starts.
	Sources    []Source
	SweepEvery time.Duration

	// Poll is the wait when there is nothing to lease, Backoff the wait
	// when the hub cannot be reached, CallTimeout the limit on one hub
	// call, and HeartbeatEvery the heartbeat interval. Zero values mean
	// the defaults.
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

// Run works until ctx is cancelled. With drain set it sweeps once and
// returns as soon as there is nothing to lease and the spool is empty.
// An item in progress when ctx is cancelled is finished and staged before
// Run returns.
func (w *Worker) Run(ctx context.Context, drain bool) error {
	swept := false
	var nextSweep time.Time
	for ctx.Err() == nil {
		due := !swept || w.SweepEvery > 0 && !time.Now().Before(nextSweep)
		if len(w.Sources) > 0 && due {
			if err := w.Sweep(ctx); err != nil {
				return err
			}
			swept = true
			nextSweep = time.Now().Add(w.SweepEvery)
		}

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

// Sweep reads every source once and stages what it finds. A source that
// fails is logged with its public error and tried again on the next
// sweep; the other sources go on.
func (w *Worker) Sweep(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	for _, src := range w.Sources {
		res, toolName, err := w.read(ctx, src)
		if errors.Is(err, ErrRobotsDisallowed) {
			w.log().Info("source_skipped", "source", src.Source, "why", "robots_disallowed")
			continue
		}
		if err != nil {
			e, _ := errs.As(err)
			w.log().Warn("source_failed", "source", src.Source, "tool", e.Tool, "code", string(e.Code), "reason", e.Reason, "fp", e.Fingerprint)
			continue
		}
		for _, r := range res.Rejects {
			w.log().Info("entry_refused", "source", src.Source, "tool", toolName, "code", string(r.Code), "reason", r.Reason, "fp", r.Fingerprint)
		}

		// One address is filed once per batch, in the order found.
		var batch []intake.Candidate
		seen := map[string]bool{}
		for _, c := range res.Candidates {
			if !seen[c.Address] {
				seen[c.Address] = true
				c.Changed = false
				batch = append(batch, c)
			}
		}
		w.log().Info("source_read", "source", src.Source, "tool", toolName, "candidates", len(batch), "refused", len(res.Rejects))
		if len(batch) == 0 {
			continue
		}
		if _, err := w.Spool.Append(Staged{Kind: KindFile, Source: src.Source, Tool: toolName, Candidates: batch}); err != nil {
			w.log().Error("spool_write_failed", "source", src.Source)
			return errSpool
		}
	}
	return nil
}

// read fetches one source and runs its sub-tool.
func (w *Worker) read(ctx context.Context, src Source) (intake.Result, string, error) {
	class := []byte(src.Source + "\n" + src.Format)
	entry, err := w.Tools.Match(src.Source, src.Format)
	if err != nil {
		return intake.Result{}, "", err
	}
	name := entry.Rule.Tool
	if entry.Rule.Input == tool.Inline || w.Fetcher == nil {
		return intake.Result{}, name, errs.New(errs.NeedsSubtool, "input_mode", class).WithTool(name)
	}
	doc, err := w.Fetcher.Fetch(ctx, src.Fetch)
	if errors.Is(err, ErrRobotsDisallowed) {
		return intake.Result{}, name, err
	}
	if err != nil {
		if e, ok := errs.As(err); ok {
			return intake.Result{}, name, e.WithTool(name)
		}
		// Never repeat an outside error: it may name an address.
		return intake.Result{}, name, errs.New(errs.Timeout, "fetch_failed", class).WithTool(name)
	}
	res, err := intake.Process(entry.Tool, tool.Input{
		Source:      src.Source,
		Format:      src.Format,
		URL:         doc.URL,
		ContentType: doc.ContentType,
		Body:        doc.Body,
	})
	return res, name, err
}

// Once flushes the spool, then leases and decides at most one item. It
// reports whether an item was leased.
func (w *Worker) Once(ctx context.Context) (bool, error) {
	// Work already begun is carried through even if a shutdown starts.
	ctx = context.WithoutCancel(ctx)

	if err := w.Flush(ctx); err != nil {
		return false, err
	}
	cctx, cancel := w.call(ctx)
	lease, err := w.Hub.Lease(cctx)
	cancel()
	if err != nil {
		return false, errHubDown
	}
	if lease == nil {
		return false, nil
	}
	staged, held := w.process(ctx, lease)
	if !held {
		// The lease was lost while working; the hub offers the item
		// again, so there is nothing to report.
		w.log().Info("lease_lost", "item", lease.ID)
		return true, nil
	}
	if _, err := w.Spool.Append(staged); err != nil {
		// Not staged, so not acknowledged: the lease runs out and the
		// item is offered again.
		w.log().Error("spool_write_failed", "item", lease.ID)
		return true, errSpool
	}
	w.log().Info("staged", "item", staged.ItemID, "kind", staged.Kind, "state", staged.State, "tool", staged.Tool,
		"code", string(staged.Code), "reason", staged.Reason, "fp", staged.Fingerprint)
	return true, w.Flush(ctx)
}

// process decides a leased item while keeping its lease alive with
// heartbeats. It reports false when the hub says the lease is gone.
func (w *Worker) process(ctx context.Context, lease *muster.Lease) (Staged, bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var gone atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(pick(w.HeartbeatEvery, defaultBeat))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cctx, ccancel := w.call(ctx)
				err := w.Hub.Heartbeat(cctx, lease.Lease)
				ccancel()
				if errors.Is(err, muster.ErrConflict) {
					gone.Store(true)
					return
				}
			}
		}
	}()

	staged := w.decide(lease)
	cancel()
	<-done
	return staged, !gone.Load()
}

// decide turns a leased item into its outcome. It is a pure function of
// the lease.
//
// Trawl holds items in discovered. The lease says where the item was
// found: its source, its title and its address, as they were recorded.
// The sub-tool whose rule names the source decides how it is read:
//
//   - A source Trawl sweeps (sam-gov, github-issues, web-sweep): the item
//     is one address found there. It passes the rules every candidate
//     passes.
//   - A source that arrives as a submission (site-form): the sub-tool
//     validates what was recorded, then the same rules apply.
//
// Then the address decides. Clean as recorded: the item is completed to
// recorded. Not public, or broken: rejected. Changed by cleaning: see
// complete. A source with no rule, or anything else, is a case no rule
// covers.
func (w *Worker) decide(lease *muster.Lease) Staged {
	st := Staged{Lease: lease.Lease, ItemID: lease.ID}
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
	unfamiliar := func(reason string) Staged {
		return failed(errs.New(errs.NeedsSubtool, reason, []byte(muster.Worker+"\n"+reason)))
	}
	completed := func(state string, result any) Staged {
		st.Kind, st.State = KindComplete, state
		st.Result, _ = json.Marshal(result)
		return st
	}

	if lease.Kind != muster.KindItem || lease.State != muster.StateDiscovered || !muster.ValidItemID(lease.ID) {
		return unfamiliar("unfamiliar_step")
	}
	found := lease.Found
	if found == nil || found.Source == "" || found.Address == "" {
		return unfamiliar("unfamiliar_item")
	}
	entry, err := w.Tools.Source(found.Source)
	if err != nil {
		e, _ := errs.As(err)
		return failed(e)
	}
	st.Tool = entry.Rule.Tool
	swept := entry.Rule.Input == tool.Fetch

	var res intake.Result
	if swept {
		// What Trawl said about the candidate when it filed it, if it
		// did: the kind of link and where it was seen.
		var prior struct {
			Kind string `json:"kind"`
			Via  string `json:"via"`
		}
		json.Unmarshal(lease.Results[muster.Worker], &prior)
		res, err = intake.Clean(st.Tool, tool.Output{Candidates: []tool.Candidate{
			{URL: found.Address, Title: found.Title, Kind: prior.Kind, Via: prior.Via},
		}})
	} else {
		doc, _ := json.Marshal(found)
		res, err = intake.Process(entry.Tool, tool.Input{
			Source:      found.Source,
			Format:      entry.Rule.Match.Formats[0],
			ContentType: "application/json",
			Body:        doc,
		})
	}
	if err != nil {
		e, _ := errs.As(err)
		return failed(e)
	}

	switch {
	case len(res.Candidates) == 1 && len(res.Rejects) == 0:
		c := res.Candidates[0]
		rec := Record{Source: found.Source, Address: c.Address, Title: c.Title, Kind: c.Kind, Via: c.Via}
		if c.Changed && swept {
			// The hub knows this item by the address as recorded. The
			// clean address is filed, and the hub's answer decides what
			// becomes of this item.
			st.Kind, st.Refile = KindComplete, &rec
			return st
		}
		// Either the address was recorded clean, so this item is the
		// one record of it, or it came through the form. Trawl cannot
		// file a form address again, because the hub wants the
		// visitor's proof of work with it, so the item is recorded
		// with its clean address in the result.
		return completed(muster.StateRecorded, rec)
	case len(res.Candidates) == 0 && len(res.Rejects) == 1:
		r := res.Rejects[0]
		st.Code, st.Reason, st.Fingerprint = r.Code, r.Reason, r.Fingerprint
		return completed(muster.StateRejected, Rejection{Reason: r.Reason, Code: r.Code, Fingerprint: r.Fingerprint})
	default:
		// A leased item stands for one address. Anything else has no
		// rule here.
		return failed(errs.New(errs.NeedsSubtool, "unfamiliar_outcome", []byte(st.Tool+"\nunfamiliar_outcome")).WithTool(st.Tool))
	}
}

// Flush sends staged entries to the hub, oldest first, and removes each
// one once the hub has it. It stops at the first entry the hub cannot
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
			w.log().Info("acknowledged", "kind", st.Kind, "item", st.ItemID, "source", st.Source)
		case errors.Is(err, muster.ErrConflict):
			// The lease ended while the outcome waited. The hub offers
			// the item again and it is decided afresh.
			w.log().Info("lease_gone", "item", st.ItemID)
		case errors.Is(err, muster.ErrRefused):
			w.log().Error("hub_refused", "kind", st.Kind, "item", st.ItemID, "why", refusalText(err))
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

// refusalText describes a refusal by its status, code and cause. The
// hub's own sentence is never logged.
func refusalText(err error) string {
	var r *muster.Refusal
	if errors.As(err, &r) {
		return r.Error()
	}
	return "refused"
}

func (w *Worker) send(ctx context.Context, st Staged) error {
	switch st.Kind {
	case KindFile:
		return w.file(ctx, st)
	case KindComplete:
		return w.complete(ctx, st)
	case KindFail:
		cctx, cancel := w.call(ctx)
		defer cancel()
		return w.Hub.Fail(cctx, st.Lease, st.Code)
	case KindNeedsSubtool:
		cctx, cancel := w.call(ctx)
		err := w.Hub.NeedsSubtool(cctx, st.Lease)
		cancel()
		if !errors.Is(err, muster.ErrConflict) {
			return err
		}
		// The hub does not let this item be paused, or the lease is
		// gone. Contract v1 has no move from discovered to
		// needs_subtool, so this is the usual path for Trawl. The item
		// is failed as E_PARSE instead, which holds it for the owner
		// to release; if the lease is gone this is refused too and the
		// entry is dropped.
		w.log().Warn("pause_refused", "item", st.ItemID, "reason", st.Reason, "fp", st.Fingerprint)
		cctx, cancel = w.call(ctx)
		defer cancel()
		return w.Hub.Fail(cctx, st.Lease, errs.Parse)
	default:
		return muster.ErrRefused
	}
}

// file hands the hub each candidate of a batch. Filing is safe to repeat:
// the hub answers "known" for an address it already has, so a batch cut
// short by an outage is simply sent again from the start.
func (w *Worker) file(ctx context.Context, st Staged) error {
	created, known := 0, 0
	for _, c := range st.Candidates {
		rec := Record{Source: st.Source, Address: c.Address, Title: c.Title, Kind: c.Kind, Via: c.Via}
		cctx, cancel := w.call(ctx)
		filed, err := w.Hub.File(cctx, filing(rec))
		cancel()
		switch {
		case err == nil && filed.New:
			created++
		case err == nil:
			known++
		case errors.Is(err, muster.ErrRefused), errors.Is(err, muster.ErrConflict):
			// The hub will not take this one candidate. The rest of
			// the batch still goes.
			w.log().Error("filing_refused", "source", st.Source, "why", refusalText(err), "fp", errs.Fingerprint([]byte(c.Address)))
		default:
			return err
		}
	}
	w.log().Info("filed", "source", st.Source, "new", created, "known", known)
	return nil
}

// filing builds the hub's filing for a record. The hub requires a title.
// A candidate that came with none is filed under its own address, cut to
// the longest title the hub accepts.
func filing(rec Record) muster.Filing {
	title := rec.Title
	if title == "" {
		title = rec.Address
		if utf8.RuneCountInString(title) > muster.MaxTitle {
			title = string([]rune(title)[:muster.MaxTitle])
		}
	}
	return muster.Filing{Source: rec.Source, Title: title, Address: rec.Address, Result: rec}
}

// complete ends a lease.
//
// When cleaning changed the address of a swept item, the clean address is
// filed first, because the hub compares addresses exactly as sent and
// making equivalent addresses equal is Trawl's job. If the clean address
// is new it becomes an item of its own, which Trawl will lease and record
// in its turn, and this item is rejected as superseded. If the hub already
// knows it, this item is rejected as a duplicate.
func (w *Worker) complete(ctx context.Context, st Staged) error {
	state, result := st.State, st.Result
	if st.Refile != nil {
		rec := *st.Refile
		cctx, cancel := w.call(ctx)
		filed, err := w.Hub.File(cctx, filing(rec))
		cancel()
		if err != nil {
			return err
		}
		switch {
		case filed.New:
			state = muster.StateRejected
			result, _ = json.Marshal(Rejection{Reason: Superseded, Address: rec.Address, Item: filed.ID})
		case filed.ID != "" && filed.ID == st.ItemID:
			// The hub holds this very item under the clean address.
			state = muster.StateRecorded
			result, _ = json.Marshal(rec)
		default:
			state = muster.StateRejected
			result, _ = json.Marshal(Rejection{Reason: Duplicate, Address: rec.Address, Item: filed.ID})
		}
	}
	var body any
	if len(result) > 0 {
		body = result
	}
	cctx, cancel := w.call(ctx)
	defer cancel()
	return w.Hub.Complete(cctx, st.Lease, state, body)
}
