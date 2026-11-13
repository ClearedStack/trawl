package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ClearedStack/trawl/internal/dedupe"
	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/golden"
	"github.com/ClearedStack/trawl/internal/muster"
	"github.com/ClearedStack/trawl/internal/spool"
	"github.com/ClearedStack/trawl/internal/tool"
)

var ctx = context.Background()

const observed = "2031-03-04T05:06:07Z"

// listTool is a made-up sub-tool for these tests. Its items are JSON
// objects with a list of addresses and optional switches.
type listTool struct {
	name   string
	onCall func()
}

type listPayload struct {
	URLs  []string `json:"urls"`
	Error string   `json:"error"`
	Rate  string   `json:"rate"`
}

func (l listTool) Name() string { return l.name }

func (l listTool) Extract(in tool.Input) (tool.Output, error) {
	if l.onCall != nil {
		l.onCall()
	}
	var p listPayload
	if err := json.Unmarshal(in.Body, &p); err != nil {
		return tool.Output{}, errs.New(errs.Parse, "bad_json", in.Body)
	}
	if p.Error != "" {
		return tool.Output{}, errs.New(errs.Code(p.Error), "made_up", in.Body)
	}
	out := tool.Output{}
	for _, u := range p.URLs {
		out.Candidates = append(out.Candidates, tool.Candidate{URL: u, Kind: "tool", Via: in.ItemID})
	}
	if p.Rate != "" {
		out.RateKey, out.RateLimit = p.Rate, 2
	}
	return out, nil
}

