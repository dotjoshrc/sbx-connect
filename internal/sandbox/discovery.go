package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// sbx ls --json uses an object containing a sandboxes array. Workspace paths
// are mounted at the same absolute path in the guest; read-only entries have
// a :ro suffix. See docker/sandboxes cli-plugin/commands/ls.go and workspaces.go.
type sandboxEntry struct {
	Name              string   `json:"name"`
	Agent             string   `json:"agent"`
	Status            string   `json:"status"`
	Workspaces        []string `json:"workspaces"`
	WorkspaceMissing  bool     `json:"workspace_missing"`
	MountPolicyDenied bool     `json:"mount_policy_denied"`
	GuestUnresponsive bool     `json:"guest_unresponsive"`
}

func parseSandboxes(out string) ([]sandboxEntry, error) {
	var listing struct {
		Sandboxes *[]sandboxEntry `json:"sandboxes"`
	}
	if err := json.Unmarshal([]byte(out), &listing); err != nil {
		return nil, fmt.Errorf("invalid sbx ls --json response: %w", err)
	}
	if listing.Sandboxes == nil {
		return nil, fmt.Errorf("invalid sbx ls --json response: missing sandboxes array")
	}
	for _, entry := range *listing.Sandboxes {
		if entry.Name == "" || strings.HasPrefix(entry.Name, "-") || strings.ContainsAny(entry.Name, "/\x00\r\n") || entry.Agent == "" || entry.Status == "" {
			return nil, fmt.Errorf("invalid sbx ls --json response: incomplete or invalid sandbox entry")
		}
		for _, workspace := range entry.Workspaces {
			if !filepath.IsAbs(workspace) || strings.ContainsRune(workspace, '\x00') {
				return nil, fmt.Errorf("invalid sbx ls --json response: workspace must be an absolute path")
			}
		}
	}
	return *listing.Sandboxes, nil
}

func workspacePath(workspace string) (string, bool) {
	readOnly := strings.HasSuffix(workspace, ":ro")
	if readOnly || strings.HasSuffix(workspace, ":rw") {
		workspace = workspace[:len(workspace)-3]
	}
	return filepath.Clean(workspace), readOnly
}

// coveringMount respects path boundaries and a more specific read-only mount
// shadowing a writable parent. Logical paths are kept distinct from symlinks.
func coveringMount(workspaces []string, project string) string {
	var best string
	var readOnly bool
	for _, workspace := range workspaces {
		path, ro := workspacePath(workspace)
		rel, err := filepath.Rel(path, project)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(path) > len(best) {
			best, readOnly = path, ro
		} else if path == best {
			readOnly = readOnly || ro
		}
	}
	if readOnly {
		return ""
	}
	return best
}

type candidate struct {
	name, mount string
}

func candidates(entries []sandboxEntry, p Project) []candidate {
	var result []candidate
	for _, entry := range entries {
		if entry.Agent != p.Agent.Name || entry.Status != "running" || entry.WorkspaceMissing || entry.MountPolicyDenied || entry.GuestUnresponsive {
			continue
		}
		mount := coveringMount(entry.Workspaces, p.Path)
		if mount == "" {
			continue
		}
		if Owned(entry.Name) {
			// Stable names are derived from a workspace path. Other owned names
			// are disposable runs, which their owner may remove at any moment.
			stable := false
			for _, workspace := range entry.Workspaces {
				path, _ := workspacePath(workspace)
				stable = stable || entry.Name == Name(entry.Agent, path)
			}
			if !stable {
				continue
			}
		}
		result = append(result, candidate{entry.Name, mount})
	}
	sort.Slice(result, func(i, j int) bool {
		// A sandbox created by an earlier fallback must not mask a user's
		// existing sandbox, even if the fallback has a more specific mount.
		ownedI, ownedJ := Owned(result[i].name), Owned(result[j].name)
		if ownedI != ownedJ {
			return !ownedI
		}
		if len(result[i].mount) != len(result[j].mount) {
			return len(result[i].mount) > len(result[j].mount)
		}
		return result[i].name < result[j].name
	})
	return result
}

