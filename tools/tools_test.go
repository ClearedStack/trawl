package tools

import (
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

const observed = "2031-03-04T05:06:07Z"

// pipeline queues a fixed batch that touches every sub-tool and every
// failure mode, using the sub-tools' own fixture documents.
func pipeline(t *testing.T, hub *muster.Memory) *fetch.Fetcher {
	inline := func(lane muster.Lane, id, source, format, path string) {
		hub.Enqueue(lane, muster.Item{ItemID: id, Source: source, Format: format, Payload: body(t, path), ObservedAt: observed})
	}
	fetched := func(lane muster.Lane, id, source, format, address string) {
		hub.Enqueue(lane, muster.Item{ItemID: id, Source: source, Format: format, Fetch: address, ObservedAt: observed})
	}

	inline(muster.Normal, "notices-1", "sam-gov", "sam-gov.opportunities.v2", "sam-gov/testdata/page-of-notices/body.json")
	inline(muster.Normal, "issues-1", "github-issues", "github-issues.v1", "github-issues/testdata/list-mixed/body.json")
	fetched(muster.Backfill, "sweep-1", "web-sweep", "web-sweep.page.v1", "https://www.agency-example.gov/small-business/resources")
	fetched(muster.Backfill, "sweep-2", "web-sweep", "web-sweep.page.v1", "https://www.agency-example.gov/private/drafts")
	fetched(muster.Backfill, "sweep-3", "web-sweep", "web-sweep.page.v1", "https://www.agency-example.gov/downloads/readiness.pdf")
	fetched(muster.Backfill, "sweep-4", "web-sweep", "web-sweep.page.v1", "https://slow.agency-example.gov/tools")
	fetched(muster.Backfill, "notices-2", "sam-gov", "sam-gov.opportunities.v2", "https://feed.agency-example.gov/opportunities/v2/search?limit=10")

	// Six submissions from one sender on one day: the sixth is over the
	// limit. They are urgent, so they are handled first, in this order.
	for i, name := range []string{"candidate", "candidate-markup-stripped", "candidate-no-title", "exactly-4096-bytes", "candidate-address-not-public", "candidate"} {
		inline(muster.Urgent, fmt.Sprintf("form-%d", i+1), "site-form", "site-form.v1", "site-form/testdata/"+name+"/body.json")
	}
	inline(muster.Urgent, "form-edit", "site-form", "site-form.v1", "site-form/testdata/edit-summary/body.json")
	inline(muster.Urgent, "form-spam", "site-form", "site-form.v1", "site-form/testdata/honeypot-filled/body.json")

	// An item from a source no sub-tool knows, a sweep item that wrongly
	// carries its page inline, and a form item that asks for a fetch.
	inline(muster.Normal, "unknown-1", "made-up-feed", "made-up.v1", "sam-gov/testdata/empty-page/body.json")
	inline(muster.Normal, "sweep-inline", "web-sweep", "web-sweep.page.v1", "sam-gov/testdata/empty-page/body.json")
	fetched(muster.Normal, "form-fetch", "site-form", "site-form.v1", "https://www.agency-example.gov/form.json")

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
	slowCalls := 0
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fails once, then answers: an upstream that recovers.
		slowCalls++
		if slowCalls == 1 {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/tools/late-checklist">Late checklist</a>`)
	})
	return fetch.NewWithTransport(madeUpWeb{
		"www.agency-example.gov":  www,
		"feed.agency-example.gov": feed,
		"slow.agency-example.gov": slow,
	}, time.Second)
}

func runPipeline(t *testing.T) []byte {
	hub := muster.NewMemory()
	fetcher := pipeline(t, hub)
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	sp, err := spool.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &worker.Worker{Hub: hub, Spool: sp, Tools: set, Fetcher: fetcher, Poll: time.Millisecond, Backoff: time.Millisecond, HeartbeatEvery: time.Hour}
	if err := w.Run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if left, _ := sp.Pending(); len(left) != 0 {
		t.Errorf("%d entries left in the spool", len(left))
	}
	data, err := golden.Marshal(hub.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestPipelineGolden runs every sub-tool through the whole worker against
// the stand-in hub and compares all the hub ends up holding with a saved
// file. Two runs must agree byte for byte.
func TestPipelineGolden(t *testing.T) {
	first, second := runPipeline(t), runPipeline(t)
	if string(first) != string(second) {
		t.Fatal("two runs over the same batch differ")
	}
	golden.Bytes(t, "testdata/pipeline.golden.json", first)
}
