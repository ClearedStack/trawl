package muster

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/screen"
)

// Fake is a hub for tests. It serves the part of contract v1 that Trawl
// touches: the five worker calls for items, and /v1/items. It is strict in
// the ways the contract is: a bearer token is required, each caller may
// make only its own calls, a body must be one JSON object with
// "contract": "v1" and no field the call does not define, and a refusal is
// a status with exactly {"error", "cause", "reason"}.
//
// The tests replay Muster's own saved examples against it, so where the
// two could drift apart a test fails. What it leaves out: proposals,
// edits, health reports, the feed, and the incident and release steps
// that follow a stopped item. It keeps everything in memory.
type Fake struct {
	mu sync.Mutex

	callers map[string]string // token -> caller
	down    bool
	// powBits is how many zero bits a site-form proof of work must show.
	powBits int

	now      time.Time
	leaseFor time.Duration
	items    []*fakeItem
	byID     map[string]*fakeItem
	byAddr   map[string]*fakeItem
	leases   map[string]*fakeLease
	leaseSeq int
	arrivals int
	calls    []Call
}

// Call is one request the fake received.
type Call struct {
	Caller string
	Path   string
	Body   string
	Status int
}

type fakeItem struct {
	FakeItem
	order     int
	lease     string    // the lease that protects the step, if any
	expires   time.Time // when that lease runs out
	notBefore time.Time // the step is not offered before this
	pausedIn  string
	results   map[string]json.RawMessage
}

type fakeLease struct {
	item   *fakeItem
	caller string
	ended  bool
	// The call that ended the lease and its answer, so a repeat of that
	// call gets the same answer and does nothing twice.
	endPath, endBody string
	endAnswer        any
}

// FakeItem is the fake's view of one item.
type FakeItem struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Lane    string `json:"lane"`
	Source  string `json:"source,omitempty"`
	Title   string `json:"title,omitempty"`
	Address string `json:"address,omitempty"`
	// Next is the worker whose step is queued, if any.
	Next string `json:"next,omitempty"`
	// Held is true after fail with E_PARSE: the item is not offered again.
	Held bool `json:"held,omitempty"`
	// Attempts counts the leases of the item's current step.
	Attempts int `json:"attempts"`
	// Results holds the latest result from each caller.
	Results map[string]json.RawMessage `json:"results,omitempty"`
}

// step is one row of the contract's queue steps: a worker holds an item
// in one state and may complete it to another, after which the next
// worker's step is queued.
type step struct {
	worker, from, to, next string
	// later marks a step that is offered only after seven days.
	later bool
}

var steps = []step{
	{worker: "trawl", from: "discovered", to: "recorded", next: "sextant"},
	{worker: "trawl", from: "discovered", to: "rejected"},
	{worker: "sextant", from: "recorded", to: "observed", next: "assay"},
	{worker: "assay", from: "observed", to: "scored", next: "countersign"},
	{worker: "assay", from: "observed", to: "screened_out", next: "countersign"},
	{worker: "countersign", from: "scored", to: "approved", next: "waybill"},
	{worker: "countersign", from: "scored", to: "parked"},
	{worker: "countersign", from: "scored", to: "linked", next: "chartroom"},
	{worker: "countersign", from: "scored", to: "merged", next: "chartroom"},
	{worker: "countersign", from: "scored", to: "rejected"},
	{worker: "countersign", from: "screened_out", to: "observed", next: "assay"},
	{worker: "waybill", from: "approved", to: "approved", next: "moldloft"},
	{worker: "moldloft", from: "approved", to: "building", next: "shakedown"},
	{worker: "shakedown", from: "building", to: "building", next: "chartroom"},
	{worker: "shakedown", from: "building", to: "parked"},
	{worker: "chartroom", from: "building", to: "live", next: "tidegauge", later: true},
	{worker: "chartroom", from: "linked", to: "linked"},
	{worker: "chartroom", from: "merged", to: "merged"},
	{worker: "chartroom", from: "retired", to: "retired"},
	{worker: "tidegauge", from: "live", to: "live", next: "tidegauge", later: true},
	{worker: "tidegauge", from: "live", to: "retired", next: "chartroom"},
}

