package sandbox

import (
	"reflect"
	"testing"

	"sbx-connect/internal/agent"
)

func TestParseSandboxes(t *testing.T) {
	for _, response := range []string{
		``, `null`, `[]`, `{}`, `{"sandboxes":null}`, `{"sandboxes":{}}`,
		`{"sandboxes":[{}]}`,
		`{"sandboxes":[{"name":"-bad","agent":"codex","status":"running"}]}`,
		`{"sandboxes":[{"name":"demo","agent":"codex","status":"running","workspaces":["relative"]}]}`,
		`{"sandboxes":[]} {"sandboxes":[]}`,
	} {
		if _, err := parseSandboxes(response); err == nil {
			t.Errorf("accepted invalid listing %q", response)
		}
	}
	response := `{"sandboxes":[{"name":"demo","id":"uuid","agent":"codex","status":"running","workspaces":["/repo","/other:ro"],"ports":[]}],"future":true}`
	got, err := parseSandboxes(response)
	want := []sandboxEntry{{Name: "demo", Agent: "codex", Status: "running", Workspaces: []string{"/repo", "/other:ro"}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, %v", got, err)
	}
	if got, err := parseSandboxes(`{"sandboxes":[]}`); err != nil || len(got) != 0 {
		t.Fatalf("empty listing: %#v %v", got, err)
	}
}

func TestCoveringMount(t *testing.T) {
	for _, tc := range []struct {
		name       string
		workspaces []string
		project    string
		want       string
	}{
		{"exact", []string{"/repo"}, "/repo", "/repo"},
		{"parent", []string{"/repo"}, "/repo/subdir", "/repo"},
		{"additional", []string{"/other", "/repo:rw"}, "/repo/subdir", "/repo"},
		{"boundary", []string{"/repo"}, "/repository", ""},
		{"read only", []string{"/repo:ro"}, "/repo/subdir", ""},
		{"shadowed", []string{"/repo", "/repo/subdir:ro"}, "/repo/subdir/child", ""},
		{"writable child", []string{"/repo:ro", "/repo/subdir"}, "/repo/subdir/child", "/repo/subdir"},
		{"duplicate modes", []string{"/repo:ro", "/repo:rw"}, "/repo", ""},
		{"clean path", []string{"/repo/./"}, "/repo/subdir", "/repo"},
		{"logical spelling", []string{"/physical"}, "/logical/subdir", ""},
		{"metacharacters", []string{"/repo with spaces ' $();"}, "/repo with spaces ' $();/child", "/repo with spaces ' $();"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := coveringMount(tc.workspaces, tc.project); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCandidates(t *testing.T) {
	ag, err := agent.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	p := Project{Path: "/repo/subdir", Agent: ag}
	entries := []sandboxEntry{
		{Name: "z-exact", Agent: "codex", Status: "running", Workspaces: []string{p.Path}},
		{Name: "a-parent", Agent: "codex", Status: "running", Workspaces: []string{"/repo"}},
		{Name: "a-exact", Agent: "codex", Status: "running", Workspaces: []string{p.Path}},
		{Name: "wrong-agent", Agent: "claude", Status: "running", Workspaces: []string{p.Path}},
		{Name: "stopped", Agent: "codex", Status: "stopped", Workspaces: []string{p.Path}},
		{Name: "missing", Agent: "codex", Status: "running", Workspaces: []string{p.Path}, WorkspaceMissing: true},
		{Name: "denied", Agent: "codex", Status: "running", Workspaces: []string{p.Path}, MountPolicyDenied: true},
		{Name: "unresponsive", Agent: "codex", Status: "running", Workspaces: []string{p.Path}, GuestUnresponsive: true},
		{Name: "read-only", Agent: "codex", Status: "running", Workspaces: []string{p.Path + ":ro"}},
		{Name: "sbx-connect-codex-subdir-000000000000000000000000", Agent: "codex", Status: "running", Workspaces: []string{p.Path}},
		{Name: Name("codex", "/repo"), Agent: "codex", Status: "running", Workspaces: []string{"/repo"}},
		{Name: Name("codex", p.Path), Agent: "codex", Status: "running", Workspaces: []string{p.Path}},
	}
	want := []candidate{{"a-exact", p.Path}, {"z-exact", p.Path}, {"a-parent", "/repo"}, {Name("codex", p.Path), p.Path}, {Name("codex", "/repo"), "/repo"}}
	if got := candidates(entries, p); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	// Discovery ordering must not depend on the service's listing order.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	if got := candidates(entries, p); !reflect.DeepEqual(got, want) {
		t.Fatalf("reversed listing: %#v", got)
	}
}
