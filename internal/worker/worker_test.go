package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/golden"
	"github.com/ClearedStack/trawl/internal/muster"
	"github.com/ClearedStack/trawl/internal/spool"
	"github.com/ClearedStack/trawl/internal/tool"
)

var ctx = context.Background()

const (
	trawlToken = "made-up-token-for-trawl"
	connToken  = "made-up-token-for-conn"
)

// listTool is a made-up sub-tool that reads a fetched JSON list of
// addresses, each with an optional title.
type listTool struct{ name string }

func (l listTool) Name() string { return l.name }

func (l listTool) Extract(in tool.Input) (tool.Output, error) {
	var p struct {
		URLs   []string          `json:"urls"`
		Titles map[string]string `json:"titles"`
		Error  string            `json:"error"`
	}
	if err := json.Unmarshal(in.Body, &p); err != nil {
		return tool.Output{}, errs.New(errs.Parse, "bad_json", in.Body)
	}
	if p.Error != "" {
		return tool.Output{}, errs.New(errs.Code(p.Error), "made_up", in.Body)
	}
	var out tool.Output
	for _, u := range p.URLs {
		out.Candidates = append(out.Candidates, tool.Candidate{URL: u, Kind: "tool", Title: p.Titles[u], Via: in.URL})
	}
	return out, nil
}

// formTool is a made-up sub-tool that reads one submission as the hub
// recorded it.
type formTool struct{}

func (formTool) Name() string { return "form" }