// pausable are the states the state table lets move to needs_subtool.
var pausable = map[string]bool{"recorded": true, "observed": true, "approved": true}

var workers = map[string]bool{
	"trawl": true, "sextant": true, "assay": true, "countersign": true, "waybill": true,
	"moldloft": true, "shakedown": true, "chartroom": true, "tidegauge": true, "bellbuoy": true,
}

// sourceLanes are the sources the hub knows and the lane a new item from
// each starts in.
var sourceLanes = map[string]string{
	SourceSAMGov:       "normal",
	SourceGitHubIssues: "normal",
	SourceWebSweep:     "backfill",
	SourceSiteForm:     "normal",
}

// NewFake returns an empty fake at the made-up moment the contract's
// examples start from. callers maps each accepted bearer token to the
// registry name of its caller, for example "trawl" or "conn".
func NewFake(callers map[string]string) *Fake {
	return &Fake{
		callers:  callers,
		powBits:  20,
		now:      time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
		leaseFor: time.Minute,
		byID:     map[string]*fakeItem{},
		byAddr:   map[string]*fakeItem{},
		leases:   map[string]*fakeLease{},
	}
}

// SetDown makes every call answer 503, as when the hub is too busy.
func (f *Fake) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// SetPowBits sets how hard the site-form proof of work is. The contract's
// figure is 20; tests that make many submissions use fewer.
func (f *Fake) SetPowBits(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.powBits = n
}

// Advance moves the fake's clock forward. A lease that has run out is
// offered again; until someone takes it, the old lease still counts.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Now returns the fake's clock.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Seed puts an item on record as it stands, without going through
// /v1/items. An empty identifier is given the next number.
func (f *Fake) Seed(it FakeItem) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.add(it).ID
}

func (f *Fake) nextID() string {
	highest := 0
	for _, it := range f.items {
		if n, err := strconv.Atoi(strings.TrimPrefix(it.ID, "PA-")); err == nil && n > highest {
			highest = n
		}
	}
	return fmt.Sprintf("PA-%04d", highest+1)
}

func (f *Fake) add(it FakeItem) *fakeItem {
	if it.ID == "" {
		it.ID = f.nextID()
	}
	if it.Lane == "" {
		it.Lane = "normal"
	}
	f.arrivals++
	item := &fakeItem{FakeItem: it, order: f.arrivals, results: map[string]json.RawMessage{}}
	for k, v := range it.Results {
		item.results[k] = v
	}
	item.Results = nil
	f.items = append(f.items, item)
	f.byID[it.ID] = item
	if it.Address != "" {
		f.byAddr[it.Address] = item
	}
	return item
}

// Items returns every item, in the order they were created.
func (f *Fake) Items() []FakeItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeItem, 0, len(f.items))
	for _, it := range f.items {
		view := it.FakeItem
		if len(it.results) > 0 {
			view.Results = map[string]json.RawMessage{}
			for k, v := range it.results {
				view.Results[k] = v
			}
		}
		out = append(out, view)
	}
	return out
}

// Calls returns every request received so far.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// refusal is the body of a refused call. The causes and sentences are the
// contract's.
type refusal struct {
	status int
	Error  errs.Code `json:"error"`
	Cause  string    `json:"cause"`
	Reason string    `json:"reason"`
}

