package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"sbx-connect/internal/agent"
	"sbx-connect/internal/sandbox"
)

var launcher, fake, buildDir string

func TestMain(m *testing.M) {
	var err error
	buildDir, err = os.MkdirTemp("", "sbx-connect-test-build-")
	if err != nil {
		panic(err)
	}
	launcher, fake = filepath.Join(buildDir, "sbx-connect"), filepath.Join(buildDir, "sbx")
	for _, build := range []struct{ output, source string }{{launcher, "../.."}, {fake, "testdata/fakesbx.go"}} {
		args := []string{"build", "-o", build.output}
		if raceEnabled && build.output == launcher {
			args = append(args, "-race")
		}
		cmd := exec.Command("go", append(args, build.source)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build: %s %v", out, err)
			os.RemoveAll(buildDir)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(buildDir)
	os.Exit(code)
}

type fixture struct {
	state, project, cache string
	env                   []string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{state: filepath.Join(root, "state"), project: filepath.Join(root, "project with spaces ' $();"), cache: filepath.Join(root, "cache")}
	for _, dir := range []string{f.state, f.project, f.cache} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.env = []string{"FAKE_STATE=" + f.state, "XDG_CACHE_HOME=" + f.cache, "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "SBX_CONNECT_CONFIG=", "HOME=" + root, "SBX_CONNECT_SBX_BIN=" + fake, "PATH=" + buildDir + ":" + os.Getenv("PATH"), "GORACE=atexit_sleep_ms=0"}
	return f
}
func (f fixture) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, launcher, args...)
	cmd.Dir = f.project
	cmd.Env = append(os.Environ(), f.env...)
	return cmd
}
func (f fixture) run(t *testing.T, input []byte, args ...string) ([]byte, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := f.command(ctx, args...)
	cmd.Stdin = bytes.NewReader(input)
	var out, diag bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diag
	err := cmd.Run()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("timed out: %s", diag.String())
	}
	return out.Bytes(), diag.String(), code
}
func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func (f fixture) calls(t *testing.T) [][]string {
	t.Helper()
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(read(t, filepath.Join(f.state, "calls"))), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, args)
	}
	return calls
}
func count(calls [][]string, verb string) int {
	n := 0
	for _, c := range calls {
		if c[0] == verb {
			n++
		}
	}
	return n
}

func TestProvisionReuseStoppedAndKits(t *testing.T) {
	for _, ag := range []string{"codex", "claude"} {
		t.Run(ag, func(t *testing.T) {
			f := newFixture(t)
			name := sandbox.Name(ag, f.project)
			out, diag, code := f.run(t, nil, "prepare", ag, "--template", "custom template")
			if code != 0 || string(out) != name+"\n" {
				t.Fatalf("%d %q %s", code, out, diag)
			}
			out, diag, code = f.run(t, nil, "stop", name)
			if code != 0 {
				t.Fatal(diag)
			}
			if s := read(t, filepath.Join(f.state, "sandboxes", name, "status")); s != "stopped" {
				t.Fatal(s)
			}
			payload := append([]byte("{\"jsonrpc\":\"2.0\"}\n"), 0, 255, '\r', '\n')
			out, diag, code = f.run(t, payload, "run", ag, "--mode=reuse", "--template", "ignored")
			if code != 0 || !bytes.Equal(out, payload) {
				t.Fatalf("stdio %d %q %s", code, out, diag)
			}
			if s := read(t, filepath.Join(f.state, "sandboxes", name, "status")); s != "running" {
				t.Fatal(s)
			}
			_, diag, code = f.run(t, nil, "prepare", ag, "--kit", "docker.io/example/custom-kit:2")
			if code != 0 {
				t.Fatal(diag)
			}
			if n := count(f.calls(t), "create"); n != 1 {
				t.Fatalf("create count %d", n)
			}
			definition, err := agent.Lookup(ag)
			if err != nil {
				t.Fatal(err)
			}
			if got := read(t, filepath.Join(f.state, "sandboxes", name, "kit")); got != definition.Kit {
				t.Fatalf("kit changed during reuse: %s", got)
			}
			for _, c := range f.calls(t) {
				if c[0] == "create" && !reflect.DeepEqual(c, []string{"create", "--name", name, "--kit", definition.Kit, "--template", "custom template", ag, f.project}) {
					t.Fatal(c)
				}
				binary := filepath.Join(f.state, "sandboxes", name, "home/.local/bin", ag+"-acp")
				if c[0] == "exec" && c[1] == "-i" && !reflect.DeepEqual(c, []string{"exec", "-i", "--workdir", f.project, name, binary}) {
					t.Fatal(c)
				}
			}
		})
	}
}

