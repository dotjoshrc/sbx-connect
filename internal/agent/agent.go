// Package agent defines the supported sandbox flavors and ACP kits.
package agent

import "fmt"

// Definition describes an agent and the kit that supplies its ACP launcher.
type Definition struct {
	Name, Kit, Binary, ZedName, ZedDefaultMode string
}

// All contains the supported agents and their published kit releases.
var All = []Definition{
	{
		Name:           "codex",
		Kit:            "docker.io/sbx/codex-acp-kit:20260924-d058fedc156325f87612d9bcd9bd313ab74ba100",
		Binary:         "/home/agent/.local/bin/codex-acp",
		ZedName:        "Codex in Docker Sandbox",
		ZedDefaultMode: "agent-full-access",
	},
	{
		Name:           "claude",
		Kit:            "docker.io/sbx/claude-acp-kit:20260924-d058fedc156325f87612d9bcd9bd313ab74ba100",
		Binary:         "/home/agent/.local/bin/claude-acp",
		ZedName:        "Claude in Docker Sandbox",
		ZedDefaultMode: "bypassPermissions",
	},
}

// Lookup returns the definition for a supported agent.
func Lookup(name string) (Definition, error) {
	for _, a := range All {
		if a.Name == name {
			return a, nil
		}
	}
	return Definition{}, fmt.Errorf("unknown agent %q: choose codex or claude", name)
}
