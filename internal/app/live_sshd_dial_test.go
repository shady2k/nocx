//go:build nocx_local_ssh

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/storage/storagetest"
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

// knownHostsPath gives this test its own $HOME and writes the fixture's host
// key where the coordinator's client looks for it. It is the coordinator's own
// trust answer for this server, and the live helper-journey stands still need
// it when they resolve a destination. The path is the product's default,
// because the client takes no other.
func (fx *liveSshd) knownHostsPath(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	storagetest.SetHome(t, home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("prepare %s: %v", dir, err)
	}
	path := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{fx.addr}, fx.hostKey)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// withSshdConfig appends lines to sshd_config. A Match block must be last,
// and only one caller may use it, so it is a single string rather than a list.
func withSshdConfig(lines string) liveSshdOption {
	return func(c *liveSshdConfig) { c.extraConfig = lines }
}

// withFarSideRecording turns on the fixture-owned surfaces the canary is
// asserted against on the far host: a private TMPDIR, a real HISTFILE, and a
// recorder that writes the argv and the environment of the very process that
// runs our exec request.
//
// The recorder has to run INSIDE that process, because argv exists nowhere
// else and not for long: sshd runs an exec request as `<login shell> -c
// <request>`, and the loader immediately execs, which replaces the argv.
//
// The seam is `~/.bashrc`, and which seam it is was MEASURED rather than
// assumed. $BASH_ENV is the obvious answer and it is the wrong one here: bash
// reads $BASH_ENV for a non-interactive shell only when it does not think it
// was started by sshd, and when SSH_CLIENT is in the environment it sources
// `~/.bashrc` INSTEAD. So the recorder is sourced from the fixture's own
// `~/.bashrc`, and BASH_ENV is left pointing at it as well, so either rule
// fires and the records simply append.
//
// It writes to files and never to a descriptor of the session: a byte on
// stdout here would land in the middle of the loader's frame protocol.
func withFarSideRecording() liveSshdOption {
	return func(c *liveSshdConfig) { c.record = true }
}

// authCount is how many times the server accepted an authentication — the
// observable that says whether a refusal cost the user a second credential
// use. LogLevel VERBOSE is what makes it observable.
func (fx *liveSshd) authCount() int {
	return strings.Count(fx.logBuf.String(), "Accepted publickey")
}
