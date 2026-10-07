// Package zed merges custom agents into JSONC without reserializing user settings.
package zed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/dotjoshrc/sbx-connect/internal/agent"
	"github.com/dotjoshrc/sbx-connect/internal/lock"
)

type Entry struct {
	Type        string            `json:"type"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env,omitempty"`
	DefaultMode string            `json:"default_mode,omitempty"`
}

func Entries(executable, sbx, configPath, mode string) map[string]Entry {
	entries := make(map[string]Entry)
	for _, a := range agent.All {
		name := a.ZedName
		switch mode {
		case "ephemeral":
			name = strings.Replace(a.ZedName, " in Docker Sandbox", " in Ephemeral Docker Sandbox", 1)
		case "auto":
			name = strings.Replace(a.ZedName, " in Docker Sandbox", " in Auto Docker Sandbox", 1)
		}
		e := Entry{Type: "custom", Command: executable, Args: []string{"run", a.Name, "--mode=" + mode}, DefaultMode: a.ZedDefaultMode}
		if sbx != "" || configPath != "" {
			e.Env = make(map[string]string)
			if sbx != "" {
				e.Env["SBX_CONNECT_SBX_BIN"] = sbx
			}
			if configPath != "" {
				e.Env["SBX_CONNECT_CONFIG"] = configPath
			}
		}
		entries[name] = e
	}
	return entries
}

func Snippet(entries map[string]Entry) ([]byte, error) {
	b, err := json.MarshalIndent(map[string]any{"agent_servers": entries}, "", "  ")
	return append(b, '\n'), err
}

func member(o *hujson.Object, key string) *hujson.Value {
	for i := range o.Members {
		if o.Members[i].Name.Value.(hujson.Literal).String() == key {
			return &o.Members[i].Value
		}
	}
	return nil
}

func unique(v *hujson.Value) error {
	for item := range v.All() {
		if o, ok := item.Value.(*hujson.Object); ok {
			seen := make(map[string]bool)
			for _, m := range o.Members {
				name := m.Name.Value.(hujson.Literal).String()
				if seen[name] {
					return fmt.Errorf("duplicate settings key %q", name)
				}
				seen[name] = true
			}
		}
	}
	return nil
}

func add(o *hujson.Object, key string, value hujson.Value, indent string) {
	o.Members = append(o.Members, hujson.ObjectMember{
		Name:  hujson.Value{BeforeExtra: hujson.Extra("\n" + indent), Value: hujson.String(key)},
		Value: value,
	})
}

// Merge retains all pre-existing tokens and comments. Conflicts are transactional.
func Merge(data []byte, entries map[string]Entry) ([]byte, error) {
	root, err := hujson.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse Zed settings: %w", err)
	}
	if err := unique(&root); err != nil {
		return nil, err
	}
	obj, ok := root.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("zed settings must be a JSON object")
	}
	servers := member(obj, "agent_servers")
	if servers == nil {
		add(obj, "agent_servers", hujson.Value{BeforeExtra: hujson.Extra(" "), Value: &hujson.Object{AfterExtra: hujson.Extra("\n  ")}}, "  ")
		servers = member(obj, "agent_servers")
	}
	serverObj, ok := servers.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("agent_servers must be an object")
	}
	changed := false
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := entries[name]
		encoded, _ := json.MarshalIndent(entry, "    ", "  ")
		if existing := member(serverObj, name); existing != nil {
			clean := existing.Clone()
			clean.Standardize()
			var have, want any
			if err := json.Unmarshal(clean.Pack(), &have); err != nil {
				return nil, err
			}
			_ = json.Unmarshal(encoded, &want)
			if !reflect.DeepEqual(have, want) {
				if missingDefaultModeOnly(have, want, entry.DefaultMode) {
					existingObj, ok := existing.Value.(*hujson.Object)
					if !ok {
						return nil, fmt.Errorf("conflicting agent_servers entry %q; rename or remove it, or use --print to merge manually", name)
					}
					add(existingObj, "default_mode", hujson.Value{BeforeExtra: hujson.Extra(" "), Value: hujson.String(entry.DefaultMode)}, "      ")
					changed = true
					continue
				}
				return nil, fmt.Errorf("conflicting agent_servers entry %q; rename or remove it, or use --print to merge manually", name)
			}
			continue
		}
		value, err := hujson.Parse(encoded)
		if err != nil {
			return nil, err
		}
		value.BeforeExtra = hujson.Extra(" ")
		add(serverObj, name, value, "    ")
		changed = true
	}
	if !changed {
		return data, nil
	}
	return root.Pack(), nil
}

func missingDefaultModeOnly(have, want any, mode string) bool {
	if mode == "" {
		return false
	}
	haveMap, ok := have.(map[string]any)
	if !ok {
		return false
	}
	wantMap, ok := want.(map[string]any)
	if !ok {
		return false
	}
	if got, ok := wantMap["default_mode"].(string); !ok || got != mode {
		return false
	}
	if _, ok := haveMap["default_mode"]; ok {
		return false
	}
	withoutMode := make(map[string]any, len(wantMap)-1)
	for key, value := range wantMap {
		if key != "default_mode" {
			withoutMode[key] = value
		}
	}
	return reflect.DeepEqual(haveMap, withoutMode)
}

func DefaultPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "zed", "settings.json"), nil
}

type Result struct {
	Path, Backup string
	Changed      bool
}

func Install(ctx context.Context, path, cache string, entries map[string]Entry) (Result, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Result{}, err
	}
	// Follow symlinks so atomic replacement doesn't destroy the user's link.
	if _, err := os.Lstat(abs); err == nil {
		abs, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return Result{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	sum := sha256.Sum256([]byte(abs))
	unlock, err := lock.Acquire(ctx, filepath.Join(cache, fmt.Sprintf("zed-%x.lock", sum[:12])), lock.Timeout)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	r := Result{Path: abs}
	data, err := os.ReadFile(abs)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	mode := os.FileMode(0o600)
	if exists {
		info, err := os.Stat(abs)
		if err != nil {
			return r, err
		}
		mode = info.Mode().Perm()
	} else {
		data = []byte("{\n}\n")
	}
	updated, err := Merge(data, entries)
	if err != nil {
		return r, err
	}
	if exists && bytes.Equal(data, updated) {
		return r, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return r, err
	}
	if exists {
		backup, err := os.CreateTemp(filepath.Dir(abs), filepath.Base(abs)+".backup-*")
		if err != nil {
			return r, err
		}
		r.Backup = backup.Name()
		if err := writeFile(backup, data, mode); err != nil {
			return r, err
		}
	}
	f, err := os.CreateTemp(filepath.Dir(abs), ".sbx-connect-settings-*")
	if err != nil {
		return r, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := writeFile(f, updated, mode); err != nil {
		return r, err
	}
	// Detect edits made while preparing the backup; ask for a retry, not data loss.
	current, readErr := os.ReadFile(abs)
	if (exists && (readErr != nil || !bytes.Equal(current, data))) || (!exists && !errors.Is(readErr, os.ErrNotExist)) {
		return r, fmt.Errorf("settings changed during installation; retry")
	}
	if err := os.Rename(f.Name(), abs); err != nil {
		return r, err
	}
	r.Changed = true
	return r, nil
}

func writeFile(f *os.File, data []byte, mode os.FileMode) error {
	defer func() { _ = f.Close() }()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// ResolveExecutable accepts an absolute path only, for reliable editor launches.
func ResolveExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(exe) || strings.ContainsRune(exe, '\x00') {
		return "", fmt.Errorf("cannot resolve executable path")
	}
	return exe, nil
}
