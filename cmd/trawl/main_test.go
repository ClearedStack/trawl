package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ClearedStack/trawl/internal/golden"
)

func TestNormalizeGolden(t *testing.T) {
	in, err := os.ReadFile("testdata/normalize.stdin")
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "seen")
	var out, errOut bytes.Buffer
	status := run([]string{"normalize", "-store", storePath}, bytes.NewReader(in), &out, &errOut)
	if status != 1 {
		t.Errorf("exit status %d, want 1 because some lines are refused", status)
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected standard error: %s", errOut.String())
	}
	golden.Bytes(t, "testdata/normalize.stdout", out.Bytes())

	saved, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	golden.Bytes(t, "testdata/normalize.store", saved)
}

// TestNormalizeRemembers runs the command twice against one store: the
// second run must report the address as seen under the same identifier.
func TestNormalizeRemembers(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "seen")
	const address = "https://tools.agency-example.gov/wage\n"
	var first, second bytes.Buffer
	if status := run([]string{"normalize", "-store", storePath}, strings.NewReader(address), &first, &first); status != 0 {
		t.Fatalf("first run exit status %d: %s", status, first.String())
	}
	if status := run([]string{"normalize", "-store", storePath}, strings.NewReader(address), &second, &second); status != 0 {
		t.Fatalf("second run exit status %d: %s", status, second.String())
	}
	if !strings.Contains(first.String(), "seen    no\nid      PA-0001\n") {
		t.Errorf("first run:\n%s", first.String())
	}
	if !strings.Contains(second.String(), "seen    yes\nid      PA-0001\n") {
		t.Errorf("second run:\n%s", second.String())
	}
}

func TestUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if status := run(nil, strings.NewReader(""), &out, &errOut); status != 2 {
		t.Errorf("no arguments: exit status %d", status)
	}
	if status := run([]string{"unknown"}, strings.NewReader(""), &out, &errOut); status != 2 {
		t.Errorf("unknown command: exit status %d", status)
	}
	out.Reset()
	if status := run([]string{"version"}, strings.NewReader(""), &out, &errOut); status != 0 || out.String() != version+"\n" {
		t.Errorf("version: status %d output %q", status, out.String())
	}
}
