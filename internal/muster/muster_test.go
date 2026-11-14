package muster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClearedStack/trawl/internal/errs"
)

var ctx = context.Background()

var (
	trawlToken = tokenFor("trawl")
	connToken  = tokenFor("conn")
)

// hub starts a fake and returns it with a client that presents the given
// token.
func hub(t *testing.T, token string) (*Fake, *HTTP) {
	t.Helper()
	fake := NewFake(allCallers())
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, client(t, srv.URL, token)
}

func client(t *testing.T, base, token string) *HTTP {
	t.Helper()
	tokenFile := ""
	if token != "" {
		tokenFile = filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := NewHTTP(base, tokenFile, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFileLeaseComplete(t *testing.T) {
	fake, c := hub(t, trawlToken)

	if lease, err := c.Lease(ctx); lease != nil || err != nil {
		t.Fatalf("nothing queued: %v, %v", lease, err)
	}

	result := map[string]string{"kind": "calculator", "via": "sam-gov:made-up"}
	filed, err := c.File(ctx, Filing{Source: SourceSAMGov, Title: "Wage calculator", Address: "https://tools.agency-example.gov/wage", Result: result})
	if err != nil || !filed.New || filed.ID != "PA-0001" {
		t.Fatalf("first filing: %+v, %v", filed, err)
	}
	again, err := c.File(ctx, Filing{Source: SourceWebSweep, Title: "Another title", Address: "https://tools.agency-example.gov/wage"})
	if err != nil || again.New || again.ID != "PA-0001" {
		t.Fatalf("second filing of the same address: %+v, %v", again, err)
	}
	// Addresses are compared exactly as sent.
	other, err := c.File(ctx, Filing{Source: SourceWebSweep, Title: "Swept", Address: "https://tools.agency-example.gov/wage/"})
	if err != nil || !other.New || other.ID != "PA-0002" {
		t.Fatalf("filing of an address that differs by a slash: %+v, %v", other, err)
	}

	lease, err := c.Lease(ctx)
	if err != nil || lease == nil {
		t.Fatalf("Lease: %v, %v", lease, err)
	}
	if lease.Lease != "PA-0001.1.1" || lease.Kind != KindItem || lease.ID != "PA-0001" || lease.State != StateDiscovered ||
		lease.Lane != "normal" || lease.Attempt != 1 || lease.Expires != "2026-01-05T09:01:00Z" {
		t.Errorf("lease = %+v", lease)
	}
	if lease.Found == nil || *lease.Found != (Found{Source: SourceSAMGov, Title: "Wage calculator", Address: "https://tools.agency-example.gov/wage"}) {
		t.Errorf("found = %+v", lease.Found)
	}
	var filedResult map[string]string
	if err := json.Unmarshal(lease.Results["trawl"], &filedResult); err != nil || !reflect.DeepEqual(filedResult, result) {
		t.Errorf("results.trawl = %s", lease.Results["trawl"])
	}

	if err := c.Heartbeat(ctx, lease.Lease); err != nil {
		t.Errorf("Heartbeat: %v", err)
	}
	if err := c.Complete(ctx, lease.Lease, StateRecorded, result); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Repeating the call that ended the lease gives the same answer.
	if err := c.Complete(ctx, lease.Lease, StateRecorded, result); err != nil {
		t.Errorf("repeated Complete: %v", err)
	}
	// A different ending for the same lease is refused.
	if err := c.Complete(ctx, lease.Lease, StateRejected, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("different Complete: %v", err)
	}
	if err := c.Heartbeat(ctx, lease.Lease); !errors.Is(err, ErrConflict) {
		t.Errorf("Heartbeat after the end: %v", err)
	}

	items := fake.Items()
	if len(items) != 2 || items[0].State != StateRecorded || items[0].Next != "sextant" {
		t.Errorf("items = %+v", items)
	}
	// A swept address starts in the backfill lane, so it comes last.
	if items[1].Lane != "backfill" || items[1].Next != "trawl" {
		t.Errorf("swept item = %+v", items[1])
	}
}

// TestRequestBodies checks the exact bytes of every call: the contract
// version in the body, the lease under "lease", and no field the contract
// does not define.
func TestRequestBodies(t *testing.T) {
	fake, c := hub(t, trawlToken)
	fake.Seed(FakeItem{State: StateDiscovered, Next: "trawl"})
	fake.Seed(FakeItem{State: StateDiscovered, Next: "trawl"})
	fake.Seed(FakeItem{State: StateDiscovered, Next: "trawl"})

	c.File(ctx, Filing{Source: "github-issues", Title: "A", Address: "https://example.org/a"})
	c.File(ctx, Filing{Source: "sam-gov", Title: "T", Address: "https://example.org/b", Result: map[string]string{"kind": "link"}})
	one, _ := c.Lease(ctx)
	c.Heartbeat(ctx, one.Lease)
	c.Complete(ctx, one.Lease, StateRejected, nil)
	two, _ := c.Lease(ctx)
	c.Fail(ctx, two.Lease, errs.Timeout)
	three, _ := c.Lease(ctx)
	c.NeedsSubtool(ctx, three.Lease)

	want := []string{
		`/v1/items {"contract":"v1","source":"github-issues","title":"A","address":"https://example.org/a"}`,
		`/v1/items {"contract":"v1","source":"sam-gov","title":"T","address":"https://example.org/b","result":{"kind":"link"}}`,
		`/v1/lease {"contract":"v1"}`,
		`/v1/heartbeat {"contract":"v1","lease":"PA-0001.1.1"}`,
		`/v1/complete {"contract":"v1","lease":"PA-0001.1.1","state":"rejected"}`,
		`/v1/lease {"contract":"v1"}`,
		`/v1/fail {"contract":"v1","lease":"PA-0002.2.1","code":"E_TIMEOUT"}`,
		`/v1/lease {"contract":"v1"}`,
		`/v1/needs_subtool {"contract":"v1","lease":"PA-0002.2.2"}`,
	}
	var got []string
	for _, call := range fake.Calls() {
		if call.Caller != "trawl" {
			t.Errorf("call made as %q", call.Caller)
		}
		got = append(got, call.Path+" "+call.Body)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFailAndPause(t *testing.T) {
	fake, c := hub(t, trawlToken)
	for i := 0; i < 3; i++ {
		fake.Seed(FakeItem{State: StateDiscovered, Next: "trawl"})
	}

	// E_TIMEOUT: the step stays in the queue, in its place.
	first, _ := c.Lease(ctx)
	if err := c.Fail(ctx, first.Lease, errs.Timeout); err != nil {
		t.Fatal(err)
	}
	again, _ := c.Lease(ctx)
	if again.ID != first.ID || again.Attempt != 2 {
		t.Errorf("after E_TIMEOUT leased %+v", again)
	}
	// E_PARSE: the item is held and not offered again.
	if err := c.Fail(ctx, again.Lease, errs.Parse); err != nil {
		t.Fatal(err)
	}
	next, _ := c.Lease(ctx)
	if next.ID == first.ID {
		t.Error("a held item was offered again")
	}
	// E_NEEDS_SUBTOOL is not a code for fail.
	if err := c.Fail(ctx, next.Lease, errs.NeedsSubtool); !errors.Is(err, ErrRefused) {
		t.Errorf("Fail with E_NEEDS_SUBTOOL: %v", err)
	}
	// The state table has no move from discovered to needs_subtool, so
	// Trawl cannot pause an item.
	err := c.NeedsSubtool(ctx, next.Lease)
	var refusal *Refusal
	if !errors.Is(err, ErrConflict) || !errors.As(err, &refusal) || refusal.Cause != "move_not_allowed" {
		t.Errorf("NeedsSubtool on a discovered item: %v", err)
	}
	items := fake.Items()
	if !items[0].Held || items[1].State != StateDiscovered || items[1].Held {
		t.Errorf("items = %+v", items)
	}
}

func TestLaneOrderIsTheHubs(t *testing.T) {
	fake, c := hub(t, trawlToken)
	fake.Seed(FakeItem{ID: "PA-0001", State: StateDiscovered, Lane: "backfill", Next: "trawl"})
	fake.Seed(FakeItem{ID: "PA-0002", State: StateDiscovered, Lane: "normal", Next: "trawl"})
	fake.Seed(FakeItem{ID: "PA-0003", State: StateDiscovered, Lane: "urgent", Next: "trawl"})
	fake.Seed(FakeItem{ID: "PA-0004", State: "recorded", Lane: "urgent", Next: "sextant"})
	fake.Seed(FakeItem{ID: "PA-0005", State: StateDiscovered, Lane: "normal", Next: "trawl", Held: true})
	var order []string
	for {
		lease, err := c.Lease(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if lease == nil {
			break
		}
		order = append(order, lease.ID+" "+lease.Lane)
		c.Complete(ctx, lease.Lease, StateRecorded, nil)
	}
	if strings.Join(order, ",") != "PA-0003 urgent,PA-0002 normal,PA-0001 backfill" {
		t.Errorf("order = %v", order)
	}
}

func TestLateResultIsKeptUntilTheStepIsRetaken(t *testing.T) {
	fake, c := hub(t, trawlToken)
	fake.Seed(FakeItem{State: StateDiscovered, Next: "trawl"})
	fake.Seed(FakeItem{State: StateDiscovered, Next: "trawl"})

	late, _ := c.Lease(ctx)
	lost, _ := c.Lease(ctx)
	fake.Advance(2 * time.Minute)
	// Nobody has retaken the first step: the late result still counts.
	if err := c.Complete(ctx, late.Lease, StateRecorded, nil); err != nil {
		t.Errorf("late Complete: %v", err)
	}
	// The second step is taken again; its old lease is refused.
	retaken, _ := c.Lease(ctx)
	if retaken.ID != lost.ID || retaken.Lease == lost.Lease {
		t.Fatalf("retaken = %+v", retaken)
	}
	if err := c.Complete(ctx, lost.Lease, StateRecorded, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("Complete on a lost lease: %v", err)
	}
}

func TestRefusals(t *testing.T) {
	fake, c := hub(t, trawlToken)
	fake.Seed(FakeItem{State: StateDiscovered, Next: "trawl"})
	lease, _ := c.Lease(ctx)
	_, noTitle := c.File(ctx, Filing{Source: SourceSAMGov, Title: "", Address: "https://example.org/"})
	_, noAddress := c.File(ctx, Filing{Source: SourceSAMGov, Title: "T", Address: ""})
	_, spaced := c.File(ctx, Filing{Source: SourceSAMGov, Title: "T", Address: "https://example.org/a b"})
	_, twoLines := c.File(ctx, Filing{Source: SourceSAMGov, Title: "one\ntwo", Address: "https://example.org/"})
	_, longTitle := c.File(ctx, Filing{Source: SourceSAMGov, Title: strings.Repeat("a", MaxTitle+1), Address: "https://example.org/"})
	// The limit is in bytes: 101 two-byte letters are 202 bytes.
	_, wideTitle := c.File(ctx, Filing{Source: SourceSAMGov, Title: strings.Repeat("\u00e9", MaxTitle/2+1), Address: "https://example.org/"})
	_, onlyMarkup := c.File(ctx, Filing{Source: SourceSAMGov, Title: "<b></b>", Address: "https://example.org/"})
	_, stranger := c.File(ctx, Filing{Source: "made-up-source", Title: "T", Address: "https://example.org/"})
	// Trawl has no visitor and no proof of work, so it cannot file as
	// the site's form.
	_, asForm := c.File(ctx, Filing{Source: SourceSiteForm, Title: "T", Address: "https://example.org/"})

	cases := []struct {
		name   string
		err    error
		status int
		cause  string
		kind   error
	}{
		{"a lease never given", c.Heartbeat(ctx, "PA-0009.9.1"), 409, "lease_not_held", ErrConflict},
		{"a move not in the table", c.Complete(ctx, lease.Lease, "observed", nil), 409, "move_not_allowed", ErrConflict},
		{"no lease named", c.Heartbeat(ctx, ""), 400, "missing_field", ErrRefused},
		{"no state named", c.Complete(ctx, lease.Lease, "", nil), 400, "missing_field", ErrRefused},
		{"a result that is not an object", c.Complete(ctx, lease.Lease, StateRecorded, []int{1, 2}), 400, "bad_field", ErrRefused},
		{"an unknown code", c.Fail(ctx, lease.Lease, errs.Code("E_OTHER")), 400, "bad_field", ErrRefused},
		{"a filing with no title", noTitle, 400, "missing_field", ErrRefused},
		{"a filing with no address", noAddress, 400, "missing_field", ErrRefused},
		{"an address with a space", spaced, 400, "bad_field", ErrRefused},
		{"a title of two lines", twoLines, 400, "bad_field", ErrRefused},
		{"a title too long", longTitle, 400, "bad_field", ErrRefused},
		{"a title too long in bytes", wideTitle, 400, "bad_field", ErrRefused},
		{"a title that is only markup", onlyMarkup, 400, "bad_field", ErrRefused},
		{"an unknown source", stranger, 422, "unknown_value", ErrRefused},
		{"a site-form filing by Trawl", asForm, 400, "missing_field", ErrRefused},
	}
	for _, tc := range cases {
		var refusal *Refusal
		if !errors.As(tc.err, &refusal) || refusal.Status != tc.status || refusal.Cause != tc.cause || !errors.Is(tc.err, tc.kind) || !refusal.Code.Valid() {
			t.Errorf("%s: %v", tc.name, tc.err)
		}
	}
	if len(fake.Items()) != 1 {
		t.Error("a refused filing was kept")
	}
}

// post sends one raw call, the way a caller other than Trawl would.
func post(t *testing.T, base, token string, path string, body any) (int, map[string]any) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var answer map[string]any
	json.NewDecoder(resp.Body).Decode(&answer)
	return resp.StatusCode, answer
}

// TestSiteFormFilingByConn files a candidate the way Conn does, with the
// visitor's proof of work.
func TestSiteFormFilingByConn(t *testing.T) {
	fake, c := hub(t, trawlToken)
	fake.SetPowBits(8)
	const title, address = "A made-up tool a visitor asked for", "https://example.org/made-up/asked-for-1"
	filing := map[string]any{
		"contract": "v1", "source": SourceSiteForm, "title": title, "address": address,
		"pow": fake.SolvePow(SourceSiteForm, title, address), "visitor": "203.0.113.7",
	}
	if status, answer := post(t, c.Base(), connToken, pathItems, filing); status != 201 || answer["id"] != "PA-0001" || answer["next"] != "trawl" {
		t.Fatalf("filing: %d %v", status, answer)
	}

	// The proof covers the title and the address.
	other := map[string]any{}
	for k, v := range filing {
		other[k] = v
	}
	other["address"] = "https://example.org/made-up/another"
	if status, answer := post(t, c.Base(), connToken, pathItems, other); status != 400 || answer["cause"] != "bad_field" {
		t.Errorf("a proof reused for another address: %d %v", status, answer)
	}
	other["address"], other["visitor"] = address+"/2", "not an address"
	if status, _ := post(t, c.Base(), connToken, pathItems, other); status != 400 {
		t.Errorf("a visitor that is not an address: %d", status)
	}
	// A proof or a visitor on any other source is refused.
	stray := map[string]any{"contract": "v1", "source": SourceSAMGov, "title": title, "address": address + "/3", "visitor": "203.0.113.7"}
	if status, answer := post(t, c.Base(), connToken, pathItems, stray); status != 400 || answer["cause"] != "bad_field" {
		t.Errorf("a visitor on a sam-gov filing: %d %v", status, answer)
	}

	// Trawl leases it like any other item and sees what was recorded,
	// and nothing of the visitor.
	lease, err := c.Lease(ctx)
	if err != nil || lease == nil || lease.Found == nil || *lease.Found != (Found{Source: SourceSiteForm, Title: title, Address: address}) || len(lease.Results) != 0 {
		t.Fatalf("lease = %+v, %v", lease, err)
	}
	for _, call := range fake.Calls() {
		if call.Caller == "trawl" && strings.Contains(call.Body, "203.0.113.7") {
			t.Error("the visitor's address reached Trawl")
		}
	}
}

// TestFakeIsStrict sends the fake the kinds of request the real hub
// refuses, without going through the client.
func TestFakeIsStrict(t *testing.T) {
	fake := NewFake(allCallers())
	srv := httptest.NewServer(fake)
	defer srv.Close()

	cases := []struct {
		name, method, path, token, contentType, body string
		status                                       int
		code                                         string
	}{
		{"no token", "POST", "/v1/lease", "", "application/json", `{"contract":"v1"}`, 401, "E_PARSE"},
		{"unknown token", "POST", "/v1/lease", "made-up-stranger", "application/json", `{"contract":"v1"}`, 401, "E_PARSE"},
		{"wrong method", "GET", "/v1/lease", trawlToken, "application/json", ``, 405, "E_PARSE"},
		{"no such call", "POST", "/v1/lookup", trawlToken, "application/json", `{"contract":"v1"}`, 404, "E_PARSE"},
		{"wrong content type", "POST", "/v1/lease", trawlToken, "text/plain", `{"contract":"v1"}`, 415, "E_PARSE"},
		{"not JSON", "POST", "/v1/lease", trawlToken, "application/json", `lease please`, 400, "E_PARSE"},
		{"empty body", "POST", "/v1/lease", trawlToken, "application/json", ``, 400, "E_PARSE"},
		{"two values", "POST", "/v1/lease", trawlToken, "application/json", `{"contract":"v1"}{"contract":"v1"}`, 400, "E_PARSE"},
		{"no contract", "POST", "/v1/lease", trawlToken, "application/json", `{}`, 400, "E_PARSE"},
		{"unknown contract", "POST", "/v1/lease", trawlToken, "application/json", `{"contract":"v2"}`, 422, "E_NEEDS_SUBTOOL"},
		{"worker named in a lease request", "POST", "/v1/lease", trawlToken, "application/json", `{"contract":"v1","worker":"trawl"}`, 400, "E_PARSE"},
		{"lane named in a lease request", "POST", "/v1/lease", trawlToken, "application/json", `{"contract":"v1","lane":"urgent"}`, 400, "E_PARSE"},
		{"lease_id instead of lease", "POST", "/v1/heartbeat", trawlToken, "application/json", `{"contract":"v1","lease_id":"PA-0001.1.1"}`, 400, "E_PARSE"},
		{"a hash key on a filing", "POST", "/v1/items", trawlToken, "application/json", `{"contract":"v1","source":"sam-gov","title":"T","address":"https://example.org/","sha256":"00"}`, 400, "E_PARSE"},
		{"too large", "POST", "/v1/items", trawlToken, "application/json", `{"contract":"v1","source":"sam-gov","title":"` + strings.Repeat("a", maxRequest) + `","address":"https://example.org/"}`, 413, "E_PARSE"},
	}
	for _, tc := range cases {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.contentType)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var answer map[string]string
		json.NewDecoder(resp.Body).Decode(&answer)
		resp.Body.Close()
		if resp.StatusCode != tc.status || answer["error"] != tc.code || answer["cause"] == "" || answer["reason"] == "" || len(answer) != 3 {
			t.Errorf("%s: status %d, answer %v", tc.name, resp.StatusCode, answer)
		}
	}
}

func TestOnlyTrawlAndConnMayFile(t *testing.T) {
	fake, sextant := hub(t, tokenFor("sextant"))
	_, err := sextant.File(ctx, Filing{Source: SourceSAMGov, Title: "T", Address: "https://example.org/"})
	var refusal *Refusal
	// 403 is sorted with the errors to wait out, so that a wrong token
	// never makes Trawl drop work.
	if !errors.As(err, &refusal) || refusal.Status != 403 || refusal.Cause != "not_your_call" || !errors.Is(err, ErrUnavailable) {
		t.Errorf("filing as sextant: %v", err)
	}
	conn := client(t, sextant.Base(), connToken)
	if _, err := conn.Lease(ctx); !errors.As(err, &refusal) || refusal.Status != 403 {
		t.Errorf("lease as conn: %v", err)
	}
	filed, err := conn.File(ctx, Filing{Source: SourceGitHubIssues, Title: "T", Address: "https://example.org/", Result: map[string]string{"seen": "made-up"}})
	if err != nil || !filed.New {
		t.Fatalf("filing as conn: %+v, %v", filed, err)
	}
	if got := fake.Items()[0]; string(got.Results["conn"]) != `{"seen":"made-up"}` || got.Next != "trawl" {
		t.Errorf("item = %+v", got)
	}
}

func TestUnavailable(t *testing.T) {
	fake, c := hub(t, trawlToken)
	fake.Seed(FakeItem{State: StateDiscovered, Next: "trawl"})
	fake.SetDown(true)
	if _, err := c.Lease(ctx); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Lease: %v", err)
	}
	if _, err := c.File(ctx, Filing{Source: SourceSAMGov, Title: "T", Address: "https://example.org/"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("File: %v", err)
	}
	fake.SetDown(false)
	if lease, err := c.Lease(ctx); err != nil || lease == nil {
		t.Errorf("Lease after recovery: %v, %v", lease, err)
	}
	if len(fake.Items()) != 1 {
		t.Error("a filing was kept while the hub was down")
	}
}

// TestTokenIsReadForEveryCall rotates the token file between two calls.
func TestTokenIsReadForEveryCall(t *testing.T) {
	fake := NewFake(map[string]string{"made-up-token-one": "trawl", "made-up-token-two": "trawl"})
	srv := httptest.NewServer(fake)
	defer srv.Close()
	var seen []string
	wrapped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Contract-Version") != "" {
			t.Error("the contract version belongs in the body, not in a header")
		}
		fake.ServeHTTP(w, r)
	}))
	defer wrapped.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	c, _ := NewHTTP(wrapped.URL, tokenFile, time.Second)
	if _, err := c.Lease(ctx); !errors.Is(err, ErrUnavailable) {
		t.Errorf("call with no token file yet: %v", err)
	}
	os.WriteFile(tokenFile, []byte("made-up-token-one\n"), 0o600)
	c.Lease(ctx)
	os.WriteFile(tokenFile, []byte("made-up-token-two\n"), 0o600)
	c.Lease(ctx)
	if !reflect.DeepEqual(seen, []string{"Bearer made-up-token-one", "Bearer made-up-token-two"}) {
		t.Errorf("tokens presented: %v", seen)
	}

	noToken := client(t, srv.URL, "")
	if _, err := noToken.Lease(ctx); !errors.Is(err, ErrUnavailable) {
		t.Errorf("call without a token: %v", err)
	}
}

