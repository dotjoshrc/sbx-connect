package sandbox

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Commands are passed as arguments, never interpolated into shell code.
// Resolving without starting the adapter keeps setup independent of ACP stdin.
const launcherCheck = `for launcher in "$@"; do
  binary=$(command -v -- "$launcher") || continue
  if test -f "$binary" && test -x "$binary"; then
    case "$binary" in /*) ;; *) binary="$PWD/$binary" ;; esac
    printf 'ready\n%s\n' "$binary"
    exit 0
  fi
done
printf 'needs-kit\n'`

// Clone-mode workspaces are private copies and must not be reused.
const reuseCheck = `if test -d /run/sandbox/source; then printf 'incompatible\n'; exit 0; fi
` + launcherCheck

func (m Manager) checkLauncher(ctx context.Context, p *Project, reuse bool) (string, error) {
	check := launcherCheck
	if reuse {
		check = reuseCheck
	}
	out, err := m.Runner.Capture(ctx, "exec", "--workdir", p.Path, p.Name, "bash", "-c", check, "--", p.Agent.Name+"-acp", "acp", p.Agent.Binary)
	if err != nil {
		return "", err
	}
	if out == "needs-kit\n" || (reuse && out == "incompatible\n") {
		return strings.TrimSuffix(out, "\n"), nil
	}
	if binary, ok := strings.CutPrefix(out, "ready\n"); ok {
		binary = strings.TrimSuffix(binary, "\n")
		if filepath.IsAbs(binary) && !strings.ContainsAny(binary, "\x00\r\n") {
			p.Agent.Binary = binary
			return "ready", nil
		}
	}
	return "", fmt.Errorf("unexpected launcher check response")
}
