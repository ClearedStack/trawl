// Package intake runs one item through a sub-tool and the shared rules
// every candidate must pass: the control-marking screen, text cleaning,
// address normalization and hashing. It is a pure function of its input.
package intake

import (
	"github.com/ClearedStack/trawl/internal/dedupe"
	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/muster"
	"github.com/ClearedStack/trawl/internal/normalize"
	"github.com/ClearedStack/trawl/internal/screen"
	"github.com/ClearedStack/trawl/internal/tool"
)

const (
	// MaxCandidates is the most addresses one item may yield.
	MaxCandidates = 500
	// MaxTitle and MaxVia bound the text kept with a candidate.
	MaxTitle = 120
	MaxVia   = 200
)

// Result is a processed item, ready to be staged.
type Result struct {
	Entries   []dedupe.Entry  `json:"entries,omitempty"`
	Proposals []tool.Proposal `json:"proposals,omitempty"`
	Rejects   []muster.Reject `json:"rejects,omitempty"`
	RateKey   string          `json:"rate_key,omitempty"`
	RateLimit int             `json:"rate_limit,omitempty"`
}

// Process runs the sub-tool on the input and applies the shared rules.
//
// A returned error is a public error for the whole item. A candidate that
// is broken is listed in Rejects and the rest of the item goes on. A
// candidate that is valid and covered by no rule pauses the whole item with
// E_NEEDS_SUBTOOL, because the contract can pause an item and not a part
// of one; replaying the item later is safe, since recording is by hash.
func Process(t tool.Tool, in tool.Input) (Result, error) {
	name := t.Name()
	out, err := t.Extract(in)
	if err != nil {
		if e, ok := errs.As(err); ok {
			return Result{}, e.WithTool(name)
		}
		// A sub-tool must return public errors. Anything else is a fault
		// in the sub-tool, reported without its text.
		return Result{}, errs.New(errs.NeedsSubtool, "subtool_fault", []byte(name)).WithTool(name)
	}
	if len(out.Candidates) > MaxCandidates {
		return Result{}, errs.New(errs.NeedsSubtool, "too_many_candidates", []byte(name)).WithTool(name)
	}

	res := Result{Proposals: out.Proposals, RateKey: out.RateKey, RateLimit: out.RateLimit}
	for _, e := range out.Rejects {
		res.Rejects = append(res.Rejects, reject(e.WithTool(name)))
	}
	for _, c := range out.Candidates {
		if screen.Marked(c.Title) || screen.Marked(c.URL) {
			res.Rejects = append(res.Rejects, reject(errs.New(errs.Parse, "controlled_marking", []byte(c.URL)).WithTool(name)))
			continue
		}
		norm, err := normalize.Normalize(c.URL)
		if err != nil {
			e, _ := errs.As(err)
			e = e.WithTool(name)
			if e.Code == errs.NeedsSubtool {
				return Result{}, e
			}
			res.Rejects = append(res.Rejects, reject(e))
			continue
		}
		res.Entries = append(res.Entries, dedupe.Entry{
			URL:    norm.URL,
			SHA256: dedupe.Hash(norm.URL),
			Kind:   c.Kind,
			Title:  screen.Text(c.Title, MaxTitle),
			Via:    screen.Text(c.Via, MaxVia),
		})
	}
	return res, nil
}

func reject(e *errs.Error) muster.Reject {
	return muster.Reject{Code: e.Code, Tool: e.Tool, Reason: e.Reason, Fingerprint: e.Fingerprint}
}
