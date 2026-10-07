package sandbox

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLogicalProjectIdentity(t *testing.T) {
	// Resolve any symlink in TempDir's own path (e.g. macOS's /var -> /private/var)
	// so physical, built by string-joining below, matches raw getwd()'s resolved form.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(dir, "physical")
	logical := filepath.Join(dir, "logical")
	if err := os.Mkdir(physical, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, logical); err != nil {
		t.Fatal(err)
	}
	t.Chdir(logical)
	t.Setenv("PWD", logical)
	path, err := AbsoluteProject(".")
	if err != nil || path != logical {
		t.Fatalf("%q %v", path, err)
	}
	if Name("codex", path) == Name("codex", physical) {
		t.Fatal("symlink paths collapsed")
	}
	t.Setenv("PWD", dir) // Invalid PWD must not redirect the current project.
	path, err = AbsoluteProject(".")
	if err != nil || path != physical {
		t.Fatalf("%q %v", path, err)
	}
	sum := sha256.Sum256([]byte(physical))
	want := fmt.Sprintf("sbx-connect-codex-physical-%x", sum[:12])
	if Name("codex", physical) != want || !Owned(want) {
		t.Fatal("incorrect hash")
	}
	if !Owned(fmt.Sprintf("sbx-connect-codex-%x", sum[:12])) {
		t.Fatal("legacy sandbox name not owned")
	}
	if Name("claude", physical) == want || Name("codex", physical+"-worktree") == want {
		t.Fatal("identity collision")
	}
}

func TestProjectSlug(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/tmp/My Project", "my-project"},
		{"/tmp/project:rw", "project-rw"},
		{"/tmp/---", "project"},
		{"/tmp/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		if got := projectSlug(tc.path); got != tc.want {
			t.Fatalf("projectSlug(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestInvalidProjects(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", file, filepath.Join(dir, "missing")} {
		if _, err := AbsoluteProject(p); err == nil {
			t.Fatal("accepted", p)
		}
	}
}
