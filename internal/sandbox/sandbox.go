package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"sbx-connect/internal/agent"
	"sbx-connect/internal/lock"
	"sbx-connect/internal/process"
)

const cleanupTimeout = 10 * time.Second

type Manager struct {
	Runner   process.Runner
	CacheDir string
	Debug    bool
}

type Project struct {
	Path, Name    string
	Agent         agent.Definition
	Kit, Template string
	ExtraKits     []string
	AddACPKit     bool
}

// AbsoluteProject preserves a valid logical PWD, including symbolic links.
// Neither symlinks nor Git roots are resolved: worktree paths remain distinct.
func AbsoluteProject(path string) (string, error) {
	if path == "" {
		path = "."
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	if path == "/" {
		return "", fmt.Errorf("choose a project directory, not the filesystem root")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project %q is not a directory", path)
	}
	// sbx reserves :ro and :rw suffixes for mount modes.
	if strings.HasSuffix(path, ":ro") || strings.HasSuffix(path, ":rw") {
		return "", fmt.Errorf("project path ends in an sbx mount-mode suffix: %q", path)
	}
	return path, nil
}

func Name(a, path string) string {
	sum := sha256.Sum256([]byte(path))
	return fmt.Sprintf("sbx-connect-%s-%s-%x", a, projectSlug(path), sum[:12])
}

// EphemeralName returns an owned sandbox name with a random per-session suffix.
func EphemeralName(a, path string) (string, error) {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("sbx-connect-%s-%s-%x", a, projectSlug(path), suffix), nil
}

func projectSlug(path string) string {
	base := filepath.Base(path)
	var b strings.Builder
	lastDash := false
	for i := 0; i < len(base); i++ {
		c := base[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c)
			lastDash = false
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
			lastDash = false
		case c >= '0' && c <= '9':
			b.WriteByte(c)
			lastDash = false
		default:
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= 40 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "project"
	}
	return slug
}

var ownedName = regexp.MustCompile(`^sbx-connect-(codex|claude)-([a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?-)?[0-9a-f]{24}$`)

func Owned(name string) bool { return ownedName.MatchString(name) }

func (m Manager) Names(ctx context.Context) ([]string, error) {
	m.debugf("lifecycle list-managed start")
	out, err := m.Runner.Capture(ctx, "ls", "--quiet")
	if err != nil {
		m.debugf("lifecycle list-managed failed error=%v", err)
		return nil, fmt.Errorf("cannot list sandboxes; check sbx login and the sandbox service: %w", err)
	}
	var names []string
	for _, name := range strings.Fields(out) {
		if Owned(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	m.debugf("lifecycle list-managed done count=%d names=%q", len(names), strings.Join(names, ","))
	return names, nil
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func (m Manager) lock(ctx context.Context, name string) (func(), error) {
	path := filepath.Join(m.CacheDir, name+".lock")
	m.debugf("lifecycle lock acquire name=%q path=%q", name, path)
	unlock, err := lock.Acquire(ctx, path, lock.Timeout)
	if err != nil {
		m.debugf("lifecycle lock failed name=%q error=%v", name, err)
		return nil, err
	}
	m.debugf("lifecycle lock acquired name=%q", name)
	return func() {
		m.debugf("lifecycle lock release name=%q", name)
		unlock()
		m.debugf("lifecycle lock released name=%q", name)
	}, nil
}

// Prepare reuses a compatible running sandbox or prepares the stable sandbox.
// The returned project carries the selected sandbox name for printing or ACP.
func (m Manager) Prepare(ctx context.Context, p Project) (Project, error) {
	m.debugf("lifecycle prepare start name=%q agent=%q project=%q", p.Name, p.Agent.Name, p.Path)
	selected, found, err := m.discover(ctx, p)
	if err != nil || found {
		m.debugf("lifecycle prepare discovery done found=%t selected=%q error=%v", found, selected.Name, err)
		return selected, err
	}
	err = m.prepareOwned(ctx, &p)
	m.debugf("lifecycle prepare owned done name=%q error=%v", p.Name, err)
	return p, err
}

func (m Manager) prepareOwned(ctx context.Context, p *Project) error {
	m.debugf("lifecycle prepare-owned start name=%q agent=%q project=%q", p.Name, p.Agent.Name, p.Path)
	unlock, err := m.lock(ctx, p.Name)
	if err != nil {
		return err
	}
	defer unlock()
	names, err := m.Names(ctx)
	if err != nil {
		return err
	}
	if !contains(names, p.Name) {
		fmt.Fprintf(m.Runner.Stderr, "Creating %s for %s\n", p.Name, p.Path)
		m.debugf("lifecycle create start name=%q kit=%q extra_kits=%d template=%q", p.Name, p.Kit, len(p.ExtraKits), p.Template)
		args := []string{"create", "--name", p.Name, "--kit", p.Kit}
		for _, kit := range p.ExtraKits {
			args = append(args, "--kit", kit)
		}
		if p.Template != "" {
			args = append(args, "--template", p.Template)
		}
		args = append(args, p.Agent.Name, p.Path)
		if err := m.Runner.Setup(ctx, args...); err != nil {
			m.debugf("lifecycle create failed name=%q error=%v", p.Name, err)
			return fmt.Errorf("sandbox creation failed: %w", err)
		}
		m.debugf("lifecycle create done name=%q", p.Name)
	} else {
		fmt.Fprintln(m.Runner.Stderr, "Reusing sandbox; kit and template changes only affect new sandboxes")
		m.debugf("lifecycle prepare-owned reuse name=%q", p.Name)
	}
	// sbx exec resumes stopped sandboxes; setup has no editor stdin. Check the
	// kit's launcher without starting ACP or downloading an npm package.
	m.debugf("lifecycle launcher-check start name=%q binary=%q", p.Name, p.Agent.Binary)
	status, err := m.checkLauncher(ctx, p, false)
	if err == nil && status != "ready" {
		err = fmt.Errorf("no executable %s-acp, acp, or %s", p.Agent.Name, p.Agent.Binary)
	}
	if err != nil {
		m.debugf("lifecycle launcher-check failed name=%q error=%v", p.Name, err)
		return fmt.Errorf("cannot verify ACP kit launcher %s in %s; check sbx diagnostics; if this sandbox predates kit support, preserve its sandbox-local data, run sbx-connect remove %s --yes, then prepare it again: %w", p.Agent.Binary, p.Name, p.Name, err)
	}
	m.debugf("lifecycle launcher-check done name=%q", p.Name)
	return nil
}

// Mode selects how Run provisions the sandbox it connects to.
type Mode int

const (
	// ModeReuse is the default: it reuses a discovered sandbox or the stable
	// per-project sandbox, same as Prepare. It is never removed after the
	// session exits.
	ModeReuse Mode = iota
	// ModeEphemeral bypasses discovery and always creates a new disposable
	// sandbox, removed after the session exits.
	ModeEphemeral
	// ModeAuto reuses a discovered sandbox if one exists (left running
	// afterward, same as ModeReuse); otherwise it falls back to creating a
	// disposable sandbox, removed after the session exits.
	ModeAuto
)

func (mode Mode) String() string {
	switch mode {
	case ModeEphemeral:
		return "ephemeral"
	case ModeAuto:
		return "auto"
	default:
		return "reuse"
	}
}

func (m Manager) Run(ctx context.Context, p Project, args []string, mode Mode) error {
	m.debugf("lifecycle run start name=%q agent=%q project=%q mode=%s adapter_args=%d", p.Name, p.Agent.Name, p.Path, mode, len(args))
	var err error
	disposable := false
	switch mode {
	case ModeEphemeral:
		if p.Name, err = EphemeralName(p.Agent.Name, p.Path); err == nil {
			err = m.prepareOwned(ctx, &p)
		}
		disposable = true
	case ModeAuto:
		var found bool
		if p, found, err = m.discover(ctx, p); err == nil && !found {
			if p.Name, err = EphemeralName(p.Agent.Name, p.Path); err == nil {
				err = m.prepareOwned(ctx, &p)
			}
			disposable = true
		}
	default: // ModeReuse
		p, err = m.Prepare(ctx, p)
	}
	if err != nil {
		return err
	}
	// Prepare/discover returned and released their lock before starting the ACP stream.
	fmt.Fprintf(m.Runner.Stderr, "Connecting to %s\n", p.Name)
	cmd := []string{"exec", "-i", "--workdir", p.Path, p.Name, p.Agent.Binary}
	m.debugf("lifecycle acp start name=%q workdir=%q binary=%q", p.Name, p.Path, p.Agent.Binary)
	err = m.Runner.Stream(ctx, append(cmd, args...)...)
	m.debugf("lifecycle acp exit name=%q error=%v", p.Name, err)
	if disposable {
		fmt.Fprintf(m.Runner.Stderr, "Removing %s after ACP session exit\n", p.Name)
		// Editor shutdown cancels the session context. Use a bounded, uncanceled
		// context so disposable sandboxes are still removed when the editor quits.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		m.debugf("lifecycle cleanup context name=%q timeout=%s", p.Name, cleanupTimeout)
		err = errors.Join(err, m.removeEphemeral(cleanupCtx, p.Name))
	}
	m.debugf("lifecycle run done name=%q error=%v", p.Name, err)
	return err
}

func (m Manager) removeEphemeral(ctx context.Context, name string) error {
	m.debugf("lifecycle cleanup start name=%q", name)
	if !Owned(name) {
		m.debugf("lifecycle cleanup rejected name=%q", name)
		return fmt.Errorf("%q is not a sbx-connect sandbox name", name)
	}
	unlock, err := m.lock(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()

	var last error
	for {
		names, err := m.Names(ctx)
		if err != nil {
			m.debugf("lifecycle cleanup list failed name=%q error=%v", name, err)
			last = err
		} else if !contains(names, name) {
			m.debugf("lifecycle cleanup already-gone name=%q", name)
			return nil
		} else {
			m.debugf("lifecycle cleanup remove start name=%q", name)
			if err := m.Runner.Setup(ctx, "rm", "--force", name); err == nil {
				m.debugf("lifecycle cleanup remove done name=%q", name)
				return nil
			} else {
				m.debugf("lifecycle cleanup remove failed name=%q error=%v", name, err)
				last = err
			}
		}

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			m.debugf("lifecycle cleanup timeout name=%q error=%v", name, last)
			return fmt.Errorf("remove disposable sandbox %s: %w", name, last)
		case <-timer.C:
			m.debugf("lifecycle cleanup retry name=%q", name)
		}
	}
}

func (m Manager) Lifecycle(ctx context.Context, action, name string) error {
	m.debugf("lifecycle command start action=%q name=%q", action, name)
	if !Owned(name) {
		m.debugf("lifecycle command rejected action=%q name=%q", action, name)
		return fmt.Errorf("%q is not a sbx-connect sandbox name", name)
	}
	unlock, err := m.lock(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	names, err := m.Names(ctx)
	if err != nil {
		return err
	}
	if !contains(names, name) {
		m.debugf("lifecycle command not-managed action=%q name=%q", action, name)
		return fmt.Errorf("sandbox %q does not exist", name)
	}
	if action == "rm" {
		err := m.Runner.Setup(ctx, action, "--force", name)
		m.debugf("lifecycle command done action=%q name=%q error=%v", action, name, err)
		return err
	}
	err = m.Runner.Setup(ctx, action, name)
	m.debugf("lifecycle command done action=%q name=%q error=%v", action, name, err)
	return err
}

func (m Manager) debugf(format string, args ...any) {
	if !m.Debug || m.Runner.Stderr == nil {
		return
	}
	fmt.Fprintf(m.Runner.Stderr, "sbx-connect debug: "+format+"\n", args...)
}
