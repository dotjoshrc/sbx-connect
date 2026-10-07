// A deliberately strict fake sbx. It provisions fake kit launchers and executes
// them over stdio, exercising kit selection, reuse, and process handling.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func write(p, s string) { must(os.WriteFile(p, []byte(s), 0600)) }
func main() {
	state := os.Getenv("FAKE_STATE")
	args := os.Args[1:]
	base := filepath.Base(os.Args[0])
	if base == "acp" || base == "codex-acp" || base == "claude-acp" {
		if len(args) == 1 && args[0] == "--help" {
			if os.Getenv("FAKE_HELP_FAIL") != "" {
				os.Exit(24)
			}
			fmt.Println("adapter help")
			return
		}
		switch os.Getenv("FAKE_ADAPTER_MODE") {
		case "args":
			must(json.NewEncoder(os.Stdout).Encode(args))
		case "exit":
			os.Exit(42)
		case "signal":
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
			write(filepath.Join(state, "ready"), "ready")
			sig := <-ch
			write(filepath.Join(state, "signal"), sig.String())
			os.Exit(128 + int(sig.(syscall.Signal)))
		default:
			_, err := io.Copy(os.Stdout, os.Stdin)
			must(err)
		}
		return
	}
	log, err := os.OpenFile(filepath.Join(state, "calls"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	must(err)
	must(json.NewEncoder(log).Encode(args))
	log.Close()
	if args[0] != "exec" || args[1] != "-i" {
		data, err := io.ReadAll(os.Stdin)
		must(err)
		if len(data) != 0 {
			panic("setup consumed editor stdin")
		}
	}
	sandboxes := filepath.Join(state, "sandboxes")
	must(os.MkdirAll(sandboxes, 0700))
	switch args[0] {
	case "version":
		fmt.Println("fake-sbx 1.0")
	case "ls":
		if os.Getenv("FAKE_LIST_FAIL") != "" {
			fmt.Fprintln(os.Stderr, "service unavailable")
			os.Exit(17)
		}
		entries, err := os.ReadDir(sandboxes)
		must(err)
		if len(args) == 2 && args[1] == "--json" {
			if response := os.Getenv("FAKE_LIST_JSON"); response != "" {
				fmt.Println(response)
				return
			}
			rows := make([]map[string]any, 0, len(entries))
			for _, e := range entries {
				dir := filepath.Join(sandboxes, e.Name())
				agent, _ := os.ReadFile(filepath.Join(dir, "agent"))
				status, _ := os.ReadFile(filepath.Join(dir, "status"))
				// Legacy fixtures intentionally contain no metadata or launcher.
				if len(agent) == 0 {
					agent = []byte("shell")
				}
				if len(status) == 0 {
					status = []byte("stopped")
				}
				var workspaces []string
				if data, err := os.ReadFile(filepath.Join(dir, "workspaces")); err == nil {
					must(json.Unmarshal(data, &workspaces))
				}
				rows = append(rows, map[string]any{"name": e.Name(), "agent": string(agent), "status": string(status), "workspaces": workspaces})
			}
			must(json.NewEncoder(os.Stdout).Encode(map[string]any{"sandboxes": rows}))
			return
		}
		if len(args) != 2 || args[1] != "--quiet" {
			panic("expected structured or quiet listing")
		}
		for _, e := range entries {
			fmt.Println(e.Name())
		}
	case "create":
		if args[1] != "--name" {
			panic("missing name")
		}
		name := args[2]
		if _, err := os.Stat(filepath.Join(sandboxes, name)); err == nil {
			panic("duplicate create")
		}
		if args[3] != "--kit" || args[4] == "" {
			panic("missing kit")
		}
		i := 3
		var kits []string
		for args[i] == "--kit" {
			kits = append(kits, args[i+1])
			i += 2
		}
		if args[i] == "--template" {
			i += 2
		}
		if len(args) != i+2 || (args[i] != "codex" && args[i] != "claude") {
			panic("expected agent and project")
		}
		if os.Getenv("FAKE_CREATE_FAIL") != "" {
			fmt.Fprintln(os.Stderr, "kit provisioning failed")
			os.Exit(23)
		}
		if os.Getenv("FAKE_SLOW") != "" {
			time.Sleep(150 * time.Millisecond)
		}
		bin := filepath.Join(sandboxes, name, "home/.local/bin")
		must(os.MkdirAll(bin, 0700))
		if os.Getenv("FAKE_NO_BINARY") == "" {
			exe, err := os.Executable()
			must(err)
			must(os.Symlink(exe, filepath.Join(bin, args[i]+"-acp")))
		}
		write(filepath.Join(sandboxes, name, "kit"), args[4])
		kitJSON, err := json.Marshal(kits)
		must(err)
		write(filepath.Join(sandboxes, name, "kits"), string(kitJSON))
		write(filepath.Join(sandboxes, name, "status"), "running")
		write(filepath.Join(sandboxes, name, "agent"), args[i])
		workspaces, err := json.Marshal([]string{args[i+1]})
		must(err)
		write(filepath.Join(sandboxes, name, "workspaces"), string(workspaces))
		fmt.Println("create noise")
	case "kit":
		if len(args) != 4 || args[1] != "add" || args[3] == "" {
			panic("expected kit add SANDBOX REFERENCE")
		}
		dir := filepath.Join(sandboxes, args[2])
		agent, err := os.ReadFile(filepath.Join(dir, "agent"))
		must(err)
		if os.Getenv("FAKE_KIT_ADD_FAIL") != "" {
			fmt.Fprintln(os.Stderr, "kit addition failed")
			os.Exit(25)
		}
		if os.Getenv("FAKE_NO_BINARY") == "" {
			exe, err := os.Executable()
			must(err)
			must(os.Symlink(exe, filepath.Join(dir, "home", ".local", "bin", string(agent)+"-acp")))
		}
		write(filepath.Join(dir, "kit"), args[3])
		fmt.Println("kit add noise")
	case "exec":
		i := 1
		if args[i] == "-i" {
			i++
		}
		if args[i] != "--workdir" {
			panic("expected workdir and no TTY")
		}
		workdir, name := args[i+1], args[i+2]
		home := filepath.Join(sandboxes, name, "home")
		if _, err := os.Stat(filepath.Join(sandboxes, name)); err != nil {
			panic("no sandbox")
		}
		must(os.MkdirAll(home, 0700))
		write(filepath.Join(sandboxes, name, "status"), "running")
		must(os.Setenv("HOME", home))
		must(os.Chdir(workdir))
		command := append([]string(nil), args[i+3:]...)
		// Keep command lookup inside the fake guest, independent of host adapters.
		must(os.Setenv("PATH", filepath.Join(sandboxes, name, "usr/local/bin")+":/usr/bin:/bin"))
		if command[0] == "bash" {
			if len(command) != 7 || command[1] != "-c" || command[3] != "--" || args[1] == "-i" {
				panic("expected non-interactive launcher check")
			}
			if os.Getenv("FAKE_PROBE_FAIL") != "" {
				fmt.Fprintln(os.Stderr, "cannot connect to sandbox")
				os.Exit(1)
			}
			must(os.Setenv("FAKE_CLONE_PATH", filepath.Join(sandboxes, name, "clone")))
			command[2] = strings.ReplaceAll(command[2], "/run/sandbox/source", `"$FAKE_CLONE_PATH"`)
			command[6] = strings.Replace(command[6], "/home/agent/", home+"/", 1)
		} else if command[0] == "test" {
			if len(command) != 3 || command[1] != "-x" || args[1] == "-i" {
				panic("expected non-interactive launcher check")
			}
			command[2] = strings.Replace(command[2], "/home/agent/", home+"/", 1)
		} else {
			if args[1] != "-i" || (!strings.HasPrefix(command[0], "/home/agent/.local/bin/") && !strings.HasPrefix(command[0], filepath.Join(sandboxes, name)+"/")) {
				panic("expected interactive kit launcher")
			}
			command[0] = strings.Replace(command[0], "/home/agent/", home+"/", 1)
		}
		binary, err := exec.LookPath(command[0])
		must(err)
		must(syscall.Exec(binary, command, os.Environ()))
	case "stop":
		write(filepath.Join(sandboxes, args[1], "status"), "stopped")
	case "rm":
		if len(args) != 3 || args[1] != "--force" {
			panic("expected forced removal")
		}
		if fails, _ := strconv.Atoi(os.Getenv("FAKE_RM_FAILS")); fails > 0 {
			attempts := filepath.Join(state, "rm-attempts-"+args[2])
			n := 0
			if data, err := os.ReadFile(attempts); err == nil {
				n, _ = strconv.Atoi(string(data))
			}
			write(attempts, strconv.Itoa(n+1))
			if n < fails {
				if os.Getenv("FAKE_RM_DELETE_ON_FAIL") != "" {
					must(os.RemoveAll(filepath.Join(sandboxes, args[2])))
				}
				fmt.Fprintln(os.Stderr, "sandbox busy")
				os.Exit(18)
			}
		}
		must(os.RemoveAll(filepath.Join(sandboxes, args[2])))
	default:
		panic("unexpected sbx command")
	}
}
