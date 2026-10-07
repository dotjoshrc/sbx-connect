package zed

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

func TestMergeJSONCAndIdempotence(t *testing.T) {
	entries := Entries("/Applications/My Tools/sbx-connect", "", "", "reuse")
	for _, input := range []string{
		"{}",
		"// header\n{\n  // theme comment\n  \"theme\": \"One Dark\", // keep me\n}\n",
		"{\"agent_servers\": {/* existing */ \"Other\": {\"command\": \"other\",},}, \"font_size\": 14,}",
		"{\"agent_servers\": { // empty agent list\n}, /* final comment */}",
		"{\"agent_servers\": {\"Other\": {} // no trailing comma\n}}",
	} {
		t.Run(input, func(t *testing.T) {
			out, err := Merge([]byte(input), entries)
			if err != nil {
				t.Fatal(err)
			}
			v, err := hujson.Parse(out)
			if err != nil {
				t.Fatalf("invalid output %s: %v", out, err)
			}
			v = v.Clone() // Parse retains slices into out; Standardize mutates them.
			v.Standardize()
			var data map[string]any
			if err := json.Unmarshal(v.Pack(), &data); err != nil {
				t.Fatal(err)
			}
			servers := data["agent_servers"].(map[string]any)
			for name := range entries {
				if servers[name] == nil {
					t.Fatal("missing", name)
				}
			}
			for _, preserved := range []string{"// header", "// theme comment", "// keep me", "/* existing */", "// empty agent list", "/* final comment */", "// no trailing comma", `"theme": "One Dark"`, `"font_size": 14`, `"Other": {"command": "other",}`} {
				if strings.Contains(input, preserved) && !bytes.Contains(out, []byte(preserved)) {
					t.Fatalf("lost %q in %s", preserved, out)
				}
			}
			again, err := Merge(out, entries)
			if err != nil || !bytes.Equal(out, again) {
				t.Fatalf("not idempotent: %s %v", again, err)
			}
		})
	}
}

func TestEntriesSetSandboxedDefaultModes(t *testing.T) {
	entries := Entries("/bin/sbx-connect", "", "", "reuse")
	for name, want := range map[string]string{
		"Codex in Docker Sandbox":  "agent-full-access",
		"Claude in Docker Sandbox": "bypassPermissions",
	} {
		if got := entries[name].DefaultMode; got != want {
			t.Fatalf("%s default mode = %q, want %q", name, got, want)
		}
	}
}

func TestEntriesCanBeEphemeral(t *testing.T) {
	entries := Entries("/bin/sbx-connect", "", "", "ephemeral")
	for name, wantArgs := range map[string][]string{
		"Codex in Ephemeral Docker Sandbox":  {"run", "codex", "--mode=ephemeral"},
		"Claude in Ephemeral Docker Sandbox": {"run", "claude", "--mode=ephemeral"},
	} {
		entry, ok := entries[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if !reflect.DeepEqual(entry.Args, wantArgs) {
			t.Fatalf("%s args = %q, want %q", name, entry.Args, wantArgs)
		}
	}
}

func TestEntriesCanBeAuto(t *testing.T) {
	entries := Entries("/bin/sbx-connect", "", "", "auto")
	for name, wantArgs := range map[string][]string{
		"Codex in Auto Docker Sandbox":  {"run", "codex", "--mode=auto"},
		"Claude in Auto Docker Sandbox": {"run", "claude", "--mode=auto"},
	} {
		entry, ok := entries[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if !reflect.DeepEqual(entry.Args, wantArgs) {
			t.Fatalf("%s args = %q, want %q", name, entry.Args, wantArgs)
		}
	}
}

func TestEntriesDefaultToReuseMode(t *testing.T) {
	entries := Entries("/bin/sbx-connect", "", "", "reuse")
	for name, wantArgs := range map[string][]string{
		"Codex in Docker Sandbox":  {"run", "codex", "--mode=reuse"},
		"Claude in Docker Sandbox": {"run", "claude", "--mode=reuse"},
	} {
		entry, ok := entries[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if !reflect.DeepEqual(entry.Args, wantArgs) {
			t.Fatalf("%s args = %q, want %q", name, entry.Args, wantArgs)
		}
	}
}

func TestMergeUpgradesExistingEntriesWithDefaultMode(t *testing.T) {
	input := []byte(`{
  "agent_servers": {
    "Codex in Docker Sandbox": {
      "type": "custom",
      "command": "/bin/sbx-connect",
      "args": ["run", "codex", "--mode=reuse"]
    },
    "Claude in Docker Sandbox": {
      "type": "custom",
      "command": "/bin/sbx-connect",
      "args": ["run", "claude", "--mode=reuse"]
    }
  }
}`)
	out, err := Merge(input, Entries("/bin/sbx-connect", "", "", "reuse"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"default_mode": "agent-full-access"`, `"default_mode": "bypassPermissions"`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
}

func TestConflictingAndInvalidSettings(t *testing.T) {
	for _, input := range []string{
		`{"agent_servers":{"Codex in Docker Sandbox":{"command":"custom"}}}`,
		`{"agent_servers":{"Claude in Docker Sandbox":{"command":"custom"}}}`,
		`{"agent_servers":null}`, `{"agent_servers":[]}`, `[]`, `invalid`,
		`{"agent_servers":{},"agent_servers":{}}`,
		`{"agent_servers":{"Other":{},"Other":{}}}`,
	} {
		if _, err := Merge([]byte(input), Entries("/bin/sbx-connect", "", "", "reuse")); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestInstallBackupModeSymlinkAndNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	link := filepath.Join(dir, "linked.json")
	original := []byte("// settings\n{\"theme\":\"test\",}\n")
	if err := os.WriteFile(path, original, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	entries := Entries("/bin/sbx-connect", "", "", "reuse")
	r, err := Install(context.Background(), link, filepath.Join(dir, "cache"), entries)
	if err != nil || !r.Changed || r.Backup == "" {
		t.Fatalf("%+v %v", r, err)
	}
	backup, err := os.ReadFile(r.Backup)
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("bad backup %s %v", backup, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("mode %v %v", info, err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	r, err = Install(context.Background(), link, filepath.Join(dir, "cache"), entries)
	if err != nil || r.Changed || r.Backup != "" {
		t.Fatalf("no-op %+v %v", r, err)
	}
	before, _ := os.ReadFile(path)
	_, err = Install(context.Background(), path, filepath.Join(dir, "cache"), Entries("/different/path", "", "", "reuse"))
	if err == nil {
		t.Fatal("expected conflict")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("conflict modified settings")
	}
	backups, _ := filepath.Glob(path + ".backup-*")
	if len(backups) != 1 {
		t.Fatal(backups)
	}
}
