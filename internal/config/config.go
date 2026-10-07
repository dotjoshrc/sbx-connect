// Package config loads kit settings for terminal and editor launches.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dotjoshrc/sbx-connect/internal/agent"
)

// Options defines extra kits and an optional replacement ACP kit for one agent.
type Options struct {
	Kits   []string `json:"kits"`
	ACPKit string   `json:"acp_kit"`
}

type file struct {
	Kits   []string           `json:"kits"`
	Agents map[string]Options `json:"agents"`
}

// UserPath returns the shared config path on both macOS and Linux.
func UserPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "sbx-connect", "config.json"), nil
}

// Load reads user and project settings, or only explicit when it is nonempty.
// Project lists replace corresponding user lists when present, including [].
// Common and agent-specific lists are then combined in that order.
func Load(project, explicit, agentName string) (Options, error) {
	paths := []string{explicit}
	if explicit == "" {
		user, err := UserPath()
		if err != nil {
			return Options{}, err
		}
		paths = []string{user, filepath.Join(project, ".sbx-connect.json")}
	}
	var common []string
	var result Options
	for _, path := range paths {
		f, err := read(path, explicit == "")
		if err != nil {
			return Options{}, err
		}
		if f.Kits != nil {
			common = f.Kits
		}
		ag := f.Agents[agentName]
		if ag.Kits != nil {
			result.Kits = ag.Kits
		}
		if ag.ACPKit != "" {
			result.ACPKit = ag.ACPKit
		}
	}
	result.Kits = append(common, result.Kits...)
	return result, nil
}

func read(path string, optional bool) (file, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return file{}, err
	}
	data, err := os.ReadFile(path)
	if optional && os.IsNotExist(err) {
		return file{}, nil
	}
	if err != nil {
		return file{}, fmt.Errorf("read kit config %s: %w", path, err)
	}
	var f file
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return file{}, fmt.Errorf("parse kit config %s: %w", path, err)
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return file{}, fmt.Errorf("kit config %s must be a JSON object", path)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return file{}, fmt.Errorf("kit config %s must contain a single JSON object", path)
	}
	for name, opts := range f.Agents {
		if _, err := agent.Lookup(name); err != nil {
			return file{}, fmt.Errorf("kit config %s: %w", path, err)
		}
		if opts.ACPKit != "" {
			if err := Validate([]string{opts.ACPKit}); err != nil {
				return file{}, fmt.Errorf("kit config %s, agent %s: %w", path, name, err)
			}
		}
		if err := Validate(opts.Kits); err != nil {
			return file{}, fmt.Errorf("kit config %s, agent %s: %w", path, name, err)
		}
		for i, kit := range opts.Kits {
			opts.Kits[i] = resolve(path, kit)
		}
		opts.ACPKit = resolve(path, opts.ACPKit)
		f.Agents[name] = opts
	}
	if err := Validate(f.Kits); err != nil {
		return file{}, fmt.Errorf("kit config %s: %w", path, err)
	}
	for i, kit := range f.Kits {
		f.Kits[i] = resolve(path, kit)
	}
	return f, nil
}

func resolve(configPath, kit string) string {
	if strings.HasPrefix(kit, "./") || strings.HasPrefix(kit, "../") {
		return filepath.Join(filepath.Dir(configPath), kit)
	}
	return kit
}

// Validate rejects empty references and characters that cannot be passed to sbx.
// Reference syntax and kit compatibility are delegated to sbx.
func Validate(kits []string) error {
	for _, kit := range kits {
		if strings.TrimSpace(kit) == "" || strings.ContainsAny(kit, "\x00\r\n") {
			return fmt.Errorf("invalid kit reference %q", kit)
		}
	}
	return nil
}

// Unique removes exact duplicate references, retaining their first occurrence.
func Unique(kits []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, kit := range kits {
		if !seen[kit] {
			seen[kit] = true
			result = append(result, kit)
		}
	}
	return result
}