func TestEphemeralCreatesAndRemovesSandbox(t *testing.T) {
	f := newFixture(t)
	persistent := sandbox.Name("codex", f.project)
	out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=ephemeral")
	if code != 0 || string(out) != "ACP input" {
		t.Fatalf("%d %q %s", code, out, diag)
	}
	var created, removed string
	for _, c := range f.calls(t) {
		switch c[0] {
		case "create":
			created = c[2]
		case "rm":
			if len(c) != 3 || c[1] != "--force" {
				t.Fatalf("remove call = %q", c)
			}
			removed = c[2]
		}
	}
	if created == "" || removed != created {
		t.Fatalf("created %q removed %q calls %q", created, removed, f.calls(t))
	}
	if created == persistent {
		t.Fatalf("used persistent sandbox name %s", created)
	}
	if _, err := os.Stat(filepath.Join(f.state, "sandboxes", created)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestEphemeralRetriesRemoval(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "FAKE_RM_FAILS=2")
	out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=ephemeral")
	if code != 0 || string(out) != "ACP input" {
		t.Fatalf("%d %q %s", code, out, diag)
	}
	var created string
	for _, c := range f.calls(t) {
		if c[0] == "create" {
			created = c[2]
		}
	}
	if created == "" {
		t.Fatalf("no sandbox created: %q", f.calls(t))
	}
	if got := count(f.calls(t), "rm"); got != 3 {
		t.Fatalf("rm count %d calls %q", got, f.calls(t))
	}
	if _, err := os.Stat(filepath.Join(f.state, "sandboxes", created)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestEphemeralStopsWhenSandboxNoLongerManaged(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "FAKE_RM_FAILS=1", "FAKE_RM_DELETE_ON_FAIL=1")
	out, diag, code := f.run(t, []byte("ACP input"), "run", "codex", "--mode=ephemeral")
	if code != 0 || string(out) != "ACP input" {
		t.Fatalf("%d %q %s", code, out, diag)
	}
	var created string
	for _, c := range f.calls(t) {
		if c[0] == "create" {
			created = c[2]
		}
	}
	if created == "" {
		t.Fatalf("no sandbox created: %q", f.calls(t))
	}
	if got := count(f.calls(t), "rm"); got != 1 {
		t.Fatalf("rm count %d calls %q", got, f.calls(t))
	}
	if _, err := os.Stat(filepath.Join(f.state, "sandboxes", created)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestDebugLifecycleLogs(t *testing.T) {
	f := newFixture(t)
	out, diag, code := f.run(t, []byte("ACP input"), "--debug", "run", "codex", "--mode=ephemeral")
	if code != 0 || string(out) != "ACP input" {
		t.Fatalf("%d %q %s", code, out, diag)
	}
	for _, want := range []string{
		"sbx-connect debug: lifecycle run start",
		"sbx-connect debug: lifecycle create start",
		"sbx-connect debug: lifecycle acp exit",
		"sbx-connect debug: lifecycle cleanup remove done",
		"sbx-connect debug: lifecycle run done",
	} {
		if !strings.Contains(diag, want) {
			t.Fatalf("missing debug log %q:\n%s", want, diag)
		}
	}
}

func TestDebugLifecycleLogsFromEnvironment(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "SBX_CONNECT_DEBUG=1")
	out, diag, code := f.run(t, nil, "prepare", "codex")
	if code != 0 || !strings.HasPrefix(string(out), "sbx-connect-codex-") {
		t.Fatalf("%d %q %s", code, out, diag)
	}
	for _, want := range []string{
		"sbx-connect debug: lifecycle prepare start",
		"sbx-connect debug: lifecycle discover start",
		"sbx-connect debug: lifecycle launcher-check done",
	} {
		if !strings.Contains(diag, want) {
			t.Fatalf("missing debug log %q:\n%s", want, diag)
		}
	}
}

func TestListingFailureDoesNotCreate(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "FAKE_LIST_FAIL=1")
	out, diag, code := f.run(t, nil, "run", "codex")
	if code != 17 || len(out) != 0 || !strings.Contains(diag, "service") {
		t.Fatalf("%d %q %s", code, out, diag)
	}
	if calls := f.calls(t); len(calls) != 1 || calls[0][0] != "ls" {
		t.Fatal(calls)
	}
}

func TestKitCreationFailureRetries(t *testing.T) {
	f := newFixture(t)
	bad := f
	bad.env = append(append([]string{}, f.env...), "FAKE_CREATE_FAIL=1")
	out, diag, code := bad.run(t, nil, "run", "codex", "--mode=reuse")
	if code != 23 || len(out) != 0 || !strings.Contains(diag, "kit provisioning failed") {
		t.Fatalf("%d %q %s", code, out, diag)
	}
	if count(f.calls(t), "exec") != 0 {
		t.Fatal("launched after failed creation")
	}
	_, diag, code = f.run(t, nil, "prepare", "codex")
	if code != 0 || count(f.calls(t), "create") != 2 {
		t.Fatalf("retry: %d %s", code, diag)
	}
}

func TestMissingKitLauncherPreservesSandbox(t *testing.T) {
	for _, ag := range []string{"codex", "claude"} {
		t.Run(ag, func(t *testing.T) {
			f := newFixture(t)
			name := sandbox.Name(ag, f.project)
			home := filepath.Join(f.state, "sandboxes", name, "home")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			data := filepath.Join(home, "existing-session")
			if err := os.WriteFile(data, []byte("keep me"), 0600); err != nil {
				t.Fatal(err)
			}
			out, diag, code := f.run(t, []byte("ACP input"), "run", ag, "--mode=reuse")
			if code == 0 || len(out) != 0 || !strings.Contains(diag, "sbx-connect remove "+name+" --yes") {
				t.Fatalf("%d %q %s", code, out, diag)
			}
			if read(t, data) != "keep me" || count(f.calls(t), "create") != 0 || count(f.calls(t), "rm") != 0 {
				t.Fatal("modified existing sandbox")
			}
			for _, c := range f.calls(t) {
				if c[0] == "exec" && c[1] == "-i" {
					t.Fatal("started ACP without a kit launcher")
				}
			}
		})
	}
}

func TestPrepareRequiresKitLauncher(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "FAKE_NO_BINARY=1")
	out, diag, code := f.run(t, nil, "prepare", "codex")
	if code == 0 || len(out) != 0 || !strings.Contains(diag, "cannot verify ACP kit launcher") {
		t.Fatalf("%d %q %s", code, out, diag)
	}
}

func TestCustomKitAndVersion(t *testing.T) {
	f := newFixture(t)
	kit := "docker.io/example/custom-kit@sha256:" + strings.Repeat("a", 64)
	_, diag, code := f.run(t, nil, "prepare", "codex", "--kit", kit)
	if code != 0 {
		t.Fatal(diag)
	}
	name := sandbox.Name("codex", f.project)
	if got := read(t, filepath.Join(f.state, "sandboxes", name, "kit")); got != kit {
		t.Fatal(got)
	}
	before := len(f.calls(t))
	out, diag, code := f.run(t, nil, "version")
	if code != 0 || len(f.calls(t)) != before {
		t.Fatalf("%d %s", code, diag)
	}
	for _, a := range agent.All {
		if !strings.Contains(string(out), a.Name+" "+a.Kit) {
			t.Fatalf("missing kit in version: %s", out)
		}
	}
}

func TestArgumentsAndExitStatus(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "FAKE_ADAPTER_MODE=args")
	want := []string{"--project", "not launcher project", "--adapter-version", "latest", "", "quoted ' \" $();", "--", "--help"}
	args := append([]string{"run", "codex", "--mode=reuse", "--project", f.project, "--"}, want...)
	out, diag, code := f.run(t, nil, args...)
	var got []string
	if err := json.Unmarshal(out, &got); err != nil || code != 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("%d got %q want %q: %s", code, out, want, diag)
	}
	f.env = append(f.env, "FAKE_ADAPTER_MODE=exit")
	out, diag, code = f.run(t, nil, "run", "codex", "--mode=reuse")
	if code != 42 || len(out) != 0 {
		t.Fatalf("%d %q %s", code, out, diag)
	}
}

