package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ClearedStack/trawl/internal/fetch"
	"github.com/ClearedStack/trawl/internal/golden"
	"github.com/ClearedStack/trawl/internal/muster"
	"github.com/ClearedStack/trawl/internal/spool"
	"github.com/ClearedStack/trawl/internal/tool"
	"github.com/ClearedStack/trawl/internal/worker"
)

// TestRegistryMatchesComponent checks registry.yaml against the naming
// registry entry in component.json: the same sub-tools, in the same order.
func TestRegistryMatchesComponent(t *testing.T) {
	data, err := os.ReadFile("../component.json")
	if err != nil {
		t.Fatal(err)
	}
	var component struct {
		Slug     string   `json:"slug"`
		SubTools []string `json:"subTools"`
	}
	if err := json.Unmarshal(data, &component); err != nil {
		t.Fatal(err)
	}
	if component.Slug != muster.Worker {
		t.Errorf("component is %q, the worker leases as %q", component.Slug, muster.Worker)
	}
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(set.Names(), component.SubTools) {
		t.Errorf("registry.yaml lists %v, component.json lists %v", set.Names(), component.SubTools)
	}
}

// TestEverySubToolHasItsThreeParts checks each registered folder for a
// matching rule, logic and fixtures, and that no folder is unregistered.
func TestEverySubToolHasItsThreeParts(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, e := range set.Entries() {
		name := e.Rule.Tool
		registered[name] = true
		if e.Tool.Name() != name {
			t.Errorf("%s: logic is named %q", name, e.Tool.Name())
		}
		if _, err := os.Stat(filepath.Join(name, "match.yaml")); err != nil {
			t.Errorf("%s has no match.yaml", name)
		}
		if goFiles, _ := filepath.Glob(filepath.Join(name, "*.go")); len(goFiles) < 2 {
			t.Errorf("%s needs logic and a test", name)
		}
		if _, err := os.Stat(filepath.Join(name, "testdata", "match.json")); err != nil {
			t.Errorf("%s has no matching fixtures", name)
		}
		if wants, _ := filepath.Glob(filepath.Join(name, "testdata", "*", "want.json")); len(wants) == 0 {
			t.Errorf("%s has no fixture cases", name)
		}
	}
	dirs, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if d.IsDir() && d.Name() != "testdata" && !registered[d.Name()] {
			t.Errorf("folder %s is not in registry.yaml", d.Name())
		}
	}
}

// TestRulesDoNotOverlap checks that no envelope used in any sub-tool's
// fixtures is matched by more than one rule.
func TestRulesDoNotOverlap(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range set.Entries() {
		for _, format := range e.Rule.Match.Formats {
			got, err := set.Match(e.Rule.Match.Source, format)
			if err != nil || got.Rule.Tool != e.Rule.Tool {
				t.Errorf("%s %s: matched %q, %v", e.Rule.Match.Source, format, got.Rule.Tool, err)
			}
		}
	}
	if _, err := set.Match("made-up-source", "made-up.v1"); err == nil {
		t.Error("an unknown source matched a rule")
	}
}

// TestInputModes pins how each sub-tool's documents arrive: the three
// public sources are read from an address on Trawl's own schedule, and a
// form submission arrives inline with a leased item.
func TestInputModes(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"sam-gov": tool.Fetch, "github-issues": tool.Fetch, "web-sweep": tool.Fetch, "site-form": tool.Inline}
	for _, e := range set.Entries() {
		if e.Rule.Input != want[e.Rule.Tool] {
			t.Errorf("%s input is %s, want %s", e.Rule.Tool, e.Rule.Input, want[e.Rule.Tool])
		}
	}
}

// madeUpWeb serves fixture pages by host name without a network.
type madeUpWeb map[string]http.Handler

