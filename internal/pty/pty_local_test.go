package pty

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/sandbox"
)

func TestLocalPty_SandboxInfoReturnsDeepCopy(t *testing.T) {
	policy := &sandbox.Policy{
		Workspace:       "/workspace",
		WritableRoots:   []string{"/workspace", "/runtime/home"},
		ReadOnlyRoots:   []string{"/usr", "/runtime/ro"},
		HomeProjections: []sandbox.HomeProjection{{HostPath: "/workspace", RelativePath: "workspace"}},
	}
	lp := &LocalPty{
		prepared: &sandbox.PreparedCommand{
			Backend: sandbox.BackendLandlock,
			Policy:  policy,
		},
	}

	first := lp.SandboxInfo()
	first.WritableRoots[0] = "/mutated"
	first.ReadOnlyRoots[0] = "/mutated-ro"
	first.HomeProjections[0].HostPath = "/mutated-home"
	second := lp.SandboxInfo()

	if got := second.WritableRoots[0]; got != "/workspace" {
		t.Fatalf("second SandboxInfo root = %q, want immutable policy root", got)
	}
	if got := second.ReadOnlyRoots[0]; got != "/usr" {
		t.Fatalf("second SandboxInfo read-only root = %q, want immutable policy root", got)
	}
	if got := second.HomeProjections[0].HostPath; got != "/workspace" {
		t.Fatalf("second SandboxInfo projection = %q, want immutable policy projection", got)
	}
	if got := policy.WritableRoots[0]; got != "/workspace" {
		t.Fatalf("policy root = %q after accessor mutation", got)
	}
	if got := policy.ReadOnlyRoots[0]; got != "/usr" {
		t.Fatalf("policy read-only root = %q after accessor mutation", got)
	}
	if got := policy.HomeProjections[0].HostPath; got != "/workspace" {
		t.Fatalf("policy projection = %q after accessor mutation", got)
	}
}

func TestLocalPty_ImplementsInterface(t *testing.T) {
	var _ Pty = (*LocalPty)(nil)
}

type startFailingSandboxService struct {
	dir string
}

func (s startFailingSandboxService) Status(context.Context) sandbox.Status {
	return sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}
}

func (s startFailingSandboxService) NewRuntimeRoot() (string, error) {
	return sandbox.NewRuntimeRoot(s.dir)
}

func (s startFailingSandboxService) Prepare(_ context.Context, req sandbox.Request, spec sandbox.CommandSpec) (*sandbox.PreparedCommand, error) {
	cmd := exec.Command(spec.Path, spec.Args...) //nolint:gosec // injected test seam must preserve the production command
	cmd.Dir = s.dir
	cmd.Env = spec.Env
	cmd.ExtraFiles = spec.ExtraFiles
	return &sandbox.PreparedCommand{
		Cmd:     cmd,
		Backend: sandbox.BackendLandlock,
		Policy: &sandbox.Policy{
			Workspace:       req.Workspace,
			WritableRoots:   []string{req.Workspace},
			HomeProjections: []sandbox.HomeProjection{},
		},
	}, nil
}

func TestNewLocal_SandboxStartFailureIsTyped(t *testing.T) {
	workspace := t.TempDir()
	_, err := NewLocal(log.NewSlogAdapter(nil), Config{
		Cols:    80,
		Rows:    24,
		Sandbox: &sandbox.Request{Workspace: workspace},
	}, WithSandboxService(startFailingSandboxService{dir: filepath.Join(t.TempDir(), "missing")}))
	var setupErr *sandbox.SetupError
	if !errors.As(err, &setupErr) {
		t.Fatalf("err = %v, want sandbox SetupError", err)
	}
}

