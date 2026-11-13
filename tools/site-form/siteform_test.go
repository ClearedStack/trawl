package siteform

import (
	_ "embed"
	"testing"

	"github.com/ClearedStack/trawl/internal/tooltest"
)

//go:embed match.yaml
var matchYAML []byte

func TestFixtures(t *testing.T) {
	tooltest.Run(t, New(), matchYAML, "testdata")
}

func TestProofOfWork(t *testing.T) {
	const challenge, subject = "made-up-challenge", "https://tools.agency-example.gov/wage"
	nonce := Solve(challenge, subject)
	if !Verify(challenge, subject, nonce) {
		t.Fatal("Solve returned a nonce that does not verify")
	}
	// The work is bound to the challenge, the subject and the nonce.
	if Verify("another-challenge", subject, nonce) {
		t.Error("the nonce verified for a different challenge")
	}
	if Verify(challenge, subject+"/other", nonce) {
		t.Error("the nonce verified for a different subject")
	}
	if Verify(challenge, subject, nonce+"x") {
		t.Error("an altered nonce verified")
	}
	// A digest of all zero bits cannot be produced here, but the counter
	// must not run past the digest.
	if got := work("", "", ""); got < 0 || got > 256 {
		t.Errorf("work = %d", got)
	}
}

func TestOwnershipMap(t *testing.T) {
	if ownership.Version != 1 {
		t.Errorf("version %d", ownership.Version)
	}
	for name, f := range ownership.Fields {
		if f.Owner == "" || f.Max < 1 || f.Max > MaxBytes {
			t.Errorf("field %s = %+v", name, f)
		}
		if f.Type != "" && f.Type != "url" {
			t.Errorf("field %s has unknown type %q", name, f.Type)
		}
	}
}
