//go:build nocx_local_ssh

package app

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh/knownhosts"
)

// The live-sshd fixture's per-build state, in the local-ssh build.
//
// IT IS EMPTY NOW, and that is nocx-oalo1.3.2's doing: every journey that
// filled it opened its session with `ssh.RealClient.Connect`, the coordinator's
// own dial stack, and that stack is gone — an ssh pane is this machine's
// helper's. What remains here is the declaration the shared fixture embeds and
// the one server-side count a surviving live-typed-ssh test reads.
//
// Why it is still per build: the fixture (`liveSshd`) compiles in both, and a
// struct's fields cannot carry a build tag (the same reason internal/ssh's
// dialState has one declaration per build). The other half is
// live_sshd_dial_state_absent_test.go.

type liveSshdDial struct{}

func newLiveSshdDial() liveSshdDial { return liveSshdDial{} }

// knownHostsPath writes the fixture's host key where a client will look for it.
// It is the coordinator's own trust answer for this server, and the live
// helper-journey stands still need it when they resolve a destination.
func (fx *liveSshd) knownHostsPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{fx.addr}, fx.hostKey)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}