func (formTool) Extract(in tool.Input) (tool.Output, error) {
	var p muster.Found
	dec := json.NewDecoder(bytes.NewReader(in.Body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return tool.Output{}, errs.New(errs.Parse, "schema", in.Body)
	}
	if p.Title == "Broken on purpose" {
		return tool.Output{}, errs.New(errs.Parse, "made_up", in.Body)
	}
	out := tool.Output{Candidates: []tool.Candidate{{URL: p.Address, Kind: "submission", Title: p.Title, Via: "form"}}}
	if p.Title == "Two for one" {
		out.Candidates = append(out.Candidates, tool.Candidate{URL: p.Address + "/second"})
	}
	return out, nil
}

// set registers the made-up sub-tools under sources the hub knows.
func set(t *testing.T) *tool.Set {
	t.Helper()
	entry := func(tl tool.Tool, source, input string) tool.Entry {
		var r tool.Rule
		r.Tool = tl.Name()
		r.Match.Source = source
		r.Match.Formats = []string{tl.Name() + ".v1"}
		r.Input = input
		return tool.Entry{Rule: r, Tool: tl}
	}
	s, err := tool.NewSet(
		entry(listTool{"list"}, muster.SourceSAMGov, tool.Fetch),
		entry(listTool{"issues"}, muster.SourceGitHubIssues, tool.Fetch),
		entry(formTool{}, muster.SourceSiteForm, tool.Inline),
	)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// web is a made-up internet for the fetcher: documents by address.
type web struct {
	docs  map[string]string
	fail  map[string]error
	calls map[string]int
}

func (w *web) Fetch(_ context.Context, address string) (Document, error) {
	if w.calls == nil {
		w.calls = map[string]int{}
	}
	w.calls[address]++
	if err, ok := w.fail[address]; ok {
		return Document{}, err
	}
	body, ok := w.docs[address]
	if !ok {
		return Document{}, errs.New(errs.Parse, "not_found", []byte(address))
	}
	return Document{URL: address, ContentType: "application/json", Body: []byte(body)}, nil
}

func urls(list ...string) string {
	data, _ := json.Marshal(map[string][]string{"urls": list})
	return string(data)
}

type rig struct {
	hub    *muster.Fake
	base   string
	worker *Worker
	web    *web
	conn   *muster.HTTP
	logs   *bytes.Buffer
}

// newRig starts a fake hub over HTTP and a worker that talks to it.
func newRig(t *testing.T, dir string) *rig {
	t.Helper()
	fake := muster.NewFake(map[string]string{trawlToken: "trawl", connToken: "conn"})
	fake.SetPowBits(4)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	r := &rig{hub: fake, base: srv.URL, web: &web{docs: map[string]string{}, fail: map[string]error{}}, logs: &bytes.Buffer{}}
	r.worker = workerFor(t, srv.URL, dir, r)
	r.conn = hubClient(t, srv.URL, connToken)
	return r
}

func hubClient(t *testing.T, base, token string) *muster.HTTP {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := muster.NewHTTP(base, tokenFile, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func workerFor(t *testing.T, base, dir string, r *rig) *Worker {
	t.Helper()
	sp, err := spool.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Worker{
		Hub:            hubClient(t, base, trawlToken),
		Spool:          sp,
		Tools:          set(t),
		Fetcher:        r.web,
		Log:            slog.New(slog.NewJSONHandler(r.logs, nil)),
		Poll:           time.Millisecond,
		Backoff:        time.Millisecond,
		HeartbeatEvery: time.Hour,
	}
}

func (r *rig) source(address, body string) {
	r.web.docs[address] = body
	r.worker.Sources = append(r.worker.Sources, Source{Source: muster.SourceSAMGov, Format: "list.v1", Fetch: address})
}

// submit files a candidate from the site's form the way Conn does, with
// a visitor's proof of work, and returns the new item's identifier.
func (r *rig) submit(t *testing.T, title, address string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"contract": "v1", "source": muster.SourceSiteForm, "title": title, "address": address,
		"pow": r.hub.SolvePow(muster.SourceSiteForm, title, address), "visitor": "203.0.113.7",
	})
	req, _ := http.NewRequest(http.MethodPost, r.base+"/v1/items", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+connToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var answer struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&answer)
	if resp.StatusCode != http.StatusCreated || answer.ID == "" {
		t.Fatalf("conn filing of %q: status %d", address, resp.StatusCode)
	}
	return answer.ID
}

// relay files a candidate as Conn from a source other than the form,
// with the address as Conn was given it.
func (r *rig) relay(t *testing.T, title, address string) string {
	t.Helper()
	filed, err := r.conn.File(ctx, muster.Filing{Source: muster.SourceGitHubIssues, Title: title, Address: address})
	if err != nil || !filed.New {
		t.Fatalf("conn filing of %q: %+v, %v", address, filed, err)
	}
	return filed.ID
}

func drain(t *testing.T, w *Worker) {
	t.Helper()
	if err := w.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
}

// clean fails the test if anything is left in the spool, waiting or set
// aside. An entry set aside means the hub refused a request outright,
// which with a strict hub means a wrong field was sent.
func clean(t *testing.T, dir string) {
	t.Helper()
	names, _ := os.ReadDir(dir)
	for _, n := range names {
		t.Errorf("left in the spool: %s", n.Name())
	}
}

func byID(hub *muster.Fake) map[string]muster.FakeItem {
	out := map[string]muster.FakeItem{}
	for _, it := range hub.Items() {
		out[it.ID] = it
	}
	return out
}

func rejection(it muster.FakeItem) Rejection {
	var r Rejection
	json.Unmarshal(it.Results["trawl"], &r)
	return r
}

func record(it muster.FakeItem) Record {
	var r Record
	json.Unmarshal(it.Results["trawl"], &r)
	return r
}

const (
	wage  = "https://tools.agency-example.gov/wage"
	rates = "https://calc.depot-example.mil/rates"
	forms = "https://forms.agency-example.gov/library"
	feedA = "https://feed.agency-example.gov/a"
	feedB = "https://feed.agency-example.gov/b"
)

func TestSweepFilesAndRecords(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	r.source(feedA, urls("HTTPS://Tools.Agency-Example.GOV/wage/?utm_source=news#top", rates, wage+"/index.html", "ftp://files.agency-example.gov/a.zip"))
	r.source(feedB, urls(rates+"?ref=home", forms))

	drain(t, r.worker)
	clean(t, dir)

	items := r.hub.Items()
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	want := []string{"PA-0001 " + wage, "PA-0002 " + rates, "PA-0003 " + forms}
	for i, it := range items {
		if it.ID+" "+it.Address != want[i] || it.State != muster.StateRecorded || it.Next != "sextant" || it.Source != muster.SourceSAMGov {
			t.Errorf("item %d = %+v", i, it)
		}
		if rec := record(it); rec.Address != it.Address || rec.Source != muster.SourceSAMGov || rec.Kind != "tool" {
			t.Errorf("item %d results.trawl = %s", i, it.Results["trawl"])
		}
	}
	if rec := record(items[2]); rec.Via != feedB {
		t.Errorf("the third item should say where it was seen: %+v", rec)
	}
	// The hub wants a title. A candidate found without one is filed
	// under its own address.
	if items[0].Title != wage {
		t.Errorf("title of an untitled candidate = %q", items[0].Title)
	}

	// A second sweep finds nothing new and changes nothing.
	before, _ := golden.Marshal(r.hub.Items())
	drain(t, r.worker)
	after, _ := golden.Marshal(r.hub.Items())
	if string(before) != string(after) {
		t.Error("a second sweep changed what the hub holds")
	}
	filings := 0
	for _, call := range r.hub.Calls() {
		if call.Path == "/v1/items" {
			filings++
		}
		if call.Status >= 400 {
			t.Errorf("the hub refused %s %s with %d", call.Path, call.Body, call.Status)
		}
	}
	if filings != 8 {
		t.Errorf("%d filings, want 8: the four distinct clean addresses, once per sweep", filings)
	}
}

// TestNoHashIsSent checks that no call carries a hash of the address.
func TestNoHashIsSent(t *testing.T) {
	r := newRig(t, t.TempDir())
	r.source(feedA, urls(wage))
	drain(t, r.worker)
	for _, call := range r.hub.Calls() {
		if strings.Contains(call.Body, "sha256") || strings.Contains(call.Body, "4f224a35258b") {
			t.Errorf("%s carries a hash: %s", call.Path, call.Body)
		}
	}
}

func TestTitlesForFiling(t *testing.T) {
	long := "https://tools.agency-example.gov/" + strings.Repeat("a", 300)
	cases := []struct {
		rec  Record
		want string
	}{
		{Record{Address: wage, Title: "Wage calculator"}, "Wage calculator"},
		{Record{Address: wage}, wage},
		{Record{Address: long}, long[:muster.MaxTitle]},
		// The limit is in bytes. A title of 120 three-byte characters
		// is cut at the last whole character that fits.
		{Record{Address: wage, Title: strings.Repeat("\u20ac", 120)}, strings.Repeat("\u20ac", 66)},
		{Record{Address: wage, Title: strings.Repeat("a", 199) + " \u20ac"}, strings.Repeat("a", 199)},
	}
	for _, c := range cases {
		if got := filing(c.rec); got.Title != c.want || got.Address != c.rec.Address {
			t.Errorf("filing(%+v).Title = %q", c.rec, got.Title)
		}
	}
}

// TestLeasedItems covers what Trawl does with each kind of discovered
// item it can be handed.
func TestLeasedItems(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	r.source(feedA, urls(rates))

	// From the form on the site, filed by Conn.
	formClean := r.submit(t, "Wage calculator", wage)
	formUnclean := r.submit(t, "<b>Forms</b> library", "HTTPS://Forms.Agency-Example.gov/library/?utm_source=x")
	formPrivate := r.submit(t, "Made-up admin page", "http://localhost:8080/admin")
	formBroken := r.submit(t, "Broken on purpose", "https://example.org/broken")
	formTwo := r.submit(t, "Two for one", "https://example.org/two")
	// From another source, relayed by Conn with the address as given.
	relayNew := r.relay(t, "Made-up checklist", "https://Example.org/checklist/index.html")
	relayKnown := r.relay(t, "Rates again", rates+"/#top")
	relayIDN := r.relay(t, "Made-up catalogue", "https://b\u00fccher.example.com/katalog")
	// Put on record some other way: no source and address, or a source
	// no sub-tool reads.
	bare := r.hub.Seed(muster.FakeItem{State: muster.StateDiscovered, Next: "trawl"})
	stranger := r.hub.Seed(muster.FakeItem{State: muster.StateDiscovered, Next: "trawl", Source: "made-up-source", Title: "T", Address: "https://example.org/stranger"})

	drain(t, r.worker)
	clean(t, dir)
	items := byID(r.hub)

	// A form address that is already clean: the item is the record.
	if it := items[formClean]; it.State != muster.StateRecorded || record(it) != (Record{Source: muster.SourceSiteForm, Address: wage, Title: "Wage calculator", Kind: "submission", Via: "form"}) {
		t.Errorf("clean form item = %+v", it)
	}
	// A form address that cleaning changes: Trawl cannot file a form
	// address again, so the item is recorded with the clean address in
	// its result and the address as typed left in what was found.
	if it := items[formUnclean]; it.State != muster.StateRecorded || record(it).Address != forms || record(it).Title != "Forms library" || it.Address == forms {
		t.Errorf("unclean form item = %+v", it)
	}
	// Not public: rejected with the public error and no address.
	if it, rej := items[formPrivate], rejection(items[formPrivate]); it.State != muster.StateRejected || rej.Code != errs.Parse || rej.Reason != "host_not_public" || len(rej.Fingerprint) != 12 || rej.Address != "" {
		t.Errorf("private form item = %+v", it)
	}
	// Broken: failed with E_PARSE, so the hub holds it.
	if it := items[formBroken]; it.State != muster.StateDiscovered || !it.Held {
		t.Errorf("broken form item = %+v", it)
	}

	// Relayed with an address cleaning changes, and the clean address is
	// new: it becomes an item of its own, recorded, and this one ends.
	rej := rejection(items[relayNew])
	if items[relayNew].State != muster.StateRejected || rej.Reason != Superseded || rej.Address != "https://example.org/checklist" {
		t.Errorf("relayed new item = %+v", items[relayNew])
	}
	if it := items[rej.Item]; it.Address != "https://example.org/checklist" || it.State != muster.StateRecorded || it.Source != muster.SourceGitHubIssues || it.Title != "Made-up checklist" {
		t.Errorf("the clean address was not recorded: %+v", it)
	}
	// Relayed, and the clean address is already known.
	rej = rejection(items[relayKnown])
	if items[relayKnown].State != muster.StateRejected || rej.Reason != Duplicate || rej.Address != rates || items[rej.Item].Address != rates {
		t.Errorf("relayed known item = %+v", items[relayKnown])
	}

	// No rule covers these. Contract v1 does not let a discovered item
	// be paused, so each is failed with E_PARSE and held for the owner,
	// instead of being offered for ever.
	for name, id := range map[string]string{"two addresses": formTwo, "host outside ASCII": relayIDN, "nothing found": bare, "unknown source": stranger} {
		if it := items[id]; it.State != muster.StateDiscovered || !it.Held || it.Attempts != 1 {
			t.Errorf("%s = %+v", name, it)
		}
	}
	for _, reason := range []string{"unfamiliar_outcome", "idn_host", "unfamiliar_item", "no_matching_rule"} {
		if !strings.Contains(r.logs.String(), `"reason":"`+reason+`"`) {
			t.Errorf("the log does not give the reason %s", reason)
		}
	}

	// Ten were put on record above, one was swept, and one clean address
	// was filed for the relayed item. Nothing else was filed.
	if len(items) != 12 {
		t.Errorf("%d items, want 12", len(items))
	}
	for _, call := range r.hub.Calls() {
		if call.Caller == "trawl" && strings.Contains(call.Body, "203.0.113.7") {
			t.Error("a visitor's address passed through Trawl")
		}
		if call.Caller == "trawl" && call.Path == "/v1/items" && strings.Contains(call.Body, `"source":"site-form"`) {
			t.Error("Trawl tried to file as the site's form")
		}
	}
}

func TestStepsTrawlHasNoRuleFor(t *testing.T) {
	w := &Worker{Tools: set(t)}
	found := &muster.Found{Source: muster.SourceSAMGov, Title: "T", Address: wage}
	cases := map[string]muster.Lease{
		"a proposal":           {Lease: "EP-0001.1", Kind: "proposal", ID: "EP-0001", State: "pending_approval"},
		"an incident":          {Lease: "incident:PA-0001.1", Kind: "incident", ID: "PA-0001", State: "recorded"},
		"an item in recorded":  {Lease: "PA-0001.1", Kind: "item", ID: "PA-0001", State: "recorded", Found: found},
		"an odd identifier":    {Lease: "PA-1.1", Kind: "item", ID: "PA-1", State: "discovered", Found: found},
		"found with no source": {Lease: "PA-0001.1", Kind: "item", ID: "PA-0001", State: "discovered", Found: &muster.Found{Address: wage}},
	}
	for name, lease := range cases {
		st := w.decide(&lease)
		if st.Kind != KindNeedsSubtool || st.Code != errs.NeedsSubtool || st.Lease != lease.Lease {
			t.Errorf("%s: %+v", name, st)
		}
	}
	// The same lease always gives the same outcome.
	lease := muster.Lease{Lease: "PA-0001.1", Kind: "item", ID: "PA-0001", State: "discovered", Found: found}
	first, _ := json.Marshal(w.decide(&lease))
	second, _ := json.Marshal(w.decide(&lease))
	if string(first) != string(second) || !strings.Contains(string(first), `"state":"recorded"`) {
		t.Errorf("decide = %s then %s", first, second)
	}
}

// noID is a hub that answers a filing without naming the item.
type noID struct{ muster.Client }

func (n noID) File(c context.Context, f muster.Filing) (muster.Filed, error) {
	filed, err := n.Client.File(c, f)
	filed.ID = ""
	return filed, err
}

// TestKnownAddressWhenTheHubNamesNoItem covers an answer to a filing that
// carries no identifier.
func TestKnownAddressWhenTheHubNamesNoItem(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	r.worker.Hub = noID{r.worker.Hub}
	first := r.relay(t, "Wage calculator", wage)
	second := r.relay(t, "Wage calculator again", wage+"/#top")
	drain(t, r.worker)
	clean(t, dir)
	items := byID(r.hub)
	if items[first].State != muster.StateRecorded {
		t.Errorf("first = %+v", items[first])
	}
	if rej := rejection(items[second]); items[second].State != muster.StateRejected || rej.Reason != Duplicate || rej.Address != wage || rej.Item != "" {
		t.Errorf("second = %+v", items[second])
	}
	if len(items) != 2 {
		t.Errorf("%d items, want 2", len(items))
	}
}

func TestSweepFailuresDoNotStopTheRest(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	slow := "https://slow.agency-example.gov/feed"
	r.web.fail[slow] = errs.New(errs.Timeout, "upstream", nil)
	r.worker.Sources = []Source{
		{Source: muster.SourceSAMGov, Format: "list.v1", Fetch: slow},
		{Source: muster.SourceSAMGov, Format: "list.v1", Fetch: "https://shy.agency-example.gov/feed"},
		{Source: muster.SourceSAMGov, Format: "list.v1", Fetch: "https://leaky.agency-example.gov/feed"},
		{Source: muster.SourceSAMGov, Format: "list.v1", Fetch: "https://broken.agency-example.gov/feed"},
		{Source: muster.SourceSAMGov, Format: "list.v1", Fetch: "https://odd.agency-example.gov/feed"},
		{Source: "made-up-source", Format: "made-up.v9", Fetch: "https://feed.agency-example.gov/unknown"},
		{Source: muster.SourceSAMGov, Format: "list.v2", Fetch: "https://feed.agency-example.gov/newer"},
		{Source: muster.SourceSiteForm, Format: "form.v1", Fetch: "https://feed.agency-example.gov/form"},
	}
	r.web.fail["https://shy.agency-example.gov/feed"] = ErrRobotsDisallowed
	r.web.fail["https://leaky.agency-example.gov/feed"] = errors.New("dial tcp 10.9.8.7:443: token=made-up-credential")
	r.web.docs["https://broken.agency-example.gov/feed"] = `{"urls": [`
	r.web.docs["https://odd.agency-example.gov/feed"] = `{"error":"E_NEEDS_SUBTOOL"}`
	r.source(feedA, urls(wage))

	drain(t, r.worker)
	clean(t, dir)
	if items := r.hub.Items(); len(items) != 1 || items[0].Address != wage || items[0].State != muster.StateRecorded {
		t.Errorf("items = %+v", items)
	}

	logs := r.logs.String()
	for _, want := range []string{
		`"code":"E_TIMEOUT","reason":"upstream"`,
		`"why":"robots_disallowed"`,
		`"code":"E_TIMEOUT","reason":"fetch_failed"`,
		`"code":"E_PARSE","reason":"bad_json"`,
		`"code":"E_NEEDS_SUBTOOL","reason":"made_up"`,
		`"code":"E_NEEDS_SUBTOOL","reason":"no_matching_rule"`,
		`"code":"E_NEEDS_SUBTOOL","reason":"input_mode"`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %s", want)
		}
	}
	if strings.Count(logs, `"reason":"no_matching_rule"`) != 2 {
		t.Error("an unknown source and an unknown format should each be reported")
	}
	// Nothing from outside may reach the log: no address and no detail
	// of an outside error.
	for _, leak := range []string{"10.9.8.7", "made-up-credential", "dial tcp", "agency-example.gov", "depot-example"} {
		if strings.Contains(logs, leak) {
			t.Errorf("the log repeats %q", leak)
		}
	}
	if r.web.calls["https://feed.agency-example.gov/form"] != 0 {
		t.Error("a sub-tool that reads submissions caused a fetch")
	}

	// The slow source recovers and is read on the next sweep.
	delete(r.web.fail, slow)
	r.web.docs[slow] = urls(rates)
	drain(t, r.worker)
	if items := r.hub.Items(); len(items) != 2 || items[1].Address != rates {
		t.Errorf("after recovery: %+v", items)
	}
}

// TestRefusedFilingDoesNotStopTheBatch covers a candidate the hub will not
// take: here an address longer than the hub allows.
func TestRefusedFilingDoesNotStopTheBatch(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	long := "https://tools.agency-example.gov/" + strings.Repeat("a", muster.MaxAddress-30)
	r.source(feedA, urls(wage, long, rates))
	drain(t, r.worker)
	clean(t, dir)
	if items := r.hub.Items(); len(items) != 2 || items[0].Address != wage || items[1].Address != rates {
		t.Errorf("items = %+v", items)
	}
	if !strings.Contains(r.logs.String(), `"msg":"filing_refused"`) || strings.Contains(r.logs.String(), "aaaa") {
		t.Errorf("the refusal should be logged without the address:\n%s", r.logs.String())
	}
}

// TestHubRestartMidBatch is the "hub unavailable" row of the
// specification: work found while the hub is away waits in the spool,
// across a restart of Trawl itself, and is sent in order on recovery.
func TestHubRestartMidBatch(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	r.source(feedA, urls("https://example.org/1", "https://example.org/2"))
	r.source(feedB, urls("https://example.org/3"))

	r.hub.SetDown(true)
	if err := r.worker.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if worked, err := r.worker.Once(ctx); worked || !errors.Is(err, errHubDown) {
		t.Fatalf("Once while down = %v, %v", worked, err)
	}
	if pending, _ := r.worker.Spool.Pending(); len(pending) != 2 {
		t.Fatalf("%d batches staged, want 2", len(pending))
	}
	if len(r.hub.Items()) != 0 {
		t.Fatal("the hub took a filing while down")
	}

	// Trawl restarts too: a new worker over the same directory, with no
	// sources this time, so everything it sends comes from the spool.
	restarted := &rig{hub: r.hub, web: r.web, logs: &bytes.Buffer{}}
	restarted.worker = workerFor(t, r.base, dir, restarted)
	r.hub.SetDown(false)
	drain(t, restarted.worker)
	clean(t, dir)

	for i, it := range r.hub.Items() {
		if it.Address != fmt.Sprintf("https://example.org/%d", i+1) || it.State != muster.StateRecorded {
			t.Errorf("item %d = %+v", i, it)
		}
	}
	if len(r.hub.Items()) != 3 {
		t.Errorf("%d items, want 3", len(r.hub.Items()))
	}
}

func TestOutcomeWaitsForTheHub(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	id := r.submit(t, "Wage calculator", wage)

	// Lease while the hub is up, then lose it before the outcome is sent.
	lease, err := r.worker.Hub.Lease(ctx)
	if err != nil || lease == nil {
		t.Fatal("lease failed")
	}
	staged, _ := r.worker.process(ctx, lease)
	if _, err := r.worker.Spool.Append(staged); err != nil {
		t.Fatal(err)
	}
	r.hub.SetDown(true)
	if err := r.worker.Flush(ctx); !errors.Is(err, errHubDown) {
		t.Fatalf("Flush while down = %v", err)
	}
	if byID(r.hub)[id].State != muster.StateDiscovered {
		t.Fatal("completed while the hub was down")
	}
	r.hub.SetDown(false)
	if err := r.worker.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	clean(t, dir)
	if byID(r.hub)[id].State != muster.StateRecorded {
		t.Error("the outcome was not sent on recovery")
	}
}

func TestRetakenLeaseIsDropped(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	id := r.submit(t, "Wage calculator", wage)
	lease, _ := r.worker.Hub.Lease(ctx)
	staged, _ := r.worker.process(ctx, lease)
	if _, err := r.worker.Spool.Append(staged); err != nil {
		t.Fatal(err)
	}
	// The lease runs out and another Trawl takes the step.
	r.hub.Advance(2 * time.Minute)
	other, _ := r.worker.Hub.Lease(ctx)
	if other == nil || other.ID != id {
		t.Fatal("the step was not offered again")
	}
	if err := r.worker.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	clean(t, dir)
	if it := byID(r.hub)[id]; it.State != muster.StateDiscovered || it.Held {
		t.Errorf("the stale outcome changed the item: %+v", it)
	}
}

// goneHub answers every heartbeat as if the lease had been lost.
type goneHub struct {
	muster.Client
	beats atomic.Int32
}

func (g *goneHub) Heartbeat(context.Context, string) error {
	g.beats.Add(1)
	return muster.ErrConflict
}

func TestHeartbeatsAndALostLease(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	r.submit(t, "Wage calculator", wage)

	// With a short interval the worker heartbeats; the fake accepts them.
	r.worker.HeartbeatEvery = time.Nanosecond
	lease, _ := r.worker.Hub.Lease(ctx)
	if _, held := r.worker.process(ctx, lease); !held {
		t.Error("a held lease was reported lost")
	}

	// When the hub says the lease is gone, the outcome is not staged.
	gone := &goneHub{Client: r.worker.Hub}
	r.worker.Hub = gone
	lost := &muster.Lease{Lease: lease.Lease, Kind: lease.Kind, ID: lease.ID, State: lease.State, Found: lease.Found}
	deadline := time.Now().Add(5 * time.Second)
	held := true
	for held && time.Now().Before(deadline) {
		_, held = r.worker.process(ctx, lost)
	}
	if held || gone.beats.Load() == 0 {
		t.Errorf("held = %v after %d heartbeats", held, gone.beats.Load())
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	r := newRig(t, t.TempDir())
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.worker.Run(cctx, false) }()
	id := r.submit(t, "Wage calculator", wage)
	deadline := time.Now().Add(5 * time.Second)
	for byID(r.hub)[id].State != muster.StateRecorded && time.Now().Before(deadline) {
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
	if byID(r.hub)[id].State != muster.StateRecorded {
		t.Error("the item was not recorded before the stop")
	}
}

func TestSweepRepeatsOnSchedule(t *testing.T) {
	r := newRig(t, t.TempDir())
	r.source(feedA, urls(wage))
	r.worker.SweepEvery = time.Millisecond
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.worker.Run(cctx, false) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		filings := 0
		for _, call := range r.hub.Calls() {
			if call.Path == "/v1/items" {
				filings++
			}
		}
		if filings >= 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if len(r.hub.Items()) != 1 {
		t.Errorf("repeated sweeps made %d items, want 1", len(r.hub.Items()))
	}
}

// TestGoldenBatch runs a fixed batch and compares everything the hub ends
// up holding with a saved file. Running it twice must give the same bytes.
func TestGoldenBatch(t *testing.T) {
	run := func() []byte {
		dir := t.TempDir()
		r := newRig(t, dir)
		r.source(feedA, urls(forms, wage+"/", "ftp://files.agency-example.gov/a.zip"))
		r.source(feedB, urls("HTTPS://CALC.Depot-Example.MIL:443/rates/index.html?utm_campaign=x", wage))
		r.submit(t, "Made-up tool", "https://example.org/tool")
		r.submit(t, "Wage calculator, typed with a fragment", wage+"#top")
		r.submit(t, "Made-up private page", "http://192.0.2.10/x")
		r.relay(t, "Made-up checklist", "https://Example.org/checklist/")
		r.relay(t, "Forms library again", forms+"/index.html")
		r.hub.Seed(muster.FakeItem{State: muster.StateDiscovered, Next: "trawl", Source: "made-up-source", Title: "T", Address: "https://example.org/stranger"})
		drain(t, r.worker)
		clean(t, dir)
		data, err := golden.Marshal(r.hub.Items())
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

func TestParseSources(t *testing.T) {
	good := `{"sources": [
  {"source": "web-sweep", "format": "web-sweep.page.v1", "fetch": "https://www.agency-example.gov/resources"},
  {"source": "sam-gov", "format": "sam-gov.opportunities.v2", "fetch": "https://feed.agency-example.gov/v2?limit=100"}
]}`
	got, err := ParseSources([]byte(good))
	if err != nil || len(got) != 2 || got[1].Source != "sam-gov" || got[0].Fetch != "https://www.agency-example.gov/resources" {
		t.Fatalf("ParseSources = %+v, %v", got, err)
	}
	if empty, err := ParseSources([]byte(`{"sources": []}`)); err != nil || len(empty) != 0 {
		t.Errorf("empty list: %+v, %v", empty, err)
	}
	bad := map[string]string{
		"not JSON":            `sources:`,
		"no list":             `{}`,
		"unknown key":         `{"sources": [], "every": "6h"}`,
		"unknown entry key":   `{"sources": [{"source": "a", "format": "b", "fetch": "https://example.org/", "key": "x"}]}`,
		"no format":           `{"sources": [{"source": "a", "fetch": "https://example.org/"}]}`,
		"address not public":  `{"sources": [{"source": "a", "format": "b", "fetch": "http://localhost/feed"}]}`,
		"address with a user": `{"sources": [{"source": "a", "format": "b", "fetch": "https://clerk:made-up@example.org/"}]}`,
		"address not http":    `{"sources": [{"source": "a", "format": "b", "fetch": "file:///etc/hosts"}]}`,
		"two values":          `{"sources": []}{"sources": []}`,
	}
	for name, text := range bad {
		if _, err := ParseSources([]byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestFormAddressTypedTwoWays pins a known gap in contract v1. The hub
// compares addresses exactly as sent, and Trawl cannot file a form
// address again in its clean form, because the hub wants the visitor's
// proof of work with every site-form filing. So the same address typed
// two ways through the form becomes two recorded items, each with the
// same clean address in its result. When the contract gives Trawl a way
// to put a clean form address to the hub, this test should change.
func TestFormAddressTypedTwoWays(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	first := r.submit(t, "Wage calculator", wage)
	second := r.submit(t, "Wage calculator", wage+"/?utm_source=x")
	drain(t, r.worker)
	clean(t, dir)
	items := byID(r.hub)
	for _, id := range []string{first, second} {
		if it := items[id]; it.State != muster.StateRecorded || record(it).Address != wage {
			t.Errorf("%s = %+v", id, it)
		}
	}
}
