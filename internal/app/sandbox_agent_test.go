package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// captureSandbox stops at the native-service seam after recording the command
// the composition root would enforce. Shell execution and lifecycle acceptance
// are exercised in shellintegration's real-PTY regression; this half pins the
// app-owned private launch inputs without depending on a host kernel backend.
type captureSandbox struct {
	cacheDir string
	spec     sandbox.CommandSpec
	artifact []byte
}

func (s *captureSandbox) Status(context.Context) sandbox.Status {
	return sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}
}

func (s *captureSandbox) NewRuntimeRoot() (string, error) {
	return sandbox.NewRuntimeRoot(s.cacheDir)
}

func (s *captureSandbox) Prepare(_ context.Context, _ sandbox.Request, spec sandbox.CommandSpec) (*sandbox.PreparedCommand, error) {
	s.spec = spec
	artifact := ""
	if len(spec.Args) >= 2 && spec.Args[0] == "--rcfile" {
		artifact = spec.Args[1]
	} else {
		for _, kv := range spec.Env {
			if dir, ok := strings.CutPrefix(kv, "ZDOTDIR="); ok {
				artifact = filepath.Join(dir, ".zshrc")
				break
			}
		}
	}
	if artifact != "" {
		s.artifact, _ = os.ReadFile(artifact)
	}
	return nil, sandbox.NewSetupErrorf("capture stop")
}

func TestSandboxedAgentLaunchIsBackendOwnedAndBoundToTheBootstrap(t *testing.T) {
	for _, shellName := range []string{"bash", "zsh"} {
		t.Run(shellName, func(t *testing.T) {
			shellPath, err := exec.LookPath(shellName)
			if err != nil {
				t.Fatalf("%s is not installed: %v", shellName, err)
			}
			storagetest.IsolateWithHome(t)
			workspace := t.TempDir()
			agent := filepath.Join(t.TempDir(), "opencode")
			if writeErr := os.WriteFile(agent, []byte("fixture"), 0o700); writeErr != nil { //nolint:gosec // executable fixture path
				t.Fatalf("write opencode fixture: %v", writeErr)
			}

			f := localFactory(t)
			svc := &captureSandbox{cacheDir: t.TempDir()}
			f.sandbox = svc
			f.shells = fixedShell{path: shellPath}
			f.agentExec = func() (string, error) { return agent, nil }

			_, err = f.NewPTY(context.Background(), pty.Config{
				SessionID: "0123456789abcdef0123456789abcdef",
				Enhanced:  true,
				Cols:      80,
				Rows:      24,
				Cwd:       workspace,
				Sandbox:   &sandbox.Request{Workspace: workspace},
			})
			var setupErr *sandbox.SetupError
			if !errors.As(err, &setupErr) {
				t.Fatalf("NewPTY error = %v, want capture SetupError", err)
			}
			if svc.spec.Path != shellPath || svc.spec.Dir != workspace {
				t.Fatalf("sandbox command = path %q dir %q, want shell %q in %q", svc.spec.Path, svc.spec.Dir, shellPath, workspace)
			}
			if len(svc.spec.TrustedExecutables) != 1 || svc.spec.TrustedExecutables[0] != agent {
				t.Fatalf("trusted executables = %v, want fixed opencode %q", svc.spec.TrustedExecutables, agent)
			}
			if !strings.Contains(string(svc.artifact), "exec "+shellintegration.ShellQuote(agent)) {
				t.Fatalf("bootstrap does not exec backend-resolved opencode; tail=%q", artifactTail(svc.artifact, 300))
			}
		})
	}
}

func artifactTail(data []byte, n int) string {
	if len(data) <= n {
		return string(data)
	}
	return string(data[len(data)-n:])
}
