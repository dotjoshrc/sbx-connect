package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dotjoshrc/sbx-connect/internal/agent"
	"github.com/dotjoshrc/sbx-connect/internal/sandbox"
)

func (f fixture) seedSandbox(t *testing.T, name, agent, status string, workspaces []string, launcherPresent bool) string {
	t.Helper()
	dir := filepath.Join(f.state, "sandboxes", name)
	bin := filepath.Join(dir, "home", ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(workspaces)
	if err != nil {
		t.Fatal(err)
	}
	for file, value := range map[string]string{"agent": agent, "status": status, "workspaces": string(data), "keep": "sandbox-local data"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if launcherPresent {
		if err := os.Symlink(fake, filepath.Join(bin, agent+"-acp")); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestACPCommandsOnSandboxPath(t *testing.T) {
	for _, ag := range agent.All {
		for _, managed := range []bool{false, true} {
			for _, command := range []string{"agent", "generic", "both", "non-executable-agent"} {
				t.Run(fmt.Sprintf("%s/managed=%t/%s", ag.Name, managed, command), func(t *testing.T) {
					f := newFixture(t)
					name, status := "existing", "running"
					if managed {
						name, status = sandbox.Name(ag.Name, f.project), "stopped"
					}
					dir := f.seedSandbox(t, name, ag.Name, status, []string{f.project}, false)
					bin := filepath.Join(dir, "usr/local/bin")
					if err := os.MkdirAll(bin, 0700); err != nil {
						t.Fatal(err)
					}
					want := filepath.Join(bin, ag.Name+"-acp")
					if command != "generic" {
						if command == "non-executable-agent" {
							if err := os.WriteFile(want, []byte("not executable"), 0600); err != nil {
								t.Fatal(err)
							}
						} else if err := os.Symlink(fake, want); err != nil {
							t.Fatal(err)
						}
					}
					if command != "agent" {
						generic := filepath.Join(bin, "acp")
						if err := os.Symlink(fake, generic); err != nil {
							t.Fatal(err)
						}
						if command != "both" {
							want = generic
						}
					}
					out, diag, code := f.run(t, []byte("setup must not read this"), "prepare", ag.Name)
					if code != 0 || string(out) != name+"\n" {
						t.Fatalf("prepare %d %q %s", code, out, diag)
					}
					for _, call := range f.calls(t) {
						if call[0] == "exec" && call[1] == "-i" {
							t.Fatal("prepare started ACP")
						}
					}
					payload := "{\"jsonrpc\":\"2.0\"}\n"
					out, diag, code = f.run(t, []byte(payload), "run", ag.Name, "--mode=reuse", "--add-acp-kit")
					if code != 0 || string(out) != payload {
						t.Fatalf("run %d %q %s", code, out, diag)
					}
					calls := f.calls(t)
					last := calls[len(calls)-1]
					if last[0] != "exec" || last[1] != "-i" || last[5] != want {
						t.Fatalf("wrong ACP command: %q, want %q", last, want)
					}
					if count(calls, "create") != 0 || count(calls, "kit") != 0 || count(calls, "rm") != 0 {
						t.Fatal("changed sandbox despite an available ACP command")
					}
				})
			}
		}
	}
}

func TestDiscoverMissingACPKitRequiresFlag(t *testing.T) {
	for _, ag := range agent.All {
		for _, command := range []string{"prepare", "run"} {
			t.Run(ag.Name+"/"+command, func(t *testing.T) {
				f := newFixture(t)
				dir := f.seedSandbox(t, "existing", ag.Name, "running", []string{filepath.Dir(f.project)}, false)
				f.seedSandbox(t, sandbox.Name(ag.Name, f.project), ag.Name, "running", []string{f.project}, true)
				args := []string{command, ag.Name}
				if command == "run" {
					args = append(args, "--mode=reuse")
				}
				out, diag, code := f.run(t, []byte("ACP input"), args...)
				if code == 0 || len(out) != 0 || !strings.Contains(diag, "existing") || !strings.Contains(diag, "--add-acp-kit") {
					t.Fatalf("missing-kit guidance %d %q %s", code, out, diag)
				}
				calls := f.calls(t)
				if count(calls, "kit") != 0 || count(calls, "create") != 0 || count(calls, "rm") != 0 || read(t, filepath.Join(dir, "keep")) != "sandbox-local data" {
					t.Fatal("changed a sandbox without --add-acp-kit")
				}
				for _, call := range calls {
					if call[0] == "exec" && call[1] == "-i" {
						t.Fatal("started ACP in another sandbox")
					}
				}
			})
		}
	}
}

func TestDiscoverAddsMissingACPKit(t *testing.T) {
	for _, ag := range agent.All {
		t.Run(ag.Name, func(t *testing.T) {
			f := newFixture(t)
			dir := f.seedSandbox(t, "existing", ag.Name, "running", []string{filepath.Dir(f.project)}, false)
			managed := f.seedSandbox(t, sandbox.Name(ag.Name, f.project), ag.Name, "running", []string{f.project}, true)
			// The closest workspace wins even when its ACP kit is missing.
			f.seedSandbox(t, "parent-with-acp", ag.Name, "running", []string{filepath.Dir(filepath.Dir(f.project))}, true)
			out, diag, code := f.run(t, []byte("setup must not read this"), "prepare", ag.Name, "--add-acp-kit")
			if code != 0 || string(out) != "existing\n" || !strings.Contains(diag, "Adding ACP kit") {
				t.Fatalf("prepare %d %q %s", code, out, diag)
			}
			if got := read(t, filepath.Join(dir, "kit")); got != ag.Kit {
				t.Fatalf("added %q, want %q", got, ag.Kit)
			}
			for _, call := range f.calls(t) {
				if call[0] == "exec" && call[1] == "-i" {
					t.Fatal("prepare started ACP")
				}
			}
			payload := "{\"jsonrpc\":\"2.0\"}\n"
			out, diag, code = f.run(t, []byte(payload), "run", ag.Name, "--mode=reuse")
			if code != 0 || string(out) != payload || !strings.Contains(diag, "Connecting to existing") {
				t.Fatalf("run %d %q %s", code, out, diag)
			}
			calls := f.calls(t)
			if count(calls, "kit") != 1 || count(calls, "create") != 0 || count(calls, "rm") != 0 || read(t, filepath.Join(dir, "keep")) != "sandbox-local data" {
				t.Fatal("reinstalled kit, created another sandbox, or lost existing data")
			}
			if read(t, filepath.Join(managed, "keep")) != "sandbox-local data" {
				t.Fatal("modified the previous managed sandbox")
			}
		})
	}
}

func TestDiscoverKitSetupFailureNeverCreates(t *testing.T) {
	for _, scenario := range []string{"add", "launcher"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			dir := f.seedSandbox(t, "existing", "codex", "running", []string{f.project}, false)
			if scenario == "add" {
				f.env = append(f.env, "FAKE_KIT_ADD_FAIL=1")
			} else {
				f.env = append(f.env, "FAKE_NO_BINARY=1")
			}
			out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=reuse", "--add-acp-kit")
			if code == 0 || len(out) != 0 || !strings.Contains(diag, "existing") || count(f.calls(t), "create") != 0 || count(f.calls(t), "rm") != 0 || read(t, filepath.Join(dir, "keep")) != "sandbox-local data" {
				t.Fatalf("setup failure %d %q %s", code, out, diag)
			}
			for _, call := range f.calls(t) {
				if call[0] == "exec" && call[1] == "-i" {
					t.Fatal("started ACP after setup failure")
				}
			}
		})
	}
}

func TestDiscoverAddsConfiguredACPKitOnly(t *testing.T) {
	f := newFixture(t)
	dir := f.seedSandbox(t, "existing", "codex", "running", []string{f.project}, false)
	config := `{"kits":["extra-common"],"agents":{"codex":{"acp_kit":"custom-acp","kits":["extra-agent"]}}}`
	if err := os.WriteFile(filepath.Join(f.project, ".sbx-connect.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	out, diag, code := f.run(t, nil, "prepare", "codex", "--add-acp-kit", "--add-kit", "extra-cli", "--template", "ignored")
	if code != 0 || string(out) != "existing\n" || read(t, filepath.Join(dir, "kit")) != "custom-acp" {
		t.Fatalf("configured kit %d %q %s", code, out, diag)
	}
	if calls := f.calls(t); count(calls, "kit") != 1 || count(calls, "create") != 0 {
		t.Fatalf("applied extra kits or created a sandbox: %q", calls)
	}
}

func TestConcurrentDiscoveryAddsKitOnce(t *testing.T) {
	f := newFixture(t)
	f.seedSandbox(t, "existing", "codex", "running", []string{f.project}, false)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=reuse", "--add-acp-kit")
			if code != 0 || string(out) != "ACP input" || !strings.Contains(diag, "Connecting to existing") {
				t.Errorf("concurrent reuse %d %q %s", code, out, diag)
			}
		})
	}
	wg.Wait()
	if calls := f.calls(t); count(calls, "kit") != 1 || count(calls, "create") != 0 {
		t.Fatalf("concurrent setup was not serialized: %q", calls)
	}
}

func TestDiscoverRunningSandbox(t *testing.T) {
	for _, ag := range []string{"codex", "claude"} {
		t.Run(ag, func(t *testing.T) {
			f := newFixture(t)
			f.seedSandbox(t, "parent", ag, "running", []string{filepath.Dir(f.project)}, true)
			f.seedSandbox(t, "z-exact", ag, "running", []string{f.project}, true)
			selected := f.seedSandbox(t, "a-exact", ag, "running", []string{f.project}, true)
			out, diag, code := f.run(t, nil, "prepare", ag, "--add-acp-kit", "--kit", "ignored", "--template", "ignored")
			if code != 0 || string(out) != "a-exact\n" {
				t.Fatalf("prepare %d %q %s", code, out, diag)
			}
			for _, call := range f.calls(t) {
				if call[0] == "exec" && call[1] == "-i" {
					t.Fatal("prepare started an ACP session")
				}
			}
			payload := "{\"jsonrpc\":\"2.0\"}\n"
			out, diag, code = f.run(t, []byte(payload), "run", ag, "--mode=reuse")
			if code != 0 || string(out) != payload || !strings.Contains(diag, "Connecting to a-exact") {
				t.Fatalf("run %d %q %s", code, out, diag)
			}
			calls := f.calls(t)
			if count(calls, "create") != 0 || count(calls, "rm") != 0 || count(calls, "kit") != 0 || read(t, filepath.Join(selected, "keep")) != "sandbox-local data" {
				t.Fatal("modified borrowed sandbox")
			}
			last := calls[len(calls)-1]
			if last[0] != "exec" || last[1] != "-i" || last[3] != f.project || last[4] != "a-exact" {
				t.Fatalf("wrong ACP target or workdir: %q", last)
			}
			out, diag, code = f.run(t, nil, "list")
			if code != 0 || len(out) != 0 {
				t.Fatalf("borrowed names leaked into list: %d %q %s", code, out, diag)
			}
			for _, command := range [][]string{{"stop", "a-exact"}, {"remove", "a-exact", "--yes"}} {
				_, _, code = f.run(t, nil, command...)
				if code == 0 {
					t.Fatal("lifecycle accepted borrowed name")
				}
			}
		})
	}
}

func TestDiscoveryFallback(t *testing.T) {
	for _, scenario := range []string{"agent", "stopped", "boundary", "readonly", "shadowed", "clone", "clone without ACP", "ephemeral"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			ag, status, name := "codex", "running", "existing"
			workspaces := []string{f.project}
			switch scenario {
			case "agent":
				ag = "claude"
			case "stopped":
				status = "stopped"
			case "boundary":
				workspaces = []string{f.project[:len(f.project)-1]}
			case "readonly":
				workspaces = []string{f.project + ":ro"}
			case "shadowed":
				workspaces = []string{filepath.Dir(f.project), f.project + ":ro"}
			case "ephemeral":
				var err error
				name, err = sandbox.EphemeralName(ag, f.project)
				if err != nil {
					t.Fatal(err)
				}
			}
			dir := f.seedSandbox(t, name, ag, status, workspaces, scenario != "clone without ACP")
			if strings.HasPrefix(scenario, "clone") {
				if err := os.Mkdir(filepath.Join(dir, "clone"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=reuse")
			if code != 0 || string(out) != "ACP input" || !strings.Contains(diag, "Connecting to "+sandbox.Name("codex", f.project)) {
				t.Fatalf("fallback %d %q %s", code, out, diag)
			}
			if count(f.calls(t), "create") != 1 || count(f.calls(t), "rm") != 0 || count(f.calls(t), "kit") != 0 || read(t, filepath.Join(dir, "keep")) != "sandbox-local data" || read(t, filepath.Join(dir, "status")) != status {
				t.Fatal("fallback changed existing sandbox")
			}
		})
	}
}

func TestDiscoverParentAndAdditionalMount(t *testing.T) {
	f := newFixture(t)
	f.seedSandbox(t, "existing", "codex", "running", []string{"/unrelated:ro", filepath.Dir(f.project)}, true)
	out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=reuse")
	if code != 0 || string(out) != "ACP input" || !strings.Contains(diag, "Connecting to existing") || count(f.calls(t), "create") != 0 {
		t.Fatalf("parent mount %d %q %s", code, out, diag)
	}
	last := f.calls(t)[len(f.calls(t))-1]
	if last[3] != f.project {
		t.Fatalf("ACP must start in project subdirectory: %q", last)
	}
}

func TestDiscoveryFailureNeverCreates(t *testing.T) {
	for _, scenario := range []string{"malformed", "missing-array", "probe"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			switch scenario {
			case "malformed":
				f.env = append(f.env, "FAKE_LIST_JSON=not JSON")
			case "missing-array":
				f.env = append(f.env, "FAKE_LIST_JSON={}")
			case "probe":
				f.seedSandbox(t, "existing", "codex", "running", []string{f.project}, true)
				f.env = append(f.env, "FAKE_PROBE_FAIL=1")
			}
			out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=reuse")
			if code == 0 || len(out) != 0 || count(f.calls(t), "create") != 0 || count(f.calls(t), "rm") != 0 {
				t.Fatalf("failure created sandbox: %d %q %s", code, out, diag)
			}
		})
	}
}

