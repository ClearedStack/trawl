package muster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ClearedStack/trawl/internal/dedupe"
	"github.com/ClearedStack/trawl/internal/errs"
)

var ctx = context.Background()

func rec(seq int, url string) dedupe.Record {
	return dedupe.Record{ID: dedupe.FormatID(seq), SHA256: dedupe.Hash(url), URL: url}
}

func item(id string) Item {
	return Item{ItemID: id, Source: "made-up", Format: "made-up.v1", Payload: []byte(`{"n":1}`), ObservedAt: "2031-03-04T05:06:07Z"}
}

// clients runs a test against the stand-in directly and again through the
// HTTP client and handler, so both follow the same contract.
func clients(t *testing.T, fn func(t *testing.T, c Client, m *Memory)) {
	t.Run("memory", func(t *testing.T) {
		m := NewMemory()
		fn(t, m, m)
	})
	t.Run("http", func(t *testing.T) {
		m := NewMemory()
		const token = "made-up-token"
		srv := httptest.NewServer(m.Handler(token))
		defer srv.Close()
		tokenFile := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := NewHTTP(srv.URL, tokenFile, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		fn(t, c, m)
	})
}

func TestLeaseAndComplete(t *testing.T) {
	clients(t, func(t *testing.T, c Client, m *Memory) {
		if got, err := c.Lease(ctx, Normal); got != nil || err != nil {
			t.Fatalf("empty lane: %v, %v", got, err)
		}
		m.Enqueue(Normal, item("item-1"))
		got, err := c.Lease(ctx, Normal)
		if err != nil || got == nil {
			t.Fatalf("Lease: %v, %v", got, err)
		}
		if got.ItemID != "item-1" || got.LeaseID == "" || got.Lane != Normal || string(got.Payload) != `{"n":1}` {
			t.Errorf("leased item = %+v", got)
		}
		if err := c.Heartbeat(ctx, got.LeaseID); err != nil {
			t.Errorf("Heartbeat: %v", err)
		}

		a := rec(1, "https://tools.agency-example.gov/wage")
		b := rec(2, "https://calc.depot-example.mil/rates")
		err = c.Complete(ctx, Completion{LeaseID: got.LeaseID, Tool: "made-up", ExpectLastSeq: 0, Records: []dedupe.Record{a, b}})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if err := c.Heartbeat(ctx, got.LeaseID); !errors.Is(err, ErrGone) {
			t.Errorf("Heartbeat after complete: %v", err)
		}
		if err := c.Complete(ctx, Completion{LeaseID: got.LeaseID}); !errors.Is(err, ErrGone) {
			t.Errorf("second Complete: %v", err)
		}

		res, err := c.Lookup(ctx, []string{a.SHA256, dedupe.Hash("https://example.org/")})
		if err != nil {
			t.Fatal(err)
		}
		if res.LastSeq != 2 || len(res.Known) != 1 || res.Known[a.SHA256] != "PA-0001" {
			t.Errorf("Lookup = %+v", res)
		}

		snap := m.Snapshot()
		if strings.Join(snap.Sextant, ",") != "PA-0001,PA-0002" {
			t.Errorf("queued for the next stage: %v", snap.Sextant)
		}
		if snap.Items[0].State != StateCompleted {
			t.Errorf("item state %s", snap.Items[0].State)
		}
	})
}

func TestCompleteRefusals(t *testing.T) {
	clients(t, func(t *testing.T, c Client, m *Memory) {
		first := rec(1, "https://tools.agency-example.gov/wage")
		m.Enqueue(Normal, item("item-1"))
		m.Enqueue(Normal, item("item-2"))
		one, _ := c.Lease(ctx, Normal)
		if err := c.Complete(ctx, Completion{LeaseID: one.LeaseID, Records: []dedupe.Record{first}}); err != nil {
			t.Fatal(err)
		}
		two, _ := c.Lease(ctx, Normal)

		cases := []struct {
			name string
			c    Completion
			want error
		}{
			{"stale sequence", Completion{ExpectLastSeq: 0, Records: []dedupe.Record{rec(1, "https://example.org/a")}}, ErrConflict},
			{"hash recorded meanwhile", Completion{ExpectLastSeq: 1, Records: []dedupe.Record{rec(2, first.URL)}}, ErrConflict},
			{"skipped identifier", Completion{ExpectLastSeq: 1, Records: []dedupe.Record{rec(3, "https://example.org/a")}}, ErrProtocol},
			{"hash does not match address", Completion{ExpectLastSeq: 1, Records: []dedupe.Record{{ID: "PA-0002", SHA256: first.SHA256, URL: "https://example.org/a"}}}, ErrProtocol},
			{"same address twice", Completion{ExpectLastSeq: 1, Records: []dedupe.Record{rec(2, "https://example.org/a"), rec(3, "https://example.org/a")}}, ErrProtocol},
			{"sighting of nothing", Completion{Sightings: []dedupe.Sighting{{ID: "PA-0009", SHA256: dedupe.Hash("https://example.org/a")}}}, ErrProtocol},
			{"sighting with wrong identifier", Completion{Sightings: []dedupe.Sighting{{ID: "PA-0002", SHA256: first.SHA256}}}, ErrProtocol},
			{"unknown reject code", Completion{Rejects: []Reject{{Code: "E_OTHER", Fingerprint: "0123456789ab"}}}, ErrProtocol},
			{"unknown disposition", Completion{Disposition: "made_up"}, ErrProtocol},
		}
		for _, tc := range cases {
			tc.c.LeaseID = two.LeaseID
			if err := c.Complete(ctx, tc.c); !errors.Is(err, tc.want) {
				t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
			}
		}
		// Nothing above may have left a trace.
		snap := m.Snapshot()
		if len(snap.Records) != 1 || len(snap.Sightings) != 0 || snap.Items[1].State != StateLeased {
			t.Errorf("refused completions changed state: %+v", snap)
		}

		// A sighting is stored once per item and address.
		s := dedupe.Sighting{ID: "PA-0001", SHA256: first.SHA256, Via: "made-up"}
		if err := c.Complete(ctx, Completion{LeaseID: two.LeaseID, Sightings: []dedupe.Sighting{s, s}}); err != nil {
			t.Fatal(err)
		}
		if got := m.Snapshot().Sightings; len(got) != 1 || got[0].ItemID != "item-2" {
			t.Errorf("sightings = %+v", got)
		}
	})
}

func TestFailAndNeedsSubtool(t *testing.T) {
	clients(t, func(t *testing.T, c Client, m *Memory) {
		for _, id := range []string{"times-out", "broken", "unfamiliar"} {
			m.Enqueue(Normal, item(id))
		}

		a, _ := c.Lease(ctx, Normal)
		if err := c.Fail(ctx, Failure{LeaseID: a.LeaseID, Code: errs.Timeout, Fingerprint: "0123456789ab"}); err != nil {
			t.Fatal(err)
		}
		b, _ := c.Lease(ctx, Normal)
		if err := c.Fail(ctx, Failure{LeaseID: b.LeaseID, Code: errs.NeedsSubtool}); !errors.Is(err, ErrProtocol) {
			t.Errorf("Fail with E_NEEDS_SUBTOOL: %v", err)
		}
		if err := c.Fail(ctx, Failure{LeaseID: b.LeaseID, Code: errs.Parse, Reason: "bad_json", Fingerprint: "0123456789ab"}); err != nil {
			t.Fatal(err)
		}
		u, _ := c.Lease(ctx, Normal)
		if err := c.NeedsSubtool(ctx, Failure{LeaseID: u.LeaseID, Code: errs.Parse}); !errors.Is(err, ErrProtocol) {
			t.Errorf("NeedsSubtool with E_PARSE: %v", err)
		}
		if err := c.NeedsSubtool(ctx, Failure{LeaseID: u.LeaseID, Code: errs.NeedsSubtool, Reason: "no_matching_rule", Fingerprint: "0123456789ab"}); err != nil {
			t.Fatal(err)
		}

		states := map[string]string{}
		for _, it := range m.Snapshot().Items {
			states[it.ItemID] = it.State
		}
		want := map[string]string{"times-out": StateQueued, "broken": StateFailed, "unfamiliar": StateNeedsSubtool}
		for id, state := range want {
			if states[id] != state {
				t.Errorf("%s is %s, want %s", id, states[id], state)
			}
		}

		// The timed-out item comes round again; the broken one does not.
		again, _ := c.Lease(ctx, Normal)
		if again == nil || again.ItemID != "times-out" {
			t.Fatalf("retry lease = %+v", again)
		}
		if next, _ := c.Lease(ctx, Normal); next != nil {
			t.Errorf("unexpected item %s", next.ItemID)
		}

		// Replay brings back every paused item.
		if n := m.Replay(); n != 1 {
			t.Errorf("Replay = %d", n)
		}
		replayed, _ := c.Lease(ctx, Normal)
		if replayed == nil || replayed.ItemID != "unfamiliar" {
			t.Fatalf("replayed lease = %+v", replayed)
		}
	})
}

func TestRate(t *testing.T) {
	clients(t, func(t *testing.T, c Client, m *Memory) {
		steps := []struct {
			key, member string
			want        int
		}{
			{"k1", "item-1", 1},
			{"k1", "item-2", 2},
			{"k1", "item-1", 2},
			{"k2", "item-1", 1},
		}
		for _, s := range steps {
			got, err := c.Rate(ctx, s.key, s.member)
			if err != nil || got != s.want {
				t.Errorf("Rate(%s, %s) = %d, %v; want %d", s.key, s.member, got, err, s.want)
			}
		}
		if _, err := c.Rate(ctx, "", "x"); !errors.Is(err, ErrProtocol) {
			t.Errorf("empty key: %v", err)
		}
	})
}

func TestUnavailable(t *testing.T) {
	clients(t, func(t *testing.T, c Client, m *Memory) {
		m.Enqueue(Urgent, item("item-1"))
		m.SetAvailable(false)
		if _, err := c.Lease(ctx, Urgent); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Lease: %v", err)
		}
		if _, err := c.Lookup(ctx, nil); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Lookup: %v", err)
		}
		if err := c.Complete(ctx, Completion{LeaseID: "x"}); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Complete: %v", err)
		}
		m.SetAvailable(true)
		if got, err := c.Lease(ctx, Urgent); err != nil || got == nil {
			t.Errorf("Lease after recovery: %v, %v", got, err)
		}
	})
}