// TestErrorsNameNothing checks that a failed call reports no address and
// none of the hub's own words.
func TestErrorsNameNothing(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	address := srv.URL
	srv.Close()
	c, err := NewHTTP(address, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Lease(ctx)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Lease against a closed port: %v", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error names the address: %v", err)
	}

	leaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"E_PARSE","cause":"node-7.made-up.internal 10.1.2.3","reason":"made-up detail about node-7"}`)
	}))
	defer leaky.Close()
	c, _ = NewHTTP(leaky.URL, "", time.Second)
	err = c.Heartbeat(ctx, "PA-0001.1.1")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	for _, leak := range []string{"node-7", "10.1.2.3", "made-up detail"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q repeats %q", err, leak)
		}
	}
	if _, err := NewHTTP("hub.made-up.svc:8080", "", time.Second); err == nil {
		t.Error("address without a scheme accepted")
	}
}

func TestOddAnswersToAFiling(t *testing.T) {
	answers := map[string]struct {
		status int
		body   string
		want   Filed
		ok     bool
	}{
		"created":                    {201, `{"id":"PA-0007","state":"discovered"}`, Filed{New: true, ID: "PA-0007"}, true},
		"created with more fields":   {201, `{"id":"PA-0007","state":"discovered","lane":"normal","made_up":1}`, Filed{New: true, ID: "PA-0007"}, true},
		"known":                      {200, `{"id":"PA-0003","duplicate":true}`, Filed{ID: "PA-0003"}, true},
		"known without an id":        {200, `{"duplicate":true}`, Filed{}, true},
		"200 that is not a known":    {200, `{"id":"PA-0003"}`, Filed{}, false},
		"200 with duplicate false":   {200, `{"id":"PA-0003","duplicate":false}`, Filed{}, false},
		"an answer that is not JSON": {201, `created`, Filed{}, false},
		"nothing at all":             {204, ``, Filed{}, false},
	}
	for name, a := range answers {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(a.status)
			io.WriteString(w, a.body)
		}))
		c, _ := NewHTTP(srv.URL, "", time.Second)
		got, err := c.File(ctx, Filing{Source: SourceSAMGov, Title: "T", Address: "https://example.org/"})
		srv.Close()
		if a.ok && (err != nil || got != a.want) {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
		if !a.ok && !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
}

func TestValidItemID(t *testing.T) {
	for _, id := range []string{"PA-0000", "PA-0001", "PA-9999", "PA-10000", "PA-123456789"} {
		if !ValidItemID(id) {
			t.Errorf("%s should be valid", id)
		}
	}
	for _, id := range []string{"", "PA-1", "PA-001", "PA-00001", "PA-01000", "PA-1234567890", "pa-0001", "EP-0001", "PA-0001 ", "PA-0001.1"} {
		if ValidItemID(id) {
			t.Errorf("%q should not be valid", id)
		}
	}
}