func TestLocalPty_SpawnAndWrite(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	n, err := lp.Write([]byte("echo hello\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n == 0 {
		t.Fatal("Write returned 0 bytes")
	}
}

func TestLocalPty_ReadReturnsOutput(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	_, err := lp.Write([]byte("echo hello\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	buf := make([]byte, 4096)
	var output strings.Builder
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, readErr := lp.Read(buf)
		if readErr != nil && readErr != io.EOF {
			t.Fatalf("Read: %v", readErr)
		}
		if n > 0 {
			output.Write(buf[:n])
			if strings.Contains(output.String(), "hello") {
				return
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	t.Fatalf("expected output to contain 'hello', got: %q", output.String())
}

func TestLocalPty_Resize(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	defer func() { _ = lp.Close() }()

	err := lp.Resize(context.Background(), 132, 43, 0, 0)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}
}

func TestLocalPty_CloseTwice(t *testing.T) {
	lp := mustSpawn(t, 80, 24)
	if err := lp.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := lp.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func mustSpawn(t testing.TB, cols, rows uint16) *LocalPty {
	t.Helper()
	lp, err := NewLocal(log.NewSlogAdapter(nil), Config{
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return lp
}

// A macOS .app launched from Finder inherits no locale at all. The child shell
// then computes a non-UTF-8 stdout encoding and every Python/Rich/prompt_toolkit
// TUI silently downgrades non-ASCII output to '?' — which looks exactly like a
// font bug in the renderer. Fill the gap, but never override a deliberate choice.
func TestWithUTF8Locale(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want string // expected LANG entry, "" = must not be added
	}{
		{
			name: "adds LANG when the environment carries no locale at all (Finder launch)",
			env:  []string{"PATH=/usr/bin", "TERM=xterm-256color"},
			want: "LANG=en_US.UTF-8",
		},
		{
			name: "keeps an inherited LANG untouched",
			env:  []string{"LANG=ru_RU.UTF-8"},
			want: "",
		},
		{
			name: "respects a deliberate non-UTF-8 LANG",
			env:  []string{"LANG=C"},
			want: "",
		},
		{
			name: "LC_ALL alone is enough — do not add LANG",
			env:  []string{"LC_ALL=en_GB.UTF-8"},
			want: "",
		},
		{
			name: "LC_CTYPE alone is enough — do not add LANG",
			env:  []string{"LC_CTYPE=en_US.UTF-8"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withUTF8Locale(tt.env)

			var added []string
			for _, kv := range got {
				if !contains(tt.env, kv) {
					added = append(added, kv)
				}
			}

			if tt.want == "" {
				if len(added) != 0 {
					t.Fatalf("expected no additions, got %v", added)
				}
				return
			}
			if len(added) != 1 || added[0] != tt.want {
				t.Fatalf("expected exactly %q to be added, got %v", tt.want, added)
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestScrubLauncherSession(t *testing.T) {
	// nocx is developed from inside a coding agent, so its own process carries
	// that agent's session markers. Handing them to a user's shell made
	// `claude` in a tab think it was a child session and turn transcript
	// saving off — a terminal must not leak its launcher's identity.
	env := []string{
		"PATH=/usr/bin",
		"CLAUDECODE=1",
		"CLAUDE_CODE_CHILD_SESSION=1",
		"CLAUDE_CODE_SESSION_ID=abc",
		"CLAUDE_PID=123",
		// Coding agents export these for their own tools; leaking them into a
		// PTY makes TUIs render black-and-white.
		"TERM=dumb",
		"NO_COLOR=1",
		"HOME=/Users/someone",
		// Not a session marker: stripping a credential would break the very
		// tool this fix exists for.
		"CLAUDE_API_KEY=secret",
	}

	got := scrubLauncherSession(env)

	for _, unwanted := range []string{"CLAUDECODE=", "CLAUDE_CODE_CHILD_SESSION=", "CLAUDE_CODE_SESSION_ID=", "CLAUDE_PID=", "TERM=", "NO_COLOR="} {
		for _, kv := range got {
			if strings.HasPrefix(kv, unwanted) {
				t.Errorf("launcher session marker survived: %q", kv)
			}
		}
	}

	for _, wanted := range []string{"PATH=/usr/bin", "HOME=/Users/someone", "CLAUDE_API_KEY=secret"} {
		found := false
		for _, kv := range got {
			if kv == wanted {
				found = true
			}
		}
		if !found {
			t.Errorf("scrub removed something it should have kept: %q", wanted)
		}
	}
}

// The decision about WHICH shell a local session runs moved to
// internal/loginshell (nocx-wwz0): it had three copies — this one, one in
// internal/git/local, and a hardcoded "bash" in the composition root that
// outranked both — and on macOS the third one won, so every user of the
// platform this product ships to was greeted by a shell they had not chosen.
// The table that used to be here lives beside its owner now; what stays here
// is the assertion that this package still REPORTS the answer it acts on.

// And the paired assertion AGENTS.md asks for: on an ordinary machine the
// decision is not merely correct, it reaches the log. A resolver nobody can
// read the output of is the arrangement this bead exists to remove.
func TestNewLocal_LogsTheShellItResolved(t *testing.T) {
	var buf bytes.Buffer
	lp, err := NewLocal(
		log.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, nil))),
		Config{Cols: 80, Rows: 24},
	)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	defer func() { _ = lp.Close() }()

	line := buf.String()
	if !strings.Contains(line, "local pty shell resolved") {
		t.Fatalf("no resolved-shell line in the log:\n%s", line)
	}
	// The path and where it came from, both: "bash" without "SHELL said so"
	// leaves the next reader doing exactly the inference this removes.
	if !strings.Contains(line, "shell=") || !strings.Contains(line, "source=") {
		t.Errorf("the line names neither the shell nor its source:\n%s", line)
	}
}
