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
)

// budgetKiB is the memory Trawl may use: 30 MiB.
const budgetKiB = 30 * 1024

// TestWorkStaysWithinMemoryBudget builds the real binary, lets it drain a
// large batch from the stand-in hub over HTTP, and checks the most memory
// the process ever held against the 30 MiB budget. The batch includes
// inline documents close to the largest size accepted.
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

	hub := muster.NewMemory()
	const observed = "2031-03-04T05:06:07Z"
	wantRecords := 0

	// Many small items: lists of made-up issue suggestions.
	for i := 0; i < 150; i++ {
		var issues []map[string]any
		for j := 0; j < 20; j++ {
			issues = append(issues, map[string]any{
				"number": i*100 + j + 1,
				"labels": []map[string]string{{"name": "tool-suggestion"}},
				"body":   fmt.Sprintf("### Tool address\n\nhttps://tools.agency-example.gov/batch/%d/%d\n\n### Tool name\n\nMade-up tool %d-%d\n", i, j, i, j),
			})
			wantRecords++
		}
		payload, _ := json.Marshal(issues)
		hub.Enqueue(muster.Normal, muster.Item{ItemID: fmt.Sprintf("issues-%d", i), Source: "github-issues", Format: "github-issues.v1", Payload: payload, ObservedAt: observed})
	}

	// A few items near the largest inline size: pages of made-up notices.
	filler := strings.Repeat("Made-up description text. ", 60)
	for i := 0; i < 6; i++ {
		var notices []map[string]any
		for j := 0; j < 120; j++ {
			var links []string
			for k := 0; k < 6; k++ {
				links = append(links, fmt.Sprintf("https://notices.agency-example.gov/files/%d/%d/%d.pdf", i, j, k))
				wantRecords++
			}
			notices = append(notices, map[string]any{
				"noticeId":      fmt.Sprintf("%032x", i*1000+j),
				"title":         "Made-up notice",
				"description":   filler,
				"resourceLinks": links,
			})
		}
		payload, _ := json.Marshal(map[string]any{"totalRecords": len(notices), "opportunitiesData": notices})
		if len(payload) < 200<<10 || len(payload) > 256<<10 {
			t.Fatalf("large item is %d bytes; it should sit just under the inline limit", len(payload))
		}
		hub.Enqueue(muster.Backfill, muster.Item{ItemID: fmt.Sprintf("notices-%d", i), Source: "sam-gov", Format: "sam-gov.opportunities.v2", Payload: payload, ObservedAt: observed})
	}

	srv := httptest.NewServer(hub.Handler(""))
	defer srv.Close()

	cmd := exec.Command(bin, "work", "-muster-url", srv.URL, "-token-file", "", "-spool", filepath.Join(t.TempDir(), "spool"), "-drain")
	// A directory the process cannot write to, to show that the spool
	// is the only place it needs.
	cmd.Dir = "/"
	cmd.Env = []string{"HOME=/nonexistent", "TMPDIR=/nonexistent"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("trawl work failed: %v\n%s", err, out)
	}

	snap := hub.Snapshot()
	if len(snap.Records) != wantRecords {
		t.Errorf("%d records, want %d", len(snap.Records), wantRecords)
	}
	for _, it := range snap.Items {
		if it.State != muster.StateCompleted {
			t.Errorf("%s ended as %s", it.ItemID, it.State)
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
	t.Logf("peak memory %d KiB of %d KiB allowed, %d records", peakKiB, budgetKiB, len(snap.Records))
	if peakKiB > budgetKiB {
		t.Errorf("peak memory %d KiB is over the %d KiB budget", peakKiB, budgetKiB)
	}
}
