package intake

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/tool"
)

type fake struct {
	out tool.Output
	err error
}

func (f fake) Name() string                            { return "made-up" }
func (f fake) Extract(tool.Input) (tool.Output, error) { return f.out, f.err }

func TestProcess(t *testing.T) {
	out := tool.Output{
		Candidates: []tool.Candidate{
			{URL: "HTTPS://Tools.Agency-Example.gov/wage/?utm_source=x", Kind: "tool", Title: " <b>Wage</b>  calculator ", Via: "made-up#1"},
			{URL: "ftp://files.agency-example.gov/a.zip", Kind: "tool"},
			{URL: "https://docs.agency-example.gov/guide", Title: "(CUI) made-up guide"},
			{URL: "https://calc.depot-example.mil/rates"},
		},
		Rejects: []*errs.Error{errs.New(errs.Parse, "bad_entry", []byte("x"))},
	}
	res, err := Process(fake{out: out}, tool.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates = %+v", res.Candidates)
	}
	first := res.Candidates[0]
	if first.Address != "https://tools.agency-example.gov/wage" || !first.Changed ||
		first.Title != "Wage calculator" || first.Via != "made-up#1" || first.Kind != "tool" {
		t.Errorf("first candidate = %+v", first)
	}
	if second := res.Candidates[1]; second.Address != "https://calc.depot-example.mil/rates" || second.Changed {
		t.Errorf("second candidate = %+v", second)
	}
	if len(res.Rejects) != 3 {
		t.Fatalf("rejects = %+v", res.Rejects)
	}
	reasons := []string{res.Rejects[0].Reason, res.Rejects[1].Reason, res.Rejects[2].Reason}
	want := []string{"bad_entry", "scheme_not_public", "controlled_marking"}
	for i := range want {
		if reasons[i] != want[i] || res.Rejects[i].Tool != "made-up" || res.Rejects[i].Code != errs.Parse {
			t.Errorf("reject %d = %+v, want reason %s", i, res.Rejects[i], want[i])
		}
	}
}

func TestUnfamiliarCandidatePausesTheItem(t *testing.T) {
	out := tool.Output{Candidates: []tool.Candidate{
		{URL: "https://tools.agency-example.gov/wage"},
		{URL: "https://bücher.example.com/katalog"},
	}}
	_, err := Process(fake{out: out}, tool.Input{})
	e, ok := errs.As(err)
	if !ok || e.Code != errs.NeedsSubtool || e.Reason != "idn_host" || e.Tool != "made-up" {
		t.Fatalf("err = %v", err)
	}
}

func TestToolErrors(t *testing.T) {
	_, err := Process(fake{err: errs.New(errs.Parse, "bad_json", []byte("{"))}, tool.Input{})
	e, ok := errs.As(err)
	if !ok || e.Code != errs.Parse || e.Tool != "made-up" {
		t.Fatalf("public error: %v", err)
	}

	_, err = Process(fake{err: errors.New("dial tcp 10.0.0.1:443: made-up detail")}, tool.Input{})
	e, ok = errs.As(err)
	if !ok || e.Code != errs.NeedsSubtool || e.Reason != "subtool_fault" {
		t.Fatalf("plain error: %v", err)
	}
	if got := err.Error(); got != "E_NEEDS_SUBTOOL tool=made-up reason=subtool_fault fp="+errs.Fingerprint([]byte("made-up")) {
		t.Errorf("error text %q", got)
	}
}

func TestTooManyCandidates(t *testing.T) {
	var out tool.Output
	for i := 0; i <= MaxCandidates; i++ {
		out.Candidates = append(out.Candidates, tool.Candidate{URL: fmt.Sprintf("https://tools.agency-example.gov/t/%d", i)})
	}
	_, err := Process(fake{out: out}, tool.Input{})
	e, ok := errs.As(err)
	if !ok || e.Code != errs.NeedsSubtool || e.Reason != "too_many_candidates" {
		t.Fatalf("err = %v", err)
	}
}