func TestDisposableRunDoesNotBorrow(t *testing.T) {
	f := newFixture(t)
	dir := f.seedSandbox(t, "existing", "codex", "running", []string{f.project}, true)
	out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=ephemeral")
	if code != 0 || string(out) != "ACP input" || count(f.calls(t), "create") != 1 || count(f.calls(t), "rm") != 1 || read(t, filepath.Join(dir, "keep")) != "sandbox-local data" {
		t.Fatalf("disposable run %d %q %s", code, out, diag)
	}
	for _, call := range f.calls(t) {
		if call[0] == "exec" && call[4] == "existing" || call[0] == "rm" && call[2] == "existing" {
			t.Fatalf("borrowed or removed existing sandbox: %q", call)
		}
		if call[0] == "ls" && call[1] == "--json" {
			t.Fatal("disposable run performed discovery")
		}
	}
}

func TestDefaultModeReusesDiscoveredSandbox(t *testing.T) {
	f := newFixture(t)
	dir := f.seedSandbox(t, "existing", "codex", "running", []string{f.project}, true)
	out, diag, code := f.run(t, []byte("ACP input"), "run", "codex")
	if code != 0 || string(out) != "ACP input" || !strings.Contains(diag, "Connecting to existing") {
		t.Fatalf("default mode %d %q %s", code, out, diag)
	}
	if count(f.calls(t), "create") != 0 || count(f.calls(t), "rm") != 0 || read(t, filepath.Join(dir, "keep")) != "sandbox-local data" {
		t.Fatalf("default mode modified borrowed sandbox: %q", f.calls(t))
	}
	var sawDiscovery bool
	for _, call := range f.calls(t) {
		if call[0] == "ls" && call[1] == "--json" {
			sawDiscovery = true
		}
	}
	if !sawDiscovery {
		t.Fatal("default mode did not perform discovery")
	}
}

