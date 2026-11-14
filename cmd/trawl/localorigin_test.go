package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const localWarning = `{"event":"local_test_origin_enabled","warning":"test only: one local http origin passes address normalization without the public host check"}` + "\n"

func normalizeOnce(t *testing.T, address string) (status int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	store := filepath.Join(t.TempDir(), "seen")
	status = run([]string{"normalize", "-store", store}, strings.NewReader(address+"\n"), &out, &errOut)
	return status, out.String(), errOut.String()
}

func TestNormalizeLocalTestOrigin(t *testing.T) {
	t.Setenv("TRAWL_LOCAL_TEST_ORIGIN", "http://127.0.0.1:41234")

	status, out, errOut := normalizeOnce(t, "http://127.0.0.1:41234/calculator")
	if status != 0 || !strings.HasPrefix(out, "url     http://127.0.0.1:41234/calculator\n") || !strings.Contains(out, "id      PA-0001\n") {
		t.Errorf("on the origin: status %d\n%s", status, out)
	}
	if errOut != localWarning {
		t.Errorf("standard error = %q, want the one fixed warning line", errOut)
	}

	status, out, _ = normalizeOnce(t, "http://127.0.0.1:41235/calculator")
	if status != 1 || !strings.Contains(out, "reason=host_not_public") {
		t.Errorf("other port: status %d\n%s", status, out)
	}
	status, out, _ = normalizeOnce(t, "http://localhost:41234/calculator")
	if status != 1 || !strings.Contains(out, "reason=host_not_public") {
		t.Errorf("localhost: status %d\n%s", status, out)
	}
}

func TestNormalizeWithoutLocalTestOrigin(t *testing.T) {
	t.Setenv("TRAWL_LOCAL_TEST_ORIGIN", "")
	status, out, errOut := normalizeOnce(t, "http://127.0.0.1:41234/calculator")
	if status != 1 || !strings.Contains(out, "reason=host_not_public") || errOut != "" {
		t.Errorf("unset: status %d\nstdout %s\nstderr %q", status, out, errOut)
	}
}

func TestLocalTestOriginRefusedValues(t *testing.T) {
	for _, v := range []string{"http://127.0.0.1:41234/", "https://127.0.0.1:41234", "http://localhost:41234", "http://127.0.0.1", "http://127.0.0.1:0", "http://[::1]:70000", "127.0.0.1:41234", " http://127.0.0.1:41234"} {
		t.Setenv("TRAWL_LOCAL_TEST_ORIGIN", v)
		for _, args := range [][]string{{"normalize"}, {"work", "-muster-url", "http://hub.made-up.svc", "-spool", t.TempDir()}} {
			var out, errOut strings.Builder
			args := args
			if args[0] == "normalize" {
				args = append(args, "-store", filepath.Join(t.TempDir(), "seen"))
			}
			if status := run(args, strings.NewReader("http://127.0.0.1:41234/x\n"), &out, &errOut); status != 2 {
				t.Errorf("%s with %q: exit status %d, want 2", args[0], v, status)
			}
			if !strings.Contains(errOut.String(), "TRAWL_LOCAL_TEST_ORIGIN must be http://127.0.0.1:<port> or http://[::1]:<port>") || out.Len() != 0 {
				t.Errorf("%s with %q: stdout %q stderr %q", args[0], v, out.String(), errOut.String())
			}
		}
	}
}

// deploy/ never sets the test-only setting.
func TestDeployNeverSetsLocalTestOrigin(t *testing.T) {
	files, _ := filepath.Glob("../../deploy/*")
	if len(files) == 0 {
		t.Fatal("no deploy files found")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "LOCAL_TEST_ORIGIN") {
			t.Errorf("%s mentions LOCAL_TEST_ORIGIN", f)
		}
	}
}