var (
	refuseNotJSON      = &refusal{400, errs.Parse, "not_json", "the request is not one json value"}
	refuseUnknownField = &refusal{400, errs.Parse, "unknown_field", "the request has a field this call does not define"}
	refuseMissing      = &refusal{400, errs.Parse, "missing_field", "a required field is missing"}
	refuseBadField     = &refusal{400, errs.Parse, "bad_field", "a field does not have the form this call requires"}
	refuseUseCall      = &refusal{400, errs.Parse, "bad_field", "report a missing rule with the needs_subtool call"}
	refuseProof        = &refusal{400, errs.Parse, "bad_field", "the proof of work is not valid"}
	refuseNothingLeft  = &refusal{400, errs.Parse, "bad_field", "nothing is left of the text once markup is removed"}
	refuseCredential   = &refusal{401, errs.Parse, "no_credential", "no valid credential was presented"}
	refuseCaller       = &refusal{403, errs.Parse, "not_your_call", "this caller may not make this call"}
	refuseNoCall       = &refusal{404, errs.Parse, "no_such_call", "no such call"}
	refuseMethod       = &refusal{405, errs.Parse, "wrong_method", "this call does not accept that method"}
	refuseTooLarge     = &refusal{413, errs.Parse, "too_large", "the request is too large"}
	refuseType         = &refusal{415, errs.Parse, "wrong_type", "the content type must be application/json"}
	refuseLease        = &refusal{409, errs.Parse, "lease_not_held", "the lease is not held"}
	refuseMove         = &refusal{409, errs.Parse, "move_not_allowed", "that move is not allowed"}
	refuseContract     = &refusal{422, errs.NeedsSubtool, "unknown_contract", "this contract version is not known"}
	refuseSource       = &refusal{422, errs.NeedsSubtool, "unknown_value", "this source is not known"}
	refuseBusy         = &refusal{503, errs.Timeout, "busy", "too many calls at once"}
)

// ServeHTTP answers one call.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxRequest+1))

	f.mu.Lock()
	caller, status, answer := f.serve(r, body)
	f.calls = append(f.calls, Call{Caller: caller, Path: r.URL.Path, Body: string(body), Status: status})
	f.mu.Unlock()

	if answer == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(answer)
}

func (f *Fake) serve(r *http.Request, body []byte) (caller string, status int, answer any) {
	type handler struct {
		fn  func(caller string, body []byte) (int, any)
		who func(caller string) bool
	}
	isWorker := func(c string) bool { return workers[c] }
	handlers := map[string]handler{
		pathLease:        {f.lease, isWorker},
		pathHeartbeat:    {f.heartbeat, isWorker},
		pathComplete:     {f.complete, isWorker},
		pathFail:         {f.fail, isWorker},
		pathNeedsSubtool: {f.needsSubtool, isWorker},
		pathItems:        {f.file, func(c string) bool { return c == Worker || c == "conn" }},
	}
	refused := func(r *refusal) (string, int, any) { return caller, r.status, r }

	h, known := handlers[r.URL.Path]
	if !known {
		return refused(refuseNoCall)
	}
	if r.Method != http.MethodPost {
		return refused(refuseMethod)
	}
	token, isBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	name, ok := f.callers[token]
	if !isBearer || !ok {
		return refused(refuseCredential)
	}
	caller = name
	if !h.who(caller) {
		return refused(refuseCaller)
	}
	if f.down {
		return refused(refuseBusy)
	}
	if r.Header.Get("Content-Type") != "application/json" {
		return refused(refuseType)
	}
	if len(body) > maxRequest {
		return refused(refuseTooLarge)
	}
	status, answer = h.fn(caller, body)
	return caller, status, answer
}

// read decodes a request body strictly: one JSON object, no unknown
// fields, and the contract version this fake knows.
func read(body []byte, v any, contract *string) *refusal {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		switch {
		case strings.HasPrefix(err.Error(), "json: unknown field"):
			return refuseUnknownField
		case isTypeError(err):
			return refuseBadField
		default:
			return refuseNotJSON
		}
	}
	if _, err := dec.Token(); err != io.EOF {
		return refuseNotJSON
	}
	if *contract == "" {
		return refuseMissing
	}
	if *contract != ContractVersion {
		return refuseContract
	}
	return nil
}

