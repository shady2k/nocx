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
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// captureSandbox stops at the native-service seam after recording the command
// and private bootstrap artifact the composition root would enforce. The real
// PTY smoke proves the child remains interactive; this half pins that sandbox
// bootstrap stays inside the private runtime tree without adding another
// foreground executable.
type captureSandbox struct {
	cacheDir     string
	spec         sandbox.CommandSpec
	artifact     []byte
	artifactPath string
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
		s.artifactPath = artifact
	}
	return nil, sandbox.NewSetupErrorf("capture stop")
}

func TestSandboxedShellLaunchKeepsPrivateAuthenticatedBootstrap(t *testing.T) {
	for _, shellName := range []string{"bash", "zsh"} {
		t.Run(shellName, func(t *testing.T) {
			shellPath, err := exec.LookPath(shellName)
			if err != nil {
				t.Fatalf("%s is not installed: %v", shellName, err)
			}
			storagetest.IsolateWithHome(t)
			workspace := t.TempDir()

			f := localFactory(t)
			svc := &captureSandbox{cacheDir: t.TempDir()}
			f.sandbox = svc
			f.shells = fixedShell{path: shellPath}

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
			runtimeSessions := filepath.Join(svc.cacheDir, "sandbox-sessions") + string(filepath.Separator)
			artifact := filepath.Clean(svc.artifactPath)
			if !strings.HasPrefix(artifact, runtimeSessions) ||
				!strings.Contains(artifact, string(filepath.Separator)+"tmp"+string(filepath.Separator)) {
				t.Fatalf("bootstrap artifact = %q, want private runtime tmp under %q", svc.artifactPath, runtimeSessions)
			}
			if !strings.Contains(string(svc.artifact), "__nocx_lc_active") {
				t.Fatalf("bootstrap artifact does not contain authenticated nocxify lifecycle")
			}
			if strings.Contains(string(svc.artifact), "opencode") {
				t.Fatalf("bootstrap artifact unexpectedly launches opencode")
			}
		})
	}
}
