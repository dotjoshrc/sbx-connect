package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeConfig(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLayeringAndRelativePaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "user"))
	user, err := UserPath()
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	writeConfig(t, user, `{"kits":["global"],"agents":{"codex":{"kits":["./user-kit"],"acp_kit":"user-acp"},"claude":{"kits":["claude-only"]}}}`)
	writeConfig(t, filepath.Join(project, ".sbx-connect.json"), `{"kits":["./project-kit"],"agents":{"codex":{"acp_kit":"./custom-acp"}}}`)
	got, err := Load(project, "", "codex")
	want := Options{Kits: []string{filepath.Join(project, "project-kit"), filepath.Join(filepath.Dir(user), "user-kit")}, ACPKit: filepath.Join(project, "custom-acp")}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
	got, err = Load(project, "", "claude")
	if err != nil || !reflect.DeepEqual(got.Kits, []string{filepath.Join(project, "project-kit"), "claude-only"}) {
		t.Fatalf("%+v %v", got, err)
	}
	writeConfig(t, filepath.Join(project, ".sbx-connect.json"), `{"kits":[],"agents":{"codex":{"kits":[]}}}`)
	got, err = Load(project, "", "codex")
	if err != nil || len(got.Kits) != 0 || got.ACPKit != "user-acp" {
		t.Fatalf("empty lists did not override: %+v %v", got, err)
	}
}

func TestExplicitConfigSkipsDiscovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	user, _ := UserPath()
	writeConfig(t, user, "invalid")
	writeConfig(t, filepath.Join(root, ".sbx-connect.json"), "invalid")
	explicit := filepath.Join(root, "custom.json")
	writeConfig(t, explicit, `{"kits":["first"],"agents":{"codex":{"kits":["second"]}}}`)
	got, err := Load(root, explicit, "codex")
	if err != nil || !reflect.DeepEqual(got.Kits, []string{"first", "second"}) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := Load(root, filepath.Join(root, "missing.json"), "codex"); err == nil {
		t.Fatal("accepted missing explicit config")
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, data := range []string{
		`invalid`, `null`, `[]`, `{} {}`, `{"kitz":[]}`, `{"kits":"kit"}`,
		`{"kits":[""]}`, `{"kits":[" \t"]}`, `{"kits":["bad\u0000kit"]}`,
		`{"agents":{"other":{}}}`, `{"agents":{"codex":{"extra":true}}}`,
		`{"agents":{"codex":{"acp_kit":" "}}}`, `{"agents":{"claude":{"kits":["\n"]}}}`,
	} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeConfig(t, path, data)
			if _, err := Load("", path, "codex"); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}

func TestMissingDefaultsAndHomeFallback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", root)
	path, err := UserPath()
	if err != nil || path != filepath.Join(root, ".config/sbx-connect/config.json") {
		t.Fatalf("%s %v", path, err)
	}
	got, err := Load(root, "", "codex")
	if err != nil || len(got.Kits) != 0 || got.ACPKit != "" {
		t.Fatalf("%+v %v", got, err)
	}
}