func isTypeError(err error) bool {
	_, ok := err.(*json.UnmarshalTypeError)
	return ok
}

// object reports whether raw is absent or a JSON object.
func object(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || trimmed[0] == '{'
}

var laneOrder = map[string]int{"urgent": 0, "normal": 1, "backfill": 2}

type leaseAnswer struct {
	Lease   string                     `json:"lease"`
	Kind    string                     `json:"kind"`
	ID      string                     `json:"id"`
	State   string                     `json:"state"`
	Lane    string                     `json:"lane"`
	Attempt int                        `json:"attempt"`
	Expires string                     `json:"expires"`
	Found   *Found                     `json:"found,omitempty"`
	Results map[string]json.RawMessage `json:"results,omitempty"`
}

type heartbeatAnswer struct {
	Lease   string `json:"lease"`
	Expires string `json:"expires"`
}

// outcome is the answer to complete, fail, needs_subtool and /v1/items.
type outcome struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Next      string `json:"next,omitempty"`
	Held      bool   `json:"held,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

func (f *Fake) lease(caller string, body []byte) (int, any) {
	var req leaseRequest
	if r := read(body, &req, &req.Contract); r != nil {
		return r.status, r
	}
	var open []*fakeItem
	for _, it := range f.items {
		free := it.lease == "" || !it.expires.After(f.now)
		if it.Next == caller && !it.Held && free && !it.notBefore.After(f.now) {
			open = append(open, it)
		}
	}
	if len(open) == 0 {
		return http.StatusNoContent, nil
	}
	sort.SliceStable(open, func(i, j int) bool {
		if laneOrder[open[i].Lane] != laneOrder[open[j].Lane] {
			return laneOrder[open[i].Lane] < laneOrder[open[j].Lane]
		}
		return open[i].order < open[j].order
	})
	it := open[0]
	f.leaseSeq++
	id := fmt.Sprintf("%s.%d", it.ID, f.leaseSeq)
	it.lease, it.expires = id, f.now.Add(f.leaseFor)
	it.Attempts++
	f.leases[id] = &fakeLease{item: it, caller: caller}

	answer := leaseAnswer{
		Lease:   id,
		Kind:    KindItem,
		ID:      it.ID,
		State:   it.State,
		Lane:    it.Lane,
		Attempt: it.Attempts,
		Expires: it.expires.Format(time.RFC3339),
	}
	if it.Address != "" {
		answer.Found = &Found{Source: it.Source, Title: it.Title, Address: it.Address}
	}
	if len(it.results) > 0 {
		answer.Results = it.results
	}
	return http.StatusOK, answer
}

// held finds an open lease of the caller. For a lease that already ended,
// it returns the saved answer when the same call is repeated.
func (f *Fake) held(caller, lease, path string, body []byte) (*fakeLease, any, *refusal) {
	l, ok := f.leases[lease]
	if !ok || l.caller != caller {
		return nil, nil, refuseLease
	}
	if l.ended {
		if l.endPath == path && l.endBody == string(body) {
			return nil, l.endAnswer, nil
		}
		return nil, nil, refuseLease
	}
	if l.item.lease != lease {
		// Someone else has taken the step since this lease ran out.
		return nil, nil, refuseLease
	}
	return l, nil, nil
}

func (f *Fake) end(l *fakeLease, path string, body []byte, answer any) (int, any) {
	l.ended, l.endPath, l.endBody, l.endAnswer = true, path, string(body), answer
	l.item.lease = ""
	return http.StatusOK, answer
}

func (f *Fake) heartbeat(caller string, body []byte) (int, any) {
	var req heartbeatRequest
	if r := read(body, &req, &req.Contract); r != nil {
		return r.status, r
	}
	if req.Lease == "" {
		return refuseMissing.status, refuseMissing
	}
	l, _, r := f.held(caller, req.Lease, pathHeartbeat, body)
	if r != nil || l == nil {
		return refuseLease.status, refuseLease
	}
	l.item.expires = f.now.Add(f.leaseFor)
	return http.StatusOK, heartbeatAnswer{Lease: req.Lease, Expires: l.item.expires.Format(time.RFC3339)}
}

func (f *Fake) complete(caller string, body []byte) (int, any) {
	var req struct {
		Contract string          `json:"contract"`
		Lease    string          `json:"lease"`
		State    string          `json:"state"`
		Result   json.RawMessage `json:"result"`
	}
	if r := read(body, &req, &req.Contract); r != nil {
		return r.status, r
	}
	if req.Lease == "" || req.State == "" {
		return refuseMissing.status, refuseMissing
	}
	if !object(req.Result) {
		return refuseBadField.status, refuseBadField
	}
	l, repeat, r := f.held(caller, req.Lease, pathComplete, body)
	if r != nil {
		return r.status, r
	}
	if l == nil {
		return http.StatusOK, repeat
	}
	it := l.item
	var row *step
	for i := range steps {
		if steps[i].worker == caller && steps[i].from == it.State && steps[i].to == req.State {
			row = &steps[i]
		}
	}
	if row == nil {
		return refuseMove.status, refuseMove
	}
	it.State, it.Next = row.to, row.next
	// A new step: it arrives at the back, and its attempts start again.
	f.arrivals++
	it.order = f.arrivals
	it.Attempts = 0
	if row.later {
		it.notBefore = f.now.Add(7 * 24 * time.Hour)
		it.Lane = "backfill"
	}
	if len(bytes.TrimSpace(req.Result)) > 0 {
		it.results[caller] = req.Result
	}
	return f.end(l, pathComplete, body, outcome{ID: it.ID, State: it.State, Next: it.Next})
}

func (f *Fake) fail(caller string, body []byte) (int, any) {
	var req failRequest
	if r := read(body, &req, &req.Contract); r != nil {
		return r.status, r
	}
	if req.Lease == "" || req.Code == "" {
		return refuseMissing.status, refuseMissing
	}
	switch req.Code {
	case errs.Timeout, errs.Parse:
	case errs.NeedsSubtool:
		return refuseUseCall.status, refuseUseCall
	default:
		return refuseBadField.status, refuseBadField
	}
	l, repeat, r := f.held(caller, req.Lease, pathFail, body)
	if r != nil {
		return r.status, r
	}
	if l == nil {
		return http.StatusOK, repeat
	}
	it := l.item
	if req.Code == errs.Timeout {
		// The step stays in the queue, in its place.
		return f.end(l, pathFail, body, outcome{ID: it.ID, State: it.State, Next: it.Next})
	}
	it.Held = true
	return f.end(l, pathFail, body, outcome{ID: it.ID, State: it.State, Held: true})
}

func (f *Fake) needsSubtool(caller string, body []byte) (int, any) {
	var req needsSubtoolRequest
	if r := read(body, &req, &req.Contract); r != nil {
		return r.status, r
	}
	if req.Lease == "" {
		return refuseMissing.status, refuseMissing
	}
	l, repeat, r := f.held(caller, req.Lease, pathNeedsSubtool, body)
	if r != nil {
		return r.status, r
	}
	if l == nil {
		return http.StatusOK, repeat
	}
	it := l.item
	if !pausable[it.State] {
		return refuseMove.status, refuseMove
	}
	it.pausedIn, it.State, it.Next = it.State, "needs_subtool", ""
	return f.end(l, pathNeedsSubtool, body, outcome{ID: it.ID, State: it.State})
}

var sourceForm = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func (f *Fake) file(caller string, body []byte) (int, any) {
	var req struct {
		Contract string          `json:"contract"`
		Source   string          `json:"source"`
		Title    string          `json:"title"`
		Address  string          `json:"address"`
		Result   json.RawMessage `json:"result"`
		Pow      string          `json:"pow"`
		Visitor  string          `json:"visitor"`
	}
	if r := read(body, &req, &req.Contract); r != nil {
		return r.status, r
	}
	if req.Source == "" || req.Title == "" || req.Address == "" {
		return refuseMissing.status, refuseMissing
	}
	oneLine := func(s string) bool {
		for _, r := range s {
			if unicode.IsControl(r) {
				return false
			}
		}
		return utf8.ValidString(s)
	}
	unbroken := func(s string) bool {
		for _, r := range s {
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				return false
			}
		}
		return utf8.ValidString(s)
	}
	if !sourceForm.MatchString(req.Source) || !object(req.Result) ||
		utf8.RuneCountInString(req.Title) > MaxTitle || !oneLine(req.Title) ||
		utf8.RuneCountInString(req.Address) > MaxAddress || !unbroken(req.Address) {
		return refuseBadField.status, refuseBadField
	}
	lane, known := sourceLanes[req.Source]
	if !known {
		return refuseSource.status, refuseSource
	}
	if req.Source == SourceSiteForm {
		if req.Pow == "" || req.Visitor == "" {
			return refuseMissing.status, refuseMissing
		}
		if _, err := netip.ParseAddr(req.Visitor); err != nil {
			return refuseBadField.status, refuseBadField
		}
		if !proven(req.Pow, req.Source, req.Title, req.Address, f.powBits, f.now) {
			return refuseProof.status, refuseProof
		}
	} else if req.Pow != "" || req.Visitor != "" {
		return refuseBadField.status, refuseBadField
	}
	title := strings.Join(strings.Fields(screen.StripHTML(req.Title)), " ")
	if title == "" {
		return refuseNothingLeft.status, refuseNothingLeft
	}

	if known, ok := f.byAddr[req.Address]; ok {
		return http.StatusOK, outcome{ID: known.ID, State: known.State, Duplicate: true}
	}
	it := f.add(FakeItem{State: StateDiscovered, Lane: lane, Source: req.Source, Title: title, Address: req.Address, Next: Worker})
	if len(bytes.TrimSpace(req.Result)) > 0 {
		it.results[caller] = req.Result
	}
	return http.StatusCreated, outcome{ID: it.ID, State: it.State, Next: it.Next}
}

// The site-form proof of work, as the contract defines it: the token is
// "<day>.<nonce>", the UTC date and up to 32 lower-case hexadecimal
// digits. It is valid when the SHA-256 hash of "muster-pow-v1", the day,
// the source, the title, the address and the nonce, joined by single zero
// bytes, begins with enough zero bits, and the day is today or yesterday.

var powToken = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2})\.([0-9a-f]{1,32})$`)

