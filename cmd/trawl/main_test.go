package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func runCmd(t *testing.T, stdin string, args ...string) (stdout, stderr string, status int) {
	t.Helper()
	var out, errb bytes.Buffer
	status = run(args, strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), status
}

// A saved batch run twice against one store: the exact output of each run
// and the exact store file left behind.
func TestGoldenBatch(t *testing.T) {
	store := filepath.Join(t.TempDir(), "seen")
	input := read(t, "batch.input.txt")

	for _, golden := range []string{"batch.first-run.golden.tsv", "batch.second-run.golden.tsv"} {
		out, errOut, status := runCmd(t, input, "normalize", "-store", store)
		if want := read(t, golden); out != want {
			t.Errorf("%s: stdout\n got:\n%s\nwant:\n%s", golden, out, want)
		}
		if errOut != "" {
			t.Errorf("%s: stderr = %q", golden, errOut)
		}
		if status != 1 {
			t.Errorf("%s: status = %d, want 1 (some lines refused)", golden, status)
		}
	}
	got, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	if want := read(t, "batch.store.golden.tsv"); string(got) != want {
		t.Errorf("store file\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestStatusZeroWhenAllAccepted(t *testing.T) {
	store := filepath.Join(t.TempDir(), "seen")
	out, _, status := runCmd(t, "https://example.org/tool\n", "normalize", "-store", store)
	if status != 0 || !strings.HasPrefix(out, "ok\thttps://example.org/tool\t") || !strings.HasSuffix(out, "\tnew\n") {
		t.Fatalf("status %d, stdout %q", status, out)
	}
	if _, _, status := runCmd(t, "", "normalize", "-store", store); status != 0 {
		t.Fatalf("empty input: status %d", status)
	}
}

func TestUsageAndStoreErrors(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt")
	if err := os.WriteFile(corrupt, []byte("not a store\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		args   []string
		stderr string
	}{
		{"no subcommand", nil, usage},
		{"unknown subcommand", []string{"sweep"}, usage},
		{"no store", []string{"normalize"}, usage},
		{"unknown flag", []string{"normalize", "-store", "x", "-fast"}, usage},
		{"extra argument", []string{"normalize", "-store", "x", "https://example.org/"}, usage},
		{"store cannot be opened", []string{"normalize", "-store", filepath.Join(dir, "missing-dir", "seen")}, "trawl: store unavailable\n"},
		{"store corrupt", []string{"normalize", "-store", corrupt}, "trawl: store corrupt\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, errOut, status := runCmd(t, "https://example.org/tool\n", c.args...)
			if status != 2 || out != "" || errOut != c.stderr {
				t.Fatalf("status %d, stdout %q, stderr %q; want 2, \"\", %q", status, out, errOut, c.stderr)
			}
		})
	}
}
