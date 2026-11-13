package errs

import (
	"fmt"
	"testing"
)

func TestErrorString(t *testing.T) {
	e := New(Parse, "honeypot", []byte("made-up payload")).WithTool("site-form")
	want := "E_PARSE tool=site-form reason=honeypot fp=" + Fingerprint([]byte("made-up payload"))
	if e.Error() != want {
		t.Fatalf("got %q want %q", e.Error(), want)
	}
	if len(e.Fingerprint) != 12 {
		t.Fatalf("fingerprint length %d", len(e.Fingerprint))
	}
}

func TestReasonCannotCarryData(t *testing.T) {
	for _, bad := range []string{"", "https://host.example/a", "Has Space", "10.0.0.1", "token=abc", "UPPER"} {
		e := New(Parse, bad, nil)
		if e.Reason != "unspecified" {
			t.Errorf("reason %q survived as %q", bad, e.Reason)
		}
	}
}

func TestAsUnwraps(t *testing.T) {
	inner := New(Timeout, "upstream", nil)
	wrapped := fmt.Errorf("context: %w", inner)
	got, ok := As(wrapped)
	if !ok || got != inner {
		t.Fatal("As did not find the public error")
	}
	if _, ok := As(fmt.Errorf("plain")); ok {
		t.Fatal("As matched a plain error")
	}
}

func TestCodes(t *testing.T) {
	for _, c := range []Code{Timeout, Parse, NeedsSubtool} {
		if !c.Valid() {
			t.Errorf("%s should be valid", c)
		}
	}
	if Code("E_OTHER").Valid() {
		t.Error("unknown code accepted")
	}
}