func TestAutoModeBorrowsDiscoveredSandbox(t *testing.T) {
	f := newFixture(t)
	dir := f.seedSandbox(t, "existing", "codex", "running", []string{f.project}, true)
	out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=auto")
	if code != 0 || string(out) != "ACP input" || !strings.Contains(diag, "Connecting to existing") {
		t.Fatalf("auto borrow %d %q %s", code, out, diag)
	}
	if count(f.calls(t), "create") != 0 || count(f.calls(t), "rm") != 0 || read(t, filepath.Join(dir, "keep")) != "sandbox-local data" {
		t.Fatalf("auto mode modified borrowed sandbox: %q", f.calls(t))
	}
	var sawDiscovery bool
	for _, call := range f.calls(t) {
		if call[0] == "ls" && call[1] == "--json" {
			sawDiscovery = true
		}
	}
	if !sawDiscovery {
		t.Fatal("auto mode did not perform discovery")
	}
}

func TestAutoModeFallsBackToDisposableSandbox(t *testing.T) {
	f := newFixture(t)
	out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=auto")
	if code != 0 || string(out) != "ACP input" {
		t.Fatalf("auto fallback %d %q %s", code, out, diag)
	}
	var created, removed string
	var sawDiscovery bool
	for _, c := range f.calls(t) {
		switch {
		case c[0] == "ls" && c[1] == "--json":
			sawDiscovery = true
		case c[0] == "create":
			created = c[2]
		case c[0] == "rm":
			removed = c[2]
		}
	}
	if !sawDiscovery {
		t.Fatal("auto mode fallback did not perform discovery")
	}
	if created == "" || removed != created {
		t.Fatalf("created %q removed %q calls %q", created, removed, f.calls(t))
	}
	if created == sandbox.Name("codex", f.project) {
		t.Fatalf("fallback used persistent sandbox name %s", created)
	}
	if _, err := os.Stat(filepath.Join(f.state, "sandboxes", created)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestInvalidModeRejected(t *testing.T) {
	f := newFixture(t)
	out, diag, code := f.run(t, nil, "run", "codex", "--mode=bogus")
	if code == 0 || len(out) != 0 || !strings.Contains(diag, `invalid --mode "bogus"`) {
		t.Fatalf("%d %q %s", code, out, diag)
	}
	if _, err := os.Stat(filepath.Join(f.state, "calls")); !os.IsNotExist(err) {
		t.Fatal("mode validation should not touch sbx")
	}
}