// prepareCandidate keeps kit installation and verification under the same lock
// so concurrent editor connections cannot recreate the container twice.
func (m Manager) prepareCandidate(ctx context.Context, p *Project, name string) (bool, error) {
	unlock, err := m.lock(ctx, name)
	if err != nil {
		return false, err
	}
	defer unlock()
	selected := *p
	selected.Name = name
	check, err := m.checkLauncher(ctx, &selected, true)
	if err != nil {
		return false, fmt.Errorf("cannot verify reuse of sandbox %s: %w", name, err)
	}
	switch check {
	case "incompatible":
		return false, nil
	case "ready":
		p.Agent.Binary = selected.Agent.Binary
		return true, nil
	case "needs-kit":
		if !p.AddACPKit {
			return false, fmt.Errorf("sandbox %s is missing ACP launcher (%s-acp or acp on PATH, or %s); rerun with --add-acp-kit to install the ACP kit in this sandbox", name, p.Agent.Name, p.Agent.Binary)
		}
		fmt.Fprintf(m.Runner.Stderr, "Adding ACP kit %s to existing sandbox %s (container recreation may interrupt running processes)\n", p.Kit, name)
		m.debugf("lifecycle reuse-kit start name=%q kit=%q", name, p.Kit)
		if err := m.Runner.Setup(ctx, "kit", "add", name, p.Kit); err != nil {
			return false, fmt.Errorf("cannot add ACP kit to existing sandbox %s; check sbx kit add support and diagnostics: %w", name, err)
		}
		status, err := m.checkLauncher(ctx, &selected, false)
		if err == nil && status != "ready" {
			err = fmt.Errorf("no executable %s-acp, acp, or %s", p.Agent.Name, p.Agent.Binary)
		}
		if err != nil {
			return false, fmt.Errorf("ACP kit added to %s but launcher %s is unavailable: %w", name, p.Agent.Binary, err)
		}
		m.debugf("lifecycle reuse-kit done name=%q", name)
		p.Agent.Binary = selected.Agent.Binary
		return true, nil
	default:
		return false, fmt.Errorf("cannot verify reuse of sandbox %s: unexpected launcher check response", name)
	}
}

func (m Manager) discover(ctx context.Context, p Project) (Project, bool, error) {
	m.debugf("lifecycle discover start agent=%q project=%q fallback=%q", p.Agent.Name, p.Path, p.Name)
	out, err := m.Runner.Capture(ctx, "ls", "--json")
	if err != nil {
		m.debugf("lifecycle discover list failed error=%v", err)
		return p, false, fmt.Errorf("cannot discover sandboxes; check sbx ls --json, login and the sandbox service: %w", err)
	}
	entries, err := parseSandboxes(out)
	if err != nil {
		m.debugf("lifecycle discover parse failed error=%v", err)
		return p, false, err
	}
	candidates := candidates(entries, p)
	m.debugf("lifecycle discover listed entries=%d candidates=%d", len(entries), len(candidates))
	for _, candidate := range candidates {
		m.debugf("lifecycle discover check start name=%q mount=%q", candidate.name, candidate.mount)
		ready, err := m.prepareCandidate(ctx, &p, candidate.name)
		if err != nil {
			m.debugf("lifecycle discover check failed name=%q error=%v", candidate.name, err)
			return p, false, err
		}
		if !ready {
			fmt.Fprintf(m.Runner.Stderr, "Skipping %s: workspace uses clone mode\n", candidate.name)
			m.debugf("lifecycle discover skip name=%q reason=%q", candidate.name, "incompatible")
			continue
		}
		p.Name = candidate.name
		fmt.Fprintf(m.Runner.Stderr, "Reusing running sandbox %s; extra kits and template changes only affect new sandboxes\n", p.Name)
		m.debugf("lifecycle discover selected name=%q mount=%q", p.Name, candidate.mount)
		return p, true, nil
	}
	m.debugf("lifecycle discover none fallback=%q", p.Name)
	return p, false, nil
}