func powBits(day, source, title, address, nonce string) int {
	sum := sha256.Sum256([]byte(strings.Join([]string{"muster-pow-v1", day, source, title, address, nonce}, "\x00")))
	zeros := 0
	for _, b := range sum {
		if b != 0 {
			return zeros + bits.LeadingZeros8(b)
		}
		zeros += 8
	}
	return zeros
}

func proven(token, source, title, address string, difficulty int, now time.Time) bool {
	m := powToken.FindStringSubmatch(token)
	if m == nil {
		return false
	}
	day := m[1]
	today := now.UTC()
	if day != today.Format("2006-01-02") && day != today.AddDate(0, 0, -1).Format("2006-01-02") {
		return false
	}
	return powBits(day, source, title, address, m[2]) >= difficulty
}

// SolvePow finds a proof-of-work token for a site-form filing, the way
// the form on the site would, so tests can file as Conn.
func (f *Fake) SolvePow(source, title, address string) string {
	f.mu.Lock()
	day, difficulty := f.now.UTC().Format("2006-01-02"), f.powBits
	f.mu.Unlock()
	for n := uint64(0); ; n++ {
		nonce := strconv.FormatUint(n, 16)
		if powBits(day, source, title, address, nonce) >= difficulty {
			return day + "." + nonce
		}
	}
}
