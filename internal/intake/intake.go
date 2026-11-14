// Package intake runs one document through a sub-tool and the shared rules
// every candidate must pass: the control-marking screen, text cleaning and
// address normalization. It is a pure function of its input.
package intake

import (
	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/normalize"
	"github.com/ClearedStack/trawl/internal/screen"
	"github.com/ClearedStack/trawl/internal/tool"
)

const (
	// MaxCandidates is the most addresses one document may yield.
	MaxCandidates = 2000
	// MaxTitle and MaxVia bound the text kept with a candidate.
	MaxTitle = 120
	MaxVia   = 200
)

// Candidate is a cleaned candidate, ready to be filed with the hub.
type Candidate struct {
	// Address is the canonical address.
	Address string `json:"address"`
	Title   string `json:"title,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Via     string `json:"via,omitempty"`
	// Changed is true when cleaning altered the address as it was found.
	Changed bool `json:"changed,omitempty"`
}

// Reject is a broken entry inside an otherwise good document.
type Reject struct {
	Code        errs.Code `json:"code"`
	Tool        string    `json:"tool,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Fingerprint string    `json:"fingerprint"`
}

// Result is a processed document.
type Result struct {
	Candidates []Candidate `json:"candidates,omitempty"`
	Rejects    []Reject    `json:"rejects,omitempty"`
}

// Process runs the sub-tool on the input and applies the shared rules.
//
// A returned error is a public error for the whole document. A candidate
// that is broken is listed in Rejects and the rest of the document goes
// on. A candidate that is valid and covered by no rule stops the whole
// document with E_NEEDS_SUBTOOL; trying the document again later is safe,
// because the hub knows an address it has already been given.
func Process(t tool.Tool, in tool.Input) (Result, error) {
	return ProcessWith(normalize.Config{}, t, in)
}

// ProcessWith is Process with the address settings in cfg.
func ProcessWith(cfg normalize.Config, t tool.Tool, in tool.Input) (Result, error) {
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
	return CleanWith(cfg, name, out)
}

// Clean applies the shared rules to what a sub-tool found: the
// control-marking screen, text cleaning and address normalization. name
// is the sub-tool the errors are attributed to.
func Clean(name string, out tool.Output) (Result, error) {
	return CleanWith(normalize.Config{}, name, out)
}

// CleanWith is Clean with the address settings in cfg.
func CleanWith(cfg normalize.Config, name string, out tool.Output) (Result, error) {
	if len(out.Candidates) > MaxCandidates {
		return Result{}, errs.New(errs.NeedsSubtool, "too_many_candidates", []byte(name)).WithTool(name)
	}

	var res Result
	for _, e := range out.Rejects {
		res.Rejects = append(res.Rejects, reject(e.WithTool(name)))
	}
	for _, c := range out.Candidates {
		if screen.Marked(c.Title) || screen.Marked(c.URL) {
			res.Rejects = append(res.Rejects, reject(errs.New(errs.Parse, "controlled_marking", []byte(c.URL)).WithTool(name)))
			continue
		}
		norm, err := cfg.Normalize(c.URL)
		if err != nil {
			e, _ := errs.As(err)
			if e.Code == errs.NeedsSubtool {
				// The fingerprint names the kind of case, not the one
				// address, so every document stopped for the same reason
				// reports the same fingerprint.
				return Result{}, errs.New(errs.NeedsSubtool, e.Reason, []byte(name+"\n"+e.Reason)).WithTool(name)
			}
			res.Rejects = append(res.Rejects, reject(e.WithTool(name)))
			continue
		}
		res.Candidates = append(res.Candidates, Candidate{
			Address: norm.URL,
			Title:   screen.Text(c.Title, MaxTitle),
			Kind:    c.Kind,
			Via:     screen.Text(c.Via, MaxVia),
			Changed: len(norm.Applied) > 0,
		})
	}
	return res, nil
}

func reject(e *errs.Error) Reject {
	return Reject{Code: e.Code, Tool: e.Tool, Reason: e.Reason, Fingerprint: e.Fingerprint}
}
