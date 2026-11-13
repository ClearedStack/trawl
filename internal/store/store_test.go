package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ClearedStack/trawl/internal/dedupe"
)

func record(seq int, url string) dedupe.Record {
	return dedupe.Record{ID: dedupe.FormatID(seq), SHA256: dedupe.Hash(url), URL: url}
}

func TestPutAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.LastSeq() != 0 {
		t.Fatal("new store is not empty")
	}
	a := record(1, "https://tools.agency-example.gov/wage")
	b := record(2, "https://calc.depot-example.mil/rates")
	for _, r := range []dedupe.Record{a, b} {
		if err := s.Put(r); err != nil {
			t.Fatal(err)
		}
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.LastSeq() != 2 {
		t.Fatalf("LastSeq = %d", again.LastSeq())
	}
	if id, ok := again.Lookup(b.SHA256); !ok || id != "PA-0002" {
		t.Fatalf("Lookup = %q, %v", id, ok)
	}
	if _, ok := again.Lookup(dedupe.Hash("https://example.org/")); ok {
		t.Fatal("unknown hash found")
	}
}

func TestPutRefusesBadRecords(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "seen"))
	if err != nil {
		t.Fatal(err)
	}
	first := record(1, "https://tools.agency-example.gov/wage")
	if err := s.Put(first); err != nil {
		t.Fatal(err)
	}
	bad := []dedupe.Record{
		record(3, "https://example.org/skipped-a-number"),
		record(1, "https://example.org/reused-a-number"),
		{ID: "PA-0002", SHA256: "not-the-hash", URL: "https://example.org/"},
		{ID: "PA-0002", SHA256: first.SHA256, URL: first.URL},
	}
	for _, r := range bad {
		if err := s.Put(r); !errors.Is(err, ErrCorrupt) {
			t.Errorf("Put(%+v) = %v", r, err)
		}
	}
}

func TestOpenRefusesDamagedFile(t *testing.T) {
	good := record(1, "https://tools.agency-example.gov/wage")
	lines := map[string]string{
		"two fields":   "PA-0001 " + good.SHA256 + "\n",
		"wrong hash":   "PA-0001 " + dedupe.Hash("x") + " " + good.URL + "\n",
		"wrong number": "PA-0002 " + good.SHA256 + " " + good.URL + "\n",
		"repeated":     "PA-0001 " + good.SHA256 + " " + good.URL + "\nPA-0002 " + good.SHA256 + " " + good.URL + "\n",
	}
	for name, content := range lines {
		path := filepath.Join(t.TempDir(), "seen")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: Open = %v", name, err)
		}
	}
}