func TestConcurrentLaunches(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "FAKE_SLOW=1")
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			out, diag, code := f.run(t, []byte("hello"), "run", "codex", "--mode=reuse")
			if code != 0 || string(out) != "hello" {
				t.Errorf("%d %q %s", code, out, diag)
			}
		})
	}
	wg.Wait()
	if count(f.calls(t), "create") != 1 {
		t.Fatal("creation was not serialized")
	}
}

func awaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func TestSignalsAndProvisionLockReleasedBeforeStream(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			f := newFixture(t)
			f.env = append(f.env, "FAKE_ADAPTER_MODE=signal")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := f.command(ctx, "run", "claude", "--mode=reuse")
			var diag bytes.Buffer
			cmd.Stderr = &diag
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			awaitFile(t, filepath.Join(f.state, "ready"))
			// Another prepare must complete while the first ACP session stays alive.
			_, d, code := f.run(t, nil, "prepare", "claude")
			if code != 0 {
				t.Fatal(d)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err := cmd.Wait()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 128+int(sig) {
				t.Fatalf("%v %s", err, diag.String())
			}
			if got := read(t, filepath.Join(f.state, "signal")); got != sig.String() {
				t.Fatal(got)
			}
		})
	}
}

func TestSignalRemovesEphemeralSandbox(t *testing.T) {
	f := newFixture(t)
	f.env = append(f.env, "FAKE_ADAPTER_MODE=signal")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := f.command(ctx, "run", "codex", "--mode=ephemeral")
	var diag bytes.Buffer
	cmd.Stderr = &diag
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	awaitFile(t, filepath.Join(f.state, "ready"))
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 128+int(syscall.SIGTERM) {
		t.Fatalf("%v %s", err, diag.String())
	}
	var created, removed string
	for _, call := range f.calls(t) {
		switch call[0] {
		case "create":
			created = call[2]
		case "rm":
			if len(call) != 3 || call[1] != "--force" {
				t.Fatalf("remove call = %q", call)
			}
			removed = call[2]
		}
	}
	if created == "" || removed != created {
		t.Fatalf("created %q removed %q calls %q", created, removed, f.calls(t))
	}
	if _, err := os.Stat(filepath.Join(f.state, "sandboxes", created)); !os.IsNotExist(err) {
		t.Fatalf("ephemeral sandbox remains after shutdown: %v", err)
	}
}

