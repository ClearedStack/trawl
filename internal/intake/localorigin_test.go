package intake

import (
	"testing"

	"github.com/ClearedStack/trawl/internal/normalize"
	"github.com/ClearedStack/trawl/internal/tool"
)

func TestCleanWithLocalTestOrigin(t *testing.T) {
	out := tool.Output{Candidates: []tool.Candidate{
		{URL: "http://127.0.0.1:41234/calculator"},
		{URL: "http://127.0.0.1:41235/calculator"},
		{URL: "http://localhost:41234/calculator"},
	}}
	cfg := normalize.Config{LocalTestOrigin: "http://127.0.0.1:41234"}
	res, err := CleanWith(cfg, "made-up", out)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Address != "http://127.0.0.1:41234/calculator" {
		t.Errorf("candidates = %+v", res.Candidates)
	}
	if len(res.Rejects) != 2 || res.Rejects[0].Reason != "host_not_public" || res.Rejects[1].Reason != "host_not_public" {
		t.Errorf("rejects = %+v", res.Rejects)
	}
	// Unset, all three are refused.
	res, err = Clean("made-up", out)
	if err != nil || len(res.Candidates) != 0 || len(res.Rejects) != 3 {
		t.Errorf("unset: %+v, %v", res, err)
	}
	in := tool.Input{}
	res, err = ProcessWith(cfg, fake{out: out}, in)
	if err != nil || len(res.Candidates) != 1 {
		t.Errorf("ProcessWith: %+v, %v", res, err)
	}
}
