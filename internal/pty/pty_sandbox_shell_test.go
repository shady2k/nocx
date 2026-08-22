package pty

import (
	"os/exec"
	"testing"

	"github.com/shady2k/nocx/internal/sandbox"
)

// Shell() answers "which binary is this session's shell", and a sandboxed
// session's cmd.Path is the enforcement wrapper rather than that. Reporting
// the wrapper sends a shell-integration status about a program the user
// cannot configure; app.go worked around it by passing the resolved shell to
// f.report on EVERY path, which quietly changed what an ordinary session
// reports too (nocx-6lh62).
func TestLocalPtyShell_NamesTheShellNotTheSandboxWrapper(t *testing.T) {
	lp := &LocalPty{
		cmd: &exec.Cmd{Path: "/usr/bin/sandbox-exec"},
		prepared: &sandbox.PreparedCommand{
			Policy: &sandbox.Policy{Shell: "/bin/zsh"},
		},
	}
	if got := lp.Shell(); got != "/bin/zsh" {
		t.Errorf("Shell() = %q, want the policy's shell %q", got, "/bin/zsh")
	}
}

// An ordinary session is unchanged: the launched process IS the answer.
func TestLocalPtyShell_OrdinarySessionNamesTheLaunchedProcess(t *testing.T) {
	lp := &LocalPty{cmd: &exec.Cmd{Path: "/bin/bash"}}
	if got := lp.Shell(); got != "/bin/bash" {
		t.Errorf("Shell() = %q, want %q", got, "/bin/bash")
	}
}