func (w madeUpWeb) RoundTrip(req *http.Request) (*http.Response, error) {
	handler, ok := w[req.URL.Host]
	if !ok {
		return nil, fmt.Errorf("no such host")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func body(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

const (
	trawlToken = "made-up-token-for-trawl"
	connToken  = "made-up-token-for-conn"
)

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

// sources is the sources file for the pipeline run: one address for each
// of the three public sources, some that fail, and one no sub-tool knows.
var sources = []worker.Source{
	{Source: "sam-gov", Format: "sam-gov.opportunities.v2", Fetch: "https://feed.agency-example.gov/opportunities/v2/search?limit=10"},
	{Source: "github-issues", Format: "github-issues.v1", Fetch: "https://code.agency-example.gov/api/issues?labels=suggestion"},
	{Source: "web-sweep", Format: "web-sweep.page.v1", Fetch: "https://www.agency-example.gov/small-business/resources"},
	{Source: "web-sweep", Format: "web-sweep.page.v1", Fetch: "https://www.agency-example.gov/private/drafts"},
	{Source: "web-sweep", Format: "web-sweep.page.v1", Fetch: "https://www.agency-example.gov/downloads/readiness.pdf"},
	{Source: "web-sweep", Format: "web-sweep.page.v1", Fetch: "https://slow.agency-example.gov/tools"},
	{Source: "made-up-feed", Format: "made-up.v1", Fetch: "https://feed.agency-example.gov/made-up"},
	{Source: "site-form", Format: "site-form.v1", Fetch: "https://www.agency-example.gov/form.json"},
}

// madeUpSites serves the sub-tools' own fixture documents.
func madeUpSites(t *testing.T) *fetch.Fetcher {
	page := func(contentType, path string) http.HandlerFunc {
		data := body(t, path)
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.Write(data)
		}
	}
	www := http.NewServeMux()
	www.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n")
	})
	www.HandleFunc("/small-business/resources", page("text/html; charset=utf-8", "web-sweep/testdata/resource-page/body.html"))
	www.HandleFunc("/private/drafts", page("text/html", "web-sweep/testdata/resource-page/body.html"))
	www.HandleFunc("/downloads/readiness.pdf", page("application/pdf", "web-sweep/testdata/not-html/body.pdf"))
	feed := http.NewServeMux()
	feed.HandleFunc("/opportunities/v2/search", page("application/json", "sam-gov/testdata/page-of-notices/body.json"))
	code := http.NewServeMux()
	code.HandleFunc("/api/issues", page("application/json", "github-issues/testdata/list-mixed/body.json"))
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	})
	return fetch.NewWithTransport(madeUpWeb{
		"www.agency-example.gov":  www,
		"feed.agency-example.gov": feed,
		"code.agency-example.gov": code,
		"slow.agency-example.gov": slow,
	}, time.Second)
}

func runPipeline(t *testing.T) []byte {
	hub := muster.NewFake(map[string]string{trawlToken: "trawl", connToken: "conn"})
	srv := httptest.NewServer(hub)
	defer srv.Close()

	// Conn files candidates from the site's form, using the site-form
	// fixtures, each with a visitor's proof of work.
	hub.SetPowBits(4)
	for _, name := range []string{
		"candidate-address-already-clean",
		"candidate",
		"candidate-markup-stripped",
		"candidate-address-not-public",
	} {
		var submission struct {
			Title   string `json:"title"`
			Address string `json:"address"`
		}
		if err := json.Unmarshal(body(t, "site-form/testdata/"+name+"/body.json"), &submission); err != nil {
			t.Fatal(err)
		}
		filing, _ := json.Marshal(map[string]string{
			"contract": "v1", "source": muster.SourceSiteForm, "title": submission.Title, "address": submission.Address,
			"pow": hub.SolvePow(muster.SourceSiteForm, submission.Title, submission.Address), "visitor": "203.0.113.7",
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/items", bytes.NewReader(filing))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+connToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("conn filing %s: status %d", name, resp.StatusCode)
		}
	}

	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sp, err := spool.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := &worker.Worker{
		Hub:            hubClient(t, srv.URL, trawlToken),
		Spool:          sp,
		Tools:          set,
		Fetcher:        madeUpSites(t),
		Sources:        sources,
		Poll:           time.Millisecond,
		Backoff:        time.Millisecond,
		HeartbeatEvery: time.Hour,
	}
	if err := w.Run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("%d entries left in the spool", len(left))
	}
	for _, call := range hub.Calls() {
		if call.Status >= 400 {
			t.Errorf("the hub refused %s with %d: %s", call.Path, call.Status, call.Body)
		}
	}
	data, err := golden.Marshal(hub.Items())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestPipelineGolden runs every sub-tool through the whole worker against
// the fake hub and compares all the hub ends up holding with a saved
// file. Two runs must agree byte for byte.
func TestPipelineGolden(t *testing.T) {
	first, second := runPipeline(t), runPipeline(t)
	if string(first) != string(second) {
		t.Fatal("two runs over the same batch differ")
	}
	golden.Bytes(t, "testdata/pipeline.golden.json", first)
}