func TestExpireLeases(t *testing.T) {
	m := NewMemory()
	m.Enqueue(Normal, item("item-1"))
	m.Enqueue(Normal, item("item-2"))
	first, _ := m.Lease(ctx, Normal)
	m.ExpireLeases()
	if err := m.Heartbeat(ctx, first.LeaseID); !errors.Is(err, ErrGone) {
		t.Errorf("Heartbeat on expired lease: %v", err)
	}
	again, _ := m.Lease(ctx, Normal)
	if again.ItemID != "item-1" || again.LeaseID == first.LeaseID {
		t.Errorf("expired item was not leased again first: %+v", again)
	}
}

func TestUnknownLane(t *testing.T) {
	clients(t, func(t *testing.T, c Client, m *Memory) {
		if _, err := c.Lease(ctx, Lane("made-up")); !errors.Is(err, ErrProtocol) {
			t.Errorf("unknown lane: %v", err)
		}
	})
}

func TestHTTPNeedsTokenAndVersion(t *testing.T) {
	m := NewMemory()
	srv := httptest.NewServer(m.Handler("made-up-token"))
	defer srv.Close()

	noToken, _ := NewHTTP(srv.URL, "", time.Second)
	if _, err := noToken.Lease(ctx, Normal); !errors.Is(err, ErrProtocol) {
		t.Errorf("call without a token: %v", err)
	}
	missing, _ := NewHTTP(srv.URL, filepath.Join(t.TempDir(), "absent"), time.Second)
	if _, err := missing.Lease(ctx, Normal); !errors.Is(err, ErrUnavailable) {
		t.Errorf("call with an unreadable token file: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+pathLease, strings.NewReader(`{"worker":"trawl","lane":"normal"}`))
	req.Header.Set("Authorization", "Bearer made-up-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("call without the contract version: status %d", resp.StatusCode)
	}
}

// TestHTTPErrorsNameNothing checks that a failed call reports no address.
func TestHTTPErrorsNameNothing(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	address := srv.URL
	srv.Close()
	c, err := NewHTTP(address, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Lease(ctx, Normal)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Lease against a closed port: %v", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), strings.TrimPrefix(address, "http://")) {
		t.Errorf("error names the address: %v", err)
	}
	if _, err := NewHTTP("hub.made-up.svc:8080", "", time.Second); err == nil {
		t.Error("address without a scheme accepted")
	}
}

func TestLanesOrder(t *testing.T) {
	if len(Lanes) != 3 || Lanes[0] != Urgent || Lanes[1] != Normal || Lanes[2] != Backfill {
		t.Errorf("Lanes = %v", Lanes)
	}
}