func TestLifecycleAndDoctor(t *testing.T) {
	f := newFixture(t)
	_, diag, code := f.run(t, nil, "prepare", "codex")
	if code != 0 {
		t.Fatal(diag)
	}
	name := sandbox.Name("codex", f.project)
	for _, foreign := range []string{"zed-codex-old", "sbx-connect-codex-invalid", "somebody-elses"} {
		if err := os.MkdirAll(filepath.Join(f.state, "sandboxes", foreign), 0700); err != nil {
			t.Fatal(err)
		}
	}
	out, diag, code := f.run(t, nil, "list")
	if code != 0 || string(out) != name+"\n" {
		t.Fatalf("%d %q %s", code, out, diag)
	}
	before := len(f.calls(t))
	out, diag, code = f.run(t, nil, "doctor")
	if code != 0 || !strings.Contains(string(out), "Preflight passed") {
		t.Fatalf("%d %s %s", code, out, diag)
	}
	for _, call := range f.calls(t)[before:] {
		if call[0] != "version" && call[0] != "ls" {
			t.Fatalf("doctor mutation: %q", call)
		}
	}
	for _, args := range [][]string{{"remove", name}, {"remove", "zed-codex-old", "--yes"}, {"remove", "--yes"}, {"stop", "somebody-elses"}} {
		_, _, code = f.run(t, nil, args...)
		if code == 0 {
			t.Fatalf("accepted %q", args)
		}
	}
	_, diag, code = f.run(t, nil, "remove", name, "--yes")
	if code != 0 {
		t.Fatal(diag)
	}
	if _, err := os.Stat(filepath.Join(f.state, "sandboxes", name)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestZedCLIPrintAndInstall(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	out, diag, code := f.run(t, nil, "install", "zed", "--settings", path, "--print")
	if code != 0 {
		t.Fatal(diag)
	}
	var settings struct {
		Agents map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
		} `json:"agent_servers"`
	}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatal(err)
	}
	for _, entry := range settings.Agents {
		if entry.Command != launcher || entry.Env["SBX_CONNECT_SBX_BIN"] != fake {
			t.Fatalf("%+v", entry)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("--print wrote settings")
	}
	out, diag, code = f.run(t, nil, "install", "zed", "--settings", path, "--ephemeral", "--print")
	if code != 0 {
		t.Fatal(diag)
	}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatal(err)
	}
	entry := settings.Agents["Codex in Ephemeral Docker Sandbox"]
	if !reflect.DeepEqual(entry.Args, []string{"run", "codex", "--mode=ephemeral"}) {
		t.Fatalf("ephemeral args: %+v", entry)
	}
	_, diag, code = f.run(t, nil, "install", "zed", "--settings", path)
	if code != 0 {
		t.Fatal(diag)
	}
	out, diag, code = f.run(t, nil, "install", "zed", "--settings", path)
	if code != 0 || !strings.Contains(string(out), "Already installed") {
		t.Fatalf("%d %s %s", code, out, diag)
	}
}

