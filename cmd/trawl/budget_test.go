package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ClearedStack/trawl/internal/muster"
	"github.com/ClearedStack/trawl/internal/worker"
)

// budgetKiB is the memory Trawl may use: 30 MiB.
const budgetKiB = 30 * 1024

// TestWorkStaysWithinMemoryBudget builds the real binary, lets it record a
// large batch of discovered items from the fake hub over HTTP, and checks
// the most memory the process ever held against the 30 MiB budget.
func TestWorkStaysWithinMemoryBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	bin := filepath.Join(t.TempDir(), "trawl")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	const token = "made-up-token-for-trawl"
	hub := muster.NewFake(map[string]string{token: "trawl"})
	const items = 2000
	for i := 0; i < items; i++ {
		address := fmt.Sprintf("https://tools.agency-example.gov/batch/%d", i)
		rec, _ := json.Marshal(worker.Record{Source: "web-sweep", Address: address, Title: fmt.Sprintf("Made-up tool %d", i), Kind: "link", Via: "https://www.agency-example.gov/resources"})
		hub.Seed(muster.FakeItem{State: muster.StateDiscovered, Next: "trawl", Lane: "backfill", Source: "web-sweep", Title: fmt.Sprintf("Made-up tool %d", i), Address: address, Results: map[string]json.RawMessage{"trawl": rec}})
	}
	// One candidate from the site's form, as the hub records it.
	hub.Seed(muster.FakeItem{State: muster.StateDiscovered, Next: "trawl", Source: "site-form", Title: "Wage calculator", Address: "https://tools.agency-example.gov/wage"})

	srv := httptest.NewServer(hub)
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "work", "-muster-url", srv.URL, "-token-file", tokenFile, "-spool", filepath.Join(t.TempDir(), "spool"), "-drain")
	// A directory the process has no business writing to, with no home
	// and no temporary directory: the spool is the only place it needs.
	cmd.Dir = "/"
	cmd.Env = []string{"HOME=/nonexistent", "TMPDIR=/nonexistent"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("trawl work failed: %v\n%s", err, out)
	}

	recorded := 0
	for _, it := range hub.Items() {
		if it.State != muster.StateRecorded || it.Next != "sextant" {
			t.Fatalf("%s ended as %s", it.ID, it.State)
		}
		recorded++
	}
	if recorded != items+1 {
		t.Errorf("%d items recorded, want %d", recorded, items+1)
	}
	for _, call := range hub.Calls() {
		if call.Status >= 400 {
			t.Fatalf("the hub refused %s with %d: %s", call.Path, call.Status, call.Body)
		}
	}

	// The binary reports its own peak when it stops. The figure the
	// operating system gives the parent for a child is not used: it can
	// include the memory of this test process.
	peakKiB := 0
	for _, line := range strings.Split(string(out), "\n") {
		var entry struct {
			Msg  string `json:"msg"`
			Peak int    `json:"peak_kib"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "stopped" {
			peakKiB = entry.Peak
		}
	}
	if peakKiB == 0 {
		t.Skip("peak memory is not reported on this system")
	}
	t.Logf("peak memory %d KiB of %d KiB allowed, %d items recorded", peakKiB, budgetKiB, recorded)
	if peakKiB > budgetKiB {
		t.Errorf("peak memory %d KiB is over the %d KiB budget", peakKiB, budgetKiB)
	}
}

func TestWorkRefusesBadSettings(t *testing.T) {
	sources := filepath.Join(t.TempDir(), "sources.json")
	os.WriteFile(sources, []byte(`{"sources": [{"source": "web-sweep", "format": "web-sweep.page.v1", "fetch": "http://localhost/"}]}`), 0o600)
	cases := map[string][]string{
		"no hub address":        {"work"},
		"hub without a scheme":  {"work", "-muster-url", "hub.made-up.svc:8080", "-spool", t.TempDir()},
		"stray argument":        {"work", "-muster-url", "http://hub.made-up.svc", "extra"},
		"sources file missing":  {"work", "-muster-url", "http://hub.made-up.svc", "-sources", filepath.Join(t.TempDir(), "absent.json")},
		"source not public":     {"work", "-muster-url", "http://hub.made-up.svc", "-sources", sources},
		"negative sweep period": {"work", "-muster-url", "http://hub.made-up.svc", "-sweep-every", "-1h"},
	}
	for name, args := range cases {
		var out, errOut strings.Builder
		if status := run(args, strings.NewReader(""), &out, &errOut); status != 2 {
			t.Errorf("%s: exit status %d", name, status)
		}
	}
}