func set(t *testing.T, tools ...tool.Tool) *tool.Set {
	t.Helper()
	var entries []tool.Entry
	for _, tl := range tools {
		var r tool.Rule
		r.Tool = tl.Name()
		r.Match.Source = tl.Name()
		r.Match.Formats = []string{tl.Name() + ".v1"}
		r.Input = tool.Either
		entries = append(entries, tool.Entry{Rule: r, Tool: tl})
	}
	s, err := tool.NewSet(entries...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func item(id, payload string) muster.Item {
	return muster.Item{ItemID: id, Source: "list", Format: "list.v1", Payload: json.RawMessage(payload), ObservedAt: observed}
}

func urls(list ...string) string {
	data, _ := json.Marshal(listPayload{URLs: list})
	return string(data)
}

func newWorker(t *testing.T, hub muster.Client, dir string) *Worker {
	t.Helper()
	sp, err := spool.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Worker{
		Hub:            hub,
		Spool:          sp,
		Tools:          set(t, listTool{name: "list"}),
		Poll:           time.Millisecond,
		Backoff:        time.Millisecond,
		HeartbeatEvery: time.Hour,
	}
}

func drain(t *testing.T, w *Worker) {
	t.Helper()
	if err := w.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
}

func pending(t *testing.T, w *Worker) int {
	t.Helper()
	names, err := w.Spool.Pending()
	if err != nil {
		t.Fatal(err)
	}
	return len(names)
}

func states(m *muster.Memory) map[string]string {
	out := map[string]string{}
	for _, it := range m.Snapshot().Items {
		out[it.ItemID] = it.State
	}
	return out
}

const (
	wage  = "https://tools.agency-example.gov/wage"
	rates = "https://calc.depot-example.mil/rates"
	forms = "https://forms.agency-example.gov/library"
)

func TestLanesAreDrainedInOrder(t *testing.T) {
	hub := muster.NewMemory()
	hub.Enqueue(muster.Backfill, item("backfill-1", urls("https://example.org/backfill")))
	hub.Enqueue(muster.Normal, item("normal-1", urls("https://example.org/normal-1")))
	hub.Enqueue(muster.Urgent, item("urgent-1", urls("https://example.org/urgent-1")))
	hub.Enqueue(muster.Normal, item("normal-2", urls("https://example.org/normal-2")))
	hub.Enqueue(muster.Urgent, item("urgent-2", urls("https://example.org/urgent-2")))

	drain(t, newWorker(t, hub, t.TempDir()))

	var got []string
	for _, r := range hub.Snapshot().Records {
		got = append(got, r.ID+" "+strings.TrimPrefix(r.URL, "https://example.org/"))
	}
	want := "PA-0001 urgent-1,PA-0002 urgent-2,PA-0003 normal-1,PA-0004 normal-2,PA-0005 backfill"
	if strings.Join(got, ",") != want {
		t.Errorf("records = %v", got)
	}
}

func TestEachAddressIsRecordedOnce(t *testing.T) {
	hub := muster.NewMemory()
	hub.Enqueue(muster.Normal, item("item-1", urls(wage, rates, "HTTPS://Tools.Agency-Example.GOV/wage/?utm_source=news#top")))
	hub.Enqueue(muster.Normal, item("item-2", urls(wage+"/index.html", forms)))
	hub.Enqueue(muster.Normal, item("item-3", urls(rates+"?ref=home")))

	drain(t, newWorker(t, hub, t.TempDir()))

	snap := hub.Snapshot()
	if len(snap.Records) != 3 {
		t.Fatalf("records = %+v", snap.Records)
	}
	wantRecords := []string{"PA-0001 " + wage, "PA-0002 " + rates, "PA-0003 " + forms}
	for i, r := range snap.Records {
		if r.ID+" "+r.URL != wantRecords[i] || r.SHA256 != dedupe.Hash(r.URL) {
			t.Errorf("record %d = %+v", i, r)
		}
	}
	wantSightings := []string{"PA-0001 item-2", "PA-0002 item-3"}
	if len(snap.Sightings) != len(wantSightings) {
		t.Fatalf("sightings = %+v", snap.Sightings)
	}
	for i, s := range snap.Sightings {
		if s.ID+" "+s.ItemID != wantSightings[i] {
			t.Errorf("sighting %d = %+v", i, s)
		}
	}
	if strings.Join(snap.Sextant, ",") != "PA-0001,PA-0002,PA-0003" {
		t.Errorf("queued for the next stage: %v", snap.Sextant)
	}
}

func TestFailuresDoNotStopTheRest(t *testing.T) {
	hub := muster.NewMemory()
	hub.Enqueue(muster.Normal, muster.Item{ItemID: "unknown-source", Source: "made-up", Format: "made-up.v9", Payload: json.RawMessage(`{}`), ObservedAt: observed})
	hub.Enqueue(muster.Normal, item("broken", `{"urls": [`))
	hub.Enqueue(muster.Normal, item("unfamiliar", `{"error":"E_NEEDS_SUBTOOL"}`))
	hub.Enqueue(muster.Normal, item("no-document", ``))
	hub.Enqueue(muster.Normal, muster.Item{ItemID: "no-time", Source: "list", Format: "list.v1", Payload: json.RawMessage(urls(wage))})
	hub.Enqueue(muster.Normal, item("partly-broken", urls("ftp://files.agency-example.gov/a.zip", rates)))
	hub.Enqueue(muster.Normal, item("good", urls(wage)))

	w := newWorker(t, hub, t.TempDir())
	drain(t, w)

	got := states(hub)
	want := map[string]string{
		"unknown-source": muster.StateNeedsSubtool,
		"broken":         muster.StateFailed,
		"unfamiliar":     muster.StateNeedsSubtool,
		"no-document":    muster.StateFailed,
		"no-time":        muster.StateFailed,
		"partly-broken":  muster.StateCompleted,
		"good":           muster.StateCompleted,
	}
	for id, state := range want {
		if got[id] != state {
			t.Errorf("%s is %s, want %s", id, got[id], state)
		}
	}
	snap := hub.Snapshot()
	if len(snap.Records) != 2 {
		t.Errorf("records = %+v", snap.Records)
	}
	for _, it := range snap.Items {
		switch it.ItemID {
		case "unknown-source":
			if it.Code != errs.NeedsSubtool || it.Reason != "no_matching_rule" || len(it.Fingerprint) != 12 {
				t.Errorf("unknown-source log = %+v", it)
			}
		case "partly-broken":
			if len(it.Rejects) != 1 || it.Rejects[0].Code != errs.Parse || it.Rejects[0].Reason != "scheme_not_public" {
				t.Errorf("partly-broken rejects = %+v", it.Rejects)
			}
		}
	}
	if n := pending(t, w); n != 0 {
		t.Errorf("%d entries left in the spool", n)
	}

	// Once a rule exists, replay brings the paused items back. Here no
	// rule was added, so they pause again, with the same fingerprint.
	before := hub.Snapshot().Items[0].Fingerprint
	if n := hub.Replay(); n != 2 {
		t.Fatalf("Replay = %d", n)
	}
	drain(t, w)
	after := hub.Snapshot().Items[0]
	if after.State != muster.StateNeedsSubtool || after.Fingerprint != before {
		t.Errorf("replayed item = %+v", after)
	}
}

type fetcher func(address string) (Document, error)

func (f fetcher) Fetch(_ context.Context, address string) (Document, error) { return f(address) }

func TestFetchOutcomes(t *testing.T) {
	hub := muster.NewMemory()
	fetchItem := func(id, address string) muster.Item {
		return muster.Item{ItemID: id, Source: "list", Format: "list.v1", Fetch: address, ObservedAt: observed}
	}
	hub.Enqueue(muster.Normal, fetchItem("times-out", "https://slow.agency-example.gov/feed"))
	hub.Enqueue(muster.Normal, fetchItem("disallowed", "https://shy.agency-example.gov/feed"))
	hub.Enqueue(muster.Normal, fetchItem("leaky", "https://leaky.agency-example.gov/feed"))
	hub.Enqueue(muster.Normal, fetchItem("works", "https://feed.agency-example.gov/list"))

	calls := map[string]int{}
	w := newWorker(t, hub, t.TempDir())
	w.Fetcher = fetcher(func(address string) (Document, error) {
		calls[address]++
		switch {
		case strings.Contains(address, "slow") && calls[address] == 1:
			return Document{}, errs.New(errs.Timeout, "upstream", nil)
		case strings.Contains(address, "shy"):
			return Document{}, ErrRobotsDisallowed
		case strings.Contains(address, "leaky") && calls[address] == 1:
			return Document{}, errors.New("dial tcp 10.9.8.7:443: made-up failure at leaky.agency-example.gov")
		}
		return Document{URL: address, ContentType: "application/json", Body: []byte(urls(address + "/found"))}, nil
	})
	drain(t, w)

	snap := hub.Snapshot()
	for _, it := range snap.Items {
		switch it.ItemID {
		case "times-out", "leaky":
			// Failed with E_TIMEOUT, stayed in the queue, then worked.
			if it.State != muster.StateCompleted || it.Attempts != 2 {
				t.Errorf("%s = %+v", it.ItemID, it)
			}
		case "disallowed":
			if it.State != muster.StateCompleted || it.Disposition != muster.RobotsDisallowed {
				t.Errorf("disallowed = %+v", it)
			}
		case "works":
			if it.State != muster.StateCompleted || it.Attempts != 1 {
				t.Errorf("works = %+v", it)
			}
		}
	}
	if len(snap.Records) != 3 {
		t.Errorf("records = %+v", snap.Records)
	}
}

// TestOutsideErrorsAreNotRepeated checks that an error from outside, which
// may name a machine or an address, never reaches the spool or the hub.
func TestOutsideErrorsAreNotRepeated(t *testing.T) {
	hub := muster.NewMemory()
	hub.Enqueue(muster.Normal, muster.Item{ItemID: "leaky", Source: "list", Format: "list.v1", Fetch: "https://leaky.agency-example.gov/feed", ObservedAt: observed})
	w := newWorker(t, hub, t.TempDir())
	w.Fetcher = fetcher(func(string) (Document, error) {
		return Document{}, errors.New("dial tcp 10.9.8.7:443: token=made-up-credential")
	})
	lease, _ := hub.Lease(ctx, muster.Normal)
	staged, held := w.process(ctx, lease)
	if !held {
		t.Fatal("lease lost")
	}
	data, _ := json.Marshal(staged)
	for _, leak := range []string{"10.9.8.7", "made-up-credential", "leaky.agency-example.gov", "dial tcp"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("staged outcome repeats %q: %s", leak, data)
		}
	}
	if staged.Kind != KindFail || staged.Code != errs.Timeout || staged.Reason != "fetch_failed" {
		t.Errorf("staged = %+v", staged)
	}
}

func TestInputModeIsEnforced(t *testing.T) {
	hub := muster.NewMemory()
	inline := listTool{name: "inline-only"}
	var r tool.Rule
	r.Tool, r.Match.Source, r.Match.Formats, r.Input = "inline-only", "inline-only", []string{"x.v1"}, tool.Inline
	s, err := tool.NewSet(tool.Entry{Rule: r, Tool: inline})
	if err != nil {
		t.Fatal(err)
	}
	hub.Enqueue(muster.Normal, muster.Item{ItemID: "wants-fetch", Source: "inline-only", Format: "x.v1", Fetch: "https://feed.agency-example.gov/list", ObservedAt: observed})
	w := newWorker(t, hub, t.TempDir())
	w.Tools = s
	w.Fetcher = fetcher(func(string) (Document, error) {
		t.Error("an inline-only sub-tool must never cause a fetch")
		return Document{}, nil
	})
	drain(t, w)
	it := hub.Snapshot().Items[0]
	if it.State != muster.StateNeedsSubtool || it.Reason != "input_mode" {
		t.Errorf("item = %+v", it)
	}
}

func TestInlinePayloadSizeLimit(t *testing.T) {
	hub := muster.NewMemory()
	big := `{"urls":["https://example.org/` + strings.Repeat("a", MaxInline) + `"]}`
	hub.Enqueue(muster.Normal, item("too-big", big))
	drain(t, newWorker(t, hub, t.TempDir()))
	it := hub.Snapshot().Items[0]
	if it.State != muster.StateFailed || it.Code != errs.Parse || it.Reason != "too_large" {
		t.Errorf("item = %+v", it)
	}
}

// TestHubRestartMidBatch is the "hub unavailable" row of the specification:
// the hub goes away after an item was leased, the finished work waits in
// the spool across a restart of Trawl itself, and is flushed on recovery.
func TestHubRestartMidBatch(t *testing.T) {
	hub := muster.NewMemory()
	dir := t.TempDir()
	hub.Enqueue(muster.Normal, item("item-1", urls(wage)))
	hub.Enqueue(muster.Normal, item("item-2", urls(rates)))

	w := newWorker(t, hub, dir)
	w.Tools = set(t, listTool{name: "list", onCall: func() { hub.SetAvailable(false) }})

	worked, err := w.Once(ctx)
	if !worked || !errors.Is(err, errHubDown) {
		t.Fatalf("Once = %v, %v", worked, err)
	}
	if n := pending(t, w); n != 1 {
		t.Fatalf("%d entries staged, want 1", n)
	}
	if len(hub.Snapshot().Records) != 0 {
		t.Fatal("recorded while the hub was down")
	}
	// While the hub is away nothing is lost and nothing new is taken.
	if worked, err := w.Once(ctx); worked || !errors.Is(err, errHubDown) {
		t.Fatalf("Once while down = %v, %v", worked, err)
	}

	// Trawl restarts too: a new worker over the same directory.
	restarted := newWorker(t, hub, dir)
	if n := pending(t, restarted); n != 1 {
		t.Fatalf("%d entries after restart, want 1", n)
	}
	hub.SetAvailable(true)
	drain(t, restarted)

	snap := hub.Snapshot()
	if len(snap.Records) != 2 || snap.Records[0].URL != wage || snap.Records[1].URL != rates {
		t.Errorf("records = %+v", snap.Records)
	}
	if n := pending(t, restarted); n != 0 {
		t.Errorf("%d entries left in the spool", n)
	}
}

func TestSpoolIsFlushedInOrder(t *testing.T) {
	hub := muster.NewMemory()
	w := newWorker(t, hub, t.TempDir())
	for i := 1; i <= 4; i++ {
		hub.Enqueue(muster.Normal, item(fmt.Sprintf("item-%d", i), urls(fmt.Sprintf("https://example.org/%d", i))))
	}
	// Lease and process all four, staging each without flushing.
	for i := 0; i < 4; i++ {
		lease, err := hub.Lease(ctx, muster.Normal)
		if err != nil || lease == nil {
			t.Fatal("lease failed")
		}
		staged, _ := w.process(ctx, lease)
		if _, err := w.Spool.Append(staged); err != nil {
			t.Fatal(err)
		}
	}
	hub.SetAvailable(false)
	if err := w.Flush(ctx); !errors.Is(err, errHubDown) {
		t.Fatalf("Flush while down = %v", err)
	}
	if n := pending(t, w); n != 4 {
		t.Fatalf("%d entries staged, want 4", n)
	}
	hub.SetAvailable(true)
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for i, r := range hub.Snapshot().Records {
		if r.URL != fmt.Sprintf("https://example.org/%d", i+1) || r.ID != dedupe.FormatID(i+1) {
			t.Errorf("record %d = %+v", i, r)
		}
	}
}

func TestExpiredLeaseIsDropped(t *testing.T) {
	hub := muster.NewMemory()
	w := newWorker(t, hub, t.TempDir())
	hub.Enqueue(muster.Normal, item("item-1", urls(wage)))
	lease, _ := hub.Lease(ctx, muster.Normal)
	staged, _ := w.process(ctx, lease)
	if _, err := w.Spool.Append(staged); err != nil {
		t.Fatal(err)
	}
	hub.ExpireLeases()
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if n := pending(t, w); n != 0 {
		t.Errorf("stale entry was kept")
	}
	if len(hub.Snapshot().Records) != 0 {
		t.Error("an outcome for an expired lease was recorded")
	}
	// The item is still in the queue and is recorded on the next pass.
	drain(t, w)
	if snap := hub.Snapshot(); len(snap.Records) != 1 || snap.Items[0].State != muster.StateCompleted {
		t.Errorf("after retry: %+v", snap)
	}
}

// racing is a hub in which another writer records an address between
// Trawl's lookup and its completion.
type racing struct {
	*muster.Memory
	rival func()
	raced int
}

func (r *racing) Lookup(c context.Context, hashes []string) (muster.LookupResult, error) {
	res, err := r.Memory.Lookup(c, hashes)
	if r.raced == 0 && r.rival != nil {
		r.raced++
		r.rival()
	}
	return res, err
}

func rival(t *testing.T, hub *muster.Memory, address string) func() {
	return func() {
		hub.Enqueue(muster.Urgent, item("rival", "{}"))
		lease, _ := hub.Lease(ctx, muster.Urgent)
		known, _ := hub.Lookup(ctx, nil)
		rec := dedupe.Record{ID: dedupe.FormatID(known.LastSeq + 1), SHA256: dedupe.Hash(address), URL: address}
		if err := hub.Complete(ctx, muster.Completion{LeaseID: lease.LeaseID, ExpectLastSeq: known.LastSeq, Records: []dedupe.Record{rec}}); err != nil {
			t.Fatalf("rival completion: %v", err)
		}
	}
}

func TestConflictIsDecidedAgain(t *testing.T) {
	t.Run("another address took the identifier", func(t *testing.T) {
		mem := muster.NewMemory()
		hub := &racing{Memory: mem}
		hub.rival = rival(t, mem, forms)
		mem.Enqueue(muster.Normal, item("item-1", urls(wage)))
		w := newWorker(t, hub, t.TempDir())
		drain(t, w)
		recs := mem.Snapshot().Records
		if len(recs) != 2 || recs[0].URL != forms || recs[1].URL != wage || recs[1].ID != "PA-0002" {
			t.Errorf("records = %+v", recs)
		}
	})
	t.Run("the same address was recorded meanwhile", func(t *testing.T) {
		mem := muster.NewMemory()
		hub := &racing{Memory: mem}
		hub.rival = rival(t, mem, wage)
		mem.Enqueue(muster.Normal, item("item-1", urls(wage)))
		w := newWorker(t, hub, t.TempDir())
		drain(t, w)
		snap := mem.Snapshot()
		if len(snap.Records) != 1 {
			t.Errorf("records = %+v", snap.Records)
		}
		if len(snap.Sightings) != 1 || snap.Sightings[0].ID != "PA-0001" || snap.Sightings[0].ItemID != "item-1" {
			t.Errorf("sightings = %+v", snap.Sightings)
		}
	})
}

// alwaysConflict refuses every completion as stale.
type alwaysConflict struct{ *muster.Memory }

func (alwaysConflict) Complete(context.Context, muster.Completion) error { return muster.ErrConflict }

func TestEndlessConflictKeepsTheEntry(t *testing.T) {
	mem := muster.NewMemory()
	mem.Enqueue(muster.Normal, item("item-1", urls(wage)))
	w := newWorker(t, alwaysConflict{mem}, t.TempDir())
	worked, err := w.Once(ctx)
	if !worked || !errors.Is(err, errBusy) {
		t.Fatalf("Once = %v, %v", worked, err)
	}
	if n := pending(t, w); n != 1 {
		t.Errorf("%d entries staged, want 1", n)
	}
}

func TestRateLimit(t *testing.T) {
	hub := muster.NewMemory()
	for i := 1; i <= 3; i++ {
		payload := fmt.Sprintf(`{"urls":["https://example.org/%d"],"rate":"made-up/2031-03-04/aa"}`, i)
		hub.Enqueue(muster.Normal, item(fmt.Sprintf("item-%d", i), payload))
	}
	hub.Enqueue(muster.Normal, item("other-key", `{"urls":["https://example.org/9"],"rate":"made-up/2031-03-04/bb"}`))
	drain(t, newWorker(t, hub, t.TempDir()))

	snap := hub.Snapshot()
	if len(snap.Records) != 3 {
		t.Errorf("records = %+v", snap.Records)
	}
	for _, it := range snap.Items {
		want := ""
		if it.ItemID == "item-3" {
			want = muster.RateLimited
		}
		if it.State != muster.StateCompleted || it.Disposition != want {
			t.Errorf("%s = %+v", it.ItemID, it)
		}
	}
}

// beating counts heartbeats and can report the lease as gone.
type beating struct {
	*muster.Memory
	beats atomic.Int32
	gone  bool
}

func (b *beating) Heartbeat(c context.Context, id string) error {
	b.beats.Add(1)
	if b.gone {
		return muster.ErrGone
	}
	return b.Memory.Heartbeat(c, id)
}

func TestHeartbeatWhileWorking(t *testing.T) {
	mem := muster.NewMemory()
	hub := &beating{Memory: mem}
	mem.Enqueue(muster.Normal, item("slow", urls(wage)))
	w := newWorker(t, hub, t.TempDir())
	w.HeartbeatEvery = time.Millisecond
	w.Tools = set(t, listTool{name: "list", onCall: func() {
		deadline := time.Now().Add(5 * time.Second)
		for hub.beats.Load() < 3 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}})
	drain(t, w)
	if hub.beats.Load() < 3 {
		t.Errorf("only %d heartbeats", hub.beats.Load())
	}
	if len(mem.Snapshot().Records) != 1 {
		t.Error("item was not recorded")
	}
}

func TestLostLeaseIsNotStaged(t *testing.T) {
	mem := muster.NewMemory()
	hub := &beating{Memory: mem, gone: true}
	mem.Enqueue(muster.Normal, item("slow", urls(wage)))
	w := newWorker(t, hub, t.TempDir())
	w.HeartbeatEvery = time.Millisecond
	w.Tools = set(t, listTool{name: "list", onCall: func() {
		deadline := time.Now().Add(5 * time.Second)
		for hub.beats.Load() < 1 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(5 * time.Millisecond)
	}})
	worked, err := w.Once(ctx)
	if !worked || err != nil {
		t.Fatalf("Once = %v, %v", worked, err)
	}
	if n := pending(t, w); n != 0 {
		t.Errorf("an outcome for a lost lease was staged")
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	hub := muster.NewMemory()
	w := newWorker(t, hub, t.TempDir())
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(cctx, false) }()
	hub.Enqueue(muster.Normal, item("item-1", urls(wage)))
	deadline := time.Now().Add(5 * time.Second)
	for len(hub.Snapshot().Records) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if len(hub.Snapshot().Records) != 1 {
		t.Error("item was not recorded before the stop")
	}
}

// batch is a fixed mix of items used for the golden run.
func batch(hub *muster.Memory) {
	hub.Enqueue(muster.Backfill, item("backfill-1", urls(forms, wage+"/")))
	hub.Enqueue(muster.Normal, item("normal-1", urls(wage, rates, "ftp://files.agency-example.gov/a.zip")))
	hub.Enqueue(muster.Normal, item("normal-2", `{"urls": [`))
	hub.Enqueue(muster.Urgent, item("urgent-1", urls("HTTPS://CALC.Depot-Example.MIL:443/rates/index.html?utm_campaign=x")))
	hub.Enqueue(muster.Normal, muster.Item{ItemID: "normal-3", Source: "made-up", Format: "made-up.v9", Payload: json.RawMessage(`{}`), ObservedAt: observed})
}

// TestGoldenBatch runs a fixed batch and compares everything the hub ends
// up holding with a saved file. Running it twice must give the same bytes.
func TestGoldenBatch(t *testing.T) {
	run := func() []byte {
		hub := muster.NewMemory()
		batch(hub)
		drain(t, newWorker(t, hub, t.TempDir()))
		data, err := golden.Marshal(hub.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	first, second := run(), run()
	if string(first) != string(second) {
		t.Fatal("two runs over the same batch differ")
	}
	golden.Bytes(t, "testdata/batch.golden.json", first)
}
