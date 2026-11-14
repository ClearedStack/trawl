package muster

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ClearedStack/trawl/internal/errs"
)

// example is one of Muster's saved contract examples.
type example struct {
	About string `json:"about"`
	Given struct {
		Items     []FakeItem      `json:"items"`
		Calls     []exampleCall   `json:"calls"`
		Wait      string          `json:"wait"`
		Condition json.RawMessage `json:"condition"`
	} `json:"given"`
	Request exampleCall `json:"request"`
	Expect  struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
		Stream json.RawMessage `json:"stream"`
	} `json:"expect"`
}

type exampleCall struct {
	As          string          `json:"as"`
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	Body        json.RawMessage `json:"body"`
	Raw         *string         `json:"raw"`
	ContentType *string         `json:"content_type"`
}

func loadExamples(t *testing.T) map[string]example {
	t.Helper()
	files, err := filepath.Glob("testdata/contract-v1/examples/*.json")
	if err != nil || len(files) == 0 {
		t.Fatal("no contract examples")
	}
	out := map[string]example{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var ex example
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ex); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		out[strings.TrimSuffix(filepath.Base(file), ".json")] = ex
	}
	return out
}

func compact(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func sortedNames(m map[string]example) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// notCovered lists the examples the fake hub does not replay, each with
// the reason. Everything else must pass exactly.
var notCovered = map[string]string{
	"release":                              "releasing a held item is Countersign's call and is not in the fake",
	"lease-incident":                       "incident steps are not in the fake",
	"complete-proposal":                    "edit proposals are not in the fake",
	"lease-proposal":                       "edit proposals are not in the fake",
	"lease-proposal-results":               "edit proposals are not in the fake",
	"edits":                                "edit proposals are not in the fake",
	"edits-honeypot":                       "edit proposals are not in the fake",
	"refused-400-bad_field-nothing-left":   "edit proposals are not in the fake",
	"refused-400-bad_field-proof-of-work":  "edit proposals are not in the fake",
	"refused-400-bad_field-unknown-target": "edit proposals are not in the fake",
	"refused-413-too_large":                "edit proposals are not in the fake",
	"refused-429-rate_limited":             "edit proposals are not in the fake",
	"health":                               "health reports are not in the fake",
	"health-replay":                        "health reports are not in the fake",
	"health-proposal-live":                 "health reports are not in the fake",
	"refused-409-duplicate":                "health reports are not in the fake",
	"refused-422-unknown_value-health":     "health reports are not in the fake",
	"feed":                                 "the live feed is not in the fake",
	"healthz":                              "the cluster's own check is not in the fake",
	"refused-500-bad_record":               "needs a damaged stored record, which no request can cause",
	"refused-500-unexpected":               "needs a fault inside the hub, which no request can cause",
	"refused-503-busy":                     "needs a hub under load, which no request can cause",
	"refused-503-busy-identity":            "needs a slow identity check, which no request can cause",
	"refused-503-store_slow":               "needs a slow store, which no request can cause",
}

func tokenFor(as string) string { return "made-up-token-for-" + as }

func allCallers() map[string]string {
	callers := map[string]string{}
	for _, name := range []string{"trawl", "sextant", "assay", "countersign", "waybill", "moldloft", "shakedown", "chartroom", "tidegauge", "bellbuoy", "conn", "shipkeeper"} {
		callers[tokenFor(name)] = name
	}
	return callers
}

func send(t *testing.T, base string, call exampleCall) (int, []byte) {
	t.Helper()
	var body io.Reader
	switch {
	case call.Raw != nil:
		body = strings.NewReader(*call.Raw)
	case call.Body != nil:
		body = bytes.NewReader(call.Body)
	}
	req, err := http.NewRequest(call.Method, base+call.Path, body)
	if err != nil {
		t.Fatal(err)
	}
	contentType := "application/json"
	if call.ContentType != nil {
		contentType = *call.ContentType
	}
	req.Header.Set("Content-Type", contentType)
	if call.As != "" {
		req.Header.Set("Authorization", "Bearer "+tokenFor(call.As))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// TestContractExamplesAgainstFake replays Muster's saved examples against
// the fake hub and compares the status and the body, field for field and
// in order.
func TestContractExamplesAgainstFake(t *testing.T) {
	examples := loadExamples(t)
	for name := range notCovered {
		if _, ok := examples[name]; !ok {
			t.Errorf("notCovered lists %q, which is not among the examples", name)
		}
	}
	replayed := 0
	for _, name := range sortedNames(examples) {
		ex := examples[name]
		if _, skip := notCovered[name]; skip {
			continue
		}
		t.Run(name, func(t *testing.T) {
			fake := NewFake(allCallers())
			srv := httptest.NewServer(fake)
			defer srv.Close()
			for _, it := range ex.Given.Items {
				fake.Seed(it)
			}
			for i, call := range ex.Given.Calls {
				if status, body := send(t, srv.URL, call); status >= 300 {
					t.Fatalf("given call %d failed with %d: %s", i, status, body)
				}
			}
			if ex.Given.Wait != "" {
				wait, err := time.ParseDuration(ex.Given.Wait)
				if err != nil {
					t.Fatal(err)
				}
				fake.Advance(wait)
			}
			if ex.Given.Condition != nil {
				t.Fatal("this example needs a condition; list it in notCovered with the reason")
			}
			status, body := send(t, srv.URL, ex.Request)
			if status != ex.Expect.Status {
				t.Errorf("status %d, want %d; body %s", status, ex.Expect.Status, body)
			}
			if ex.Expect.Body == nil {
				if len(bytes.TrimSpace(body)) != 0 {
					t.Errorf("a body where none is expected: %s", body)
				}
				return
			}
			if got, want := compact(body), compact(ex.Expect.Body); got != want {
				t.Errorf("body\n got  %s\n want %s", got, want)
			}
		})
		replayed++
	}
	t.Logf("replayed %d of %d examples against the fake; %d not covered", replayed, len(examples), len(notCovered))
	if replayed < 20 {
		t.Errorf("only %d examples replayed", replayed)
	}
}

// answering is a transport that checks one request against a saved
// example and answers with the example's saved answer.
type answering struct {
	t      *testing.T
	ex     example
	called bool
}

func (a *answering) RoundTrip(req *http.Request) (*http.Response, error) {
	a.called = true
	body, _ := io.ReadAll(req.Body)
	if got, want := compact(body), compact(a.ex.Request.Body); got != want {
		a.t.Errorf("the client sent\n     %s\nthe example sends\n     %s", got, want)
	}
	if req.Method != a.ex.Request.Method || req.URL.Path != a.ex.Request.Path {
		a.t.Errorf("the client called %s %s", req.Method, req.URL.Path)
	}
	if req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Authorization") != "Bearer "+tokenFor("trawl") {
		a.t.Error("wrong content type or token")
	}
	return &http.Response{
		StatusCode: a.ex.Expect.Status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(a.ex.Expect.Body)),
	}, nil
}

// clientFields are the fields Trawl's client can put in each call.
var clientFields = map[string][]string{
	pathLease:        {"contract"},
	pathHeartbeat:    {"contract", "lease"},
	pathComplete:     {"contract", "lease", "state", "result"},
	pathFail:         {"contract", "lease", "code"},
	pathNeedsSubtool: {"contract", "lease"},
	pathItems:        {"contract", "source", "title", "address", "result"},
}

// sendable reports whether the client can produce the example's request:
// a POST of an object with the contract version this client speaks and
// only the fields the client has for that call, each of the right type.
func sendable(ex example) bool {
	fields, known := clientFields[ex.Request.Path]
	if !known || ex.Request.Method != http.MethodPost || ex.Request.Raw != nil || ex.Request.ContentType != nil || ex.Request.As == "" {
		return false
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(ex.Request.Body, &body) != nil {
		return false
	}
	if string(body["contract"]) != `"v1"` {
		return false
	}
	for key, value := range body {
		ok := false
		for _, f := range fields {
			ok = ok || f == key
		}
		if !ok {
			return false
		}
		if key != "result" {
			var text string
			if json.Unmarshal(value, &text) != nil {
				return false
			}
		}
	}
	// The client always names a lease, a state, a code, a source and an
	// address where the call has one, so it cannot send a body that
	// leaves one out.
	for _, f := range fields {
		if _, present := body[f]; !present && f != "result" && f != "title" {
			return false
		}
	}
	return true
}

// TestContractExamplesThroughClient drives Trawl's client with the
// arguments of each saved example it can produce, and checks the request
// it sends and the way it reads the answer.
func TestContractExamplesThroughClient(t *testing.T) {
	examples := loadExamples(t)
	replayed := map[string]int{}
	for _, name := range sortedNames(examples) {
		ex := examples[name]
		if !sendable(ex) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			rt := &answering{t: t, ex: ex}
			c := client(t, "http://hub.made-up.svc", tokenFor("trawl"))
			c.client.Transport = rt

			var body struct {
				Lease   string          `json:"lease"`
				State   string          `json:"state"`
				Code    errs.Code       `json:"code"`
				Result  json.RawMessage `json:"result"`
				Source  string          `json:"source"`
				Title   string          `json:"title"`
				Address string          `json:"address"`
			}
			if err := json.Unmarshal(ex.Request.Body, &body); err != nil {
				t.Fatal(err)
			}
			var result any
			if body.Result != nil {
				result = body.Result
			}

			var err error
			switch ex.Request.Path {
			case pathLease:
				var lease *Lease
				lease, err = c.Lease(ctx)
				switch {
				case ex.Expect.Status == http.StatusNoContent && (lease != nil || err != nil):
					t.Errorf("204 was read as %+v, %v", lease, err)
				case ex.Expect.Status == http.StatusOK:
					if lease == nil {
						t.Fatalf("no lease read: %v", err)
					}
					// Reading the answer and writing it again must lose
					// nothing the worker uses.
					var want Lease
					if jsonErr := json.Unmarshal(ex.Expect.Body, &want); jsonErr != nil {
						t.Fatal(jsonErr)
					}
					got, _ := json.Marshal(lease)
					wanted, _ := json.Marshal(want)
					if string(got) != string(wanted) || lease.Lease == "" || lease.ID == "" || lease.Kind == "" || lease.State == "" {
						t.Errorf("lease read as %s", got)
					}
				}
			case pathHeartbeat:
				err = c.Heartbeat(ctx, body.Lease)
			case pathComplete:
				err = c.Complete(ctx, body.Lease, body.State, result)
			case pathFail:
				err = c.Fail(ctx, body.Lease, body.Code)
			case pathNeedsSubtool:
				err = c.NeedsSubtool(ctx, body.Lease)
			case pathItems:
				var filed Filed
				filed, err = c.File(ctx, Filing{Source: body.Source, Title: body.Title, Address: body.Address, Result: result})
				var want struct {
					ID        string `json:"id"`
					Duplicate bool   `json:"duplicate"`
				}
				json.Unmarshal(ex.Expect.Body, &want)
				if err == nil && (filed.ID != want.ID || filed.New == want.Duplicate || filed.New != (ex.Expect.Status == http.StatusCreated)) {
					t.Errorf("filing read as %+v", filed)
				}
			}
			if !rt.called {
				t.Fatal("no request was made")
			}

			var refusal *Refusal
			switch status := ex.Expect.Status; {
			case status < 300:
				if err != nil {
					t.Errorf("a %d was read as %v", status, err)
				}
			case !errors.As(err, &refusal) || refusal.Status != status:
				t.Errorf("a %d was read as %v", status, err)
			default:
				var want struct {
					Error errs.Code `json:"error"`
					Cause string    `json:"cause"`
				}
				json.Unmarshal(ex.Expect.Body, &want)
				if refusal.Code != want.Error || refusal.Cause != want.Cause {
					t.Errorf("refusal read as %+v, want %+v", refusal, want)
				}
				kind := ErrRefused
				switch {
				case status == http.StatusConflict:
					kind = ErrConflict
				case status == 401, status == 403, status == 429, status >= 500:
					kind = ErrUnavailable
				}
				if !errors.Is(err, kind) {
					t.Errorf("a %d %s was not sorted as %v", status, want.Cause, kind)
				}
			}
		})
		replayed[ex.Request.Path]++
	}
	for path := range clientFields {
		if replayed[path] == 0 {
			t.Errorf("no example of %s was replayed through the client", path)
		}
	}
	t.Logf("replayed through the client, by call: %v", replayed)
}