func TestInputValidationAndSBXPrecedence(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"run"}, {"run", "other"}, {"prepare", "codex", "--adapter-version", "latest"}, {"prepare", "codex", "--adapter-version", "^1.2.3"}, {"prepare", "codex", "--project", "/"}, {"prepare", "codex", "extra"}, {"doctor", "extra"}} {
		_, _, code := f.run(t, nil, args...)
		if code == 0 {
			t.Fatalf("accepted %q", args)
		}
	}
	if _, err := os.Stat(filepath.Join(f.state, "calls")); !os.IsNotExist(err) {
		t.Fatal("validation invoked sbx")
	}
	f.env = append(f.env, "SBX_CONNECT_SBX_BIN=/does/not/exist")
	_, diag, code := f.run(t, nil, "--sbx-bin", fake, "prepare", "codex")
	if code != 0 {
		t.Fatal(diag)
	}
}

func TestConfiguredExtraKits(t *testing.T) {
	f := newFixture(t)
	configPath := filepath.Join(f.project, ".sbx-connect.json")
	data := `{"kits":["common","duplicate","./local-kit"],"agents":{"codex":{"kits":["codex-extra"],"acp_kit":"configured-acp"},"claude":{"kits":["claude-extra"]}}}`
	if err := os.WriteFile(configPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cliKit := "git+https://example.test/repo#dir=with,comma ' $();"
	for _, ag := range []string{"codex", "claude"} {
		// Match the non-ephemeral Zed entries: --mode=reuse, no per-project flags or explicit config required.
		out, diag, code := f.run(t, []byte("ACP input"), "run", ag, "--mode=reuse", "--add-kit", cliKit, "--add-kit", "duplicate", "--add-kit", "last")
		if code != 0 || string(out) != "ACP input" {
			t.Fatalf("%d %q %s", code, out, diag)
		}
		definition, _ := agent.Lookup(ag)
		acp := definition.Kit
		if ag == "codex" {
			acp = "configured-acp"
		}
		want := []string{acp, "common", "duplicate", filepath.Join(f.project, "local-kit"), ag + "-extra", cliKit, "last"}
		var got []string
		name := sandbox.Name(ag, f.project)
		if err := json.Unmarshal([]byte(read(t, filepath.Join(f.state, "sandboxes", name, "kits"))), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("kits got %q want %q", got, want)
		}
	}
	if err := os.WriteFile(configPath, []byte(`{"kits":["changed"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, diag, code := f.run(t, nil, "prepare", "codex")
	if code != 0 || count(f.calls(t), "create") != 2 || !strings.Contains(diag, "only affect new sandboxes") {
		t.Fatalf("reuse %d %s", code, diag)
	}
}

func TestConfigFlagsAndEditorRegistration(t *testing.T) {
	f := newFixture(t)
	configPath := filepath.Join(f.project, "custom config.json")
	if err := os.WriteFile(configPath, []byte(`{"kits":["extra"],"agents":{"codex":{"acp_kit":"configured-acp"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "SBX_CONNECT_CONFIG=/missing/config.json")
	out, diag, code := f.run(t, nil, "--config", "custom config.json", "install", "zed", "--print")
	if code != 0 {
		t.Fatal(diag)
	}
	var settings struct {
		Agents map[string]struct {
			Args []string
			Env  map[string]string
		} `json:"agent_servers"`
	}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatal(err)
	}
	definition, _ := agent.Lookup("codex")
	entry := settings.Agents[definition.ZedName]
	if entry.Env["SBX_CONNECT_CONFIG"] != configPath {
		t.Fatalf("config path not made absolute: %s", out)
	}
	f.env = append(f.env, "SBX_CONNECT_CONFIG="+entry.Env["SBX_CONNECT_CONFIG"])
	// Use the emitted editor args and environment, plus an explicit ACP override.
	_, diag, code = f.run(t, nil, append(entry.Args, "--kit", "cli-acp")...)
	if code != 0 {
		t.Fatal(diag)
	}
	name := sandbox.Name("codex", f.project)
	if got := read(t, filepath.Join(f.state, "sandboxes", name, "kits")); got != `["cli-acp","extra"]` {
		t.Fatal(got)
	}
}

func TestInvalidKitConfigDoesNotInvokeSBX(t *testing.T) {
	for _, data := range []string{`{"kits":[""]}`, `{"kits":[123]}`, `{"agent":{"codex":{}}}`} {
		f := newFixture(t)
		if err := os.WriteFile(filepath.Join(f.project, ".sbx-connect.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		out, _, code := f.run(t, nil, "prepare", "codex")
		if code == 0 || len(out) != 0 {
			t.Fatalf("accepted invalid config %s", data)
		}
		if _, err := os.Stat(filepath.Join(f.state, "calls")); !os.IsNotExist(err) {
			t.Fatal("invalid config invoked sbx")
		}
	}
	f := newFixture(t)
	for _, args := range [][]string{{"prepare", "codex", "--add-kit", ""}, {"prepare", "codex", "--kit", " "}} {
		if _, _, code := f.run(t, nil, args...); code == 0 {
			t.Fatalf("accepted %q", args)
		}
	}
	if _, err := os.Stat(filepath.Join(f.state, "calls")); !os.IsNotExist(err) {
		t.Fatal("invalid kit flags invoked sbx")
	}
}

func TestUserConfigWithExplicitProject(t *testing.T) {
	f := newFixture(t)
	userConfig := filepath.Join(filepath.Dir(f.project), "config/sbx-connect/config.json")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte(`{"kits":["user-common"],"agents":{"codex":{"kits":["user-codex"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".sbx-connect.json"), []byte(`{"kits":["project-common"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, diag, code := f.run(t, nil, "prepare", "codex", "--project", project, "--kit", "acp")
	if code != 0 {
		t.Fatal(diag)
	}
	name := sandbox.Name("codex", project)
	if got := read(t, filepath.Join(f.state, "sandboxes", name, "kits")); got != `["acp","project-common","user-codex"]` {
		t.Fatal(got)
	}
}
