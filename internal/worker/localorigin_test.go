package worker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ClearedStack/trawl/internal/muster"
	"github.com/ClearedStack/trawl/internal/normalize"
)

// TestLeasedItemOnTheLocalTestOrigin covers the test-only setting: an item
// recorded at the one local origin is recorded when the worker is given it,
// and refused as not public otherwise, or on any other port.
func TestLeasedItemOnTheLocalTestOrigin(t *testing.T) {
	const origin = "http://127.0.0.1:41234"
	lease := func(address string) *muster.Lease {
		return &muster.Lease{Lease: "PA-0001.1", Kind: "item", ID: "PA-0001", State: "discovered",
			Found: &muster.Found{Source: muster.SourceSAMGov, Title: "Calculator", Address: address}}
	}
	on := &Worker{Tools: set(t), Normalize: normalize.Config{LocalTestOrigin: origin}}
	off := &Worker{Tools: set(t)}

	st := on.decide(lease(origin + "/calculator"))
	if st.Kind != KindComplete || st.State != muster.StateRecorded || !strings.Contains(string(st.Result), origin+"/calculator") {
		t.Errorf("on the origin, setting on: %+v %s", st, st.Result)
	}
	for name, w := range map[string]*Worker{"setting off": off, "setting on, other port": on} {
		address := origin + "/calculator"
		if w == on {
			address = "http://127.0.0.1:41235/calculator"
		}
		st := w.decide(lease(address))
		if st.Kind != KindComplete || st.State != muster.StateRejected || st.Reason != "host_not_public" {
			t.Errorf("%s: %+v", name, st)
		}
	}
	first, _ := json.Marshal(on.decide(lease(origin + "/calculator")))
	second, _ := json.Marshal(on.decide(lease(origin + "/calculator")))
	if string(first) != string(second) {
		t.Errorf("decide is not deterministic: %s then %s", first, second)
	}
}
