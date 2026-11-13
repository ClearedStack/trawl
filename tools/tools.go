// Package tools is the registry of Trawl's sub-tools. Each sub-tool lives
// in its own folder here, with a matching rule, logic and fixtures, and is
// listed in registry.yaml. Nothing is discovered at run time: the registry
// and the rules are compiled into the binary.
package tools

import (
	"embed"
	"fmt"

	"github.com/ClearedStack/trawl/internal/muster"
	"github.com/ClearedStack/trawl/internal/tool"
	siteform "github.com/ClearedStack/trawl/tools/site-form"
)

//go:embed registry.yaml */match.yaml
var files embed.FS

// logic maps a folder name to the constructor of its logic.
var logic = map[string]func() tool.Tool{
	siteform.Name: siteform.New,
}

// Load reads the registry and every listed sub-tool's matching rule, and
// pairs each rule with its logic. It fails if the registry lists a
// sub-tool that has no logic, or logic exists that the registry leaves out.
func Load() (*tool.Set, error) {
	data, err := files.ReadFile("registry.yaml")
	if err != nil {
		return nil, err
	}
	reg, err := tool.ParseRegistry(data)
	if err != nil {
		return nil, fmt.Errorf("registry.yaml: %w", err)
	}
	if reg.Contract != muster.ContractVersion {
		return nil, fmt.Errorf("registry.yaml is for contract %q, this build speaks %q", reg.Contract, muster.ContractVersion)
	}

	var entries []tool.Entry
	listed := map[string]bool{}
	for _, name := range reg.Tools {
		build, ok := logic[name]
		if !ok {
			return nil, fmt.Errorf("registry.yaml lists %q, which has no logic", name)
		}
		data, err := files.ReadFile(name + "/match.yaml")
		if err != nil {
			return nil, fmt.Errorf("%s has no match.yaml", name)
		}
		rule, err := tool.ParseRule(data)
		if err != nil {
			return nil, fmt.Errorf("%s/match.yaml: %w", name, err)
		}
		if rule.Tool != name {
			return nil, fmt.Errorf("%s/match.yaml names %q", name, rule.Tool)
		}
		listed[name] = true
		entries = append(entries, tool.Entry{Rule: rule, Tool: build()})
	}
	for name := range logic {
		if !listed[name] {
			return nil, fmt.Errorf("%q has logic and is missing from registry.yaml", name)
		}
	}
	return tool.NewSet(entries...)
}
