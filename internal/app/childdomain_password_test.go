package app

// The authentication phase belongs to the ssh client, not to us — nocx-beib.
//
// The owner could not log in to a password-authenticated host from a local
// tab: the correct password produced "Permission denied, please try again."
// Composing the child as
//
//	{ printf wrapper; printf capability; printf payload; printf terminator;
//	  stty raw -echo; cat; } | ssh -tt -R … dst
//
// puts the bootstrap into the client's stdin BEFORE it has authenticated,
// and takes the terminal away from that stdin at the same time. With a key
// neither matters — the far side reaches a prompt immediately and swallows
// the staged bytes as intended — so every proof of the ssh child
// (ssh_child_assembly_test.go, ADR-0025) authenticates with an agent key and
// the interactive case was never exercised. Warp, measured from its own
// remote-server binary, runs `command ssh` with the terminal still on stdin
// and delivers through the shell's startup arguments; ADR-0022 decided the
// same thing for us ("the ssh command line is the carrier") and this is the
// one place that drifted off it.
//
// These tests state the contract in the only terms that matter to a user
// typing a password: the client gets a terminal, and the first thing it
// reads is what the human typed.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/shady2k/nocx/internal/lifecyclepub"
)

// fakeSSHOnPath installs an `ssh` that reports what the real client would
// experience — whether stdin is a terminal, and what the first line it reads
// actually is — then exits. It records its argv so the forward and the
// destination can be asserted without a network.
func fakeSSHOnPath(t *testing.T) (dir string, argsFile string) {
	t.Helper()
	dir = t.TempDir()
	argsFile = filepath.Join(dir, "argv")
	script := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "` + argsFile + `"
if [ -t 0 ]; then printf 'STDIN_IS_TTY\n'; else printf 'STDIN_IS_PIPE\n'; fi
printf "dst's password: "
IFS= read -r __pw
printf 'FIRST_READ[%s]\n' "$__pw"
`
	// #nosec G306 — the stand-in for ssh must be executable to be found
	// through PATH; temp dir, no secret.
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	return dir, argsFile
}

// testChildStartCommand stands in for the launcher's start command: multi-line
// and full of quotes, so a composer that mangles it fails here rather than on
// a real host.
func testChildStartCommand() string {
	return "env -u BASH_ENV bash -c '__nocx_ib() { :; }\nprintf %s \"it'\"'\"'s\"\n'"
}

// runComposedLine executes the composed child line on a real pty with the
// fake ssh first on PATH, types the password once the client asks for it,
// and returns everything the terminal saw.
func runComposedLine(t *testing.T, line, pathDir, password string) string {
	t.Helper()
	c := exec.Command("/bin/sh", "-c", line) // #nosec G204 — the line under test.
	c.Env = append(os.Environ(), "PATH="+pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ptmx, err := pty.Start(c)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	done := make(chan string, 1)
	promptSeen := make(chan struct{})
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		prompt := false
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
				if !prompt && strings.Contains(sb.String(), "password: ") {
					close(promptSeen)
					prompt = true
				}
			}
			if rerr != nil {
				break
			}
		}
		done <- sb.String()
	}()

	// The client asks; the human answers. Wait for the visible prompt rather
	// than guessing how long the fake client takes to start.
	select {
	case <-promptSeen:
	case <-time.After(15 * time.Second):
		_ = c.Process.Kill()
		t.Fatal("timed out waiting for the composed line's password prompt")
		return ""
	}
	if _, werr := ptmx.Write([]byte(password + "\n")); werr != nil {
		t.Logf("write password: %v", werr)
	}

	select {
	case out := <-done:
		_ = c.Wait()
		return out
	case <-time.After(15 * time.Second):
		_ = c.Process.Kill()
		t.Fatal("timed out running the composed line")
		return ""
	}
}

// The client must keep the terminal on its stdin: that is what lets OpenSSH
// prompt for a password, a passphrase, a host-key confirmation or a
// second factor at all.
func TestComposeSSHChildLine_ClientKeepsTheTerminalOnStdin(t *testing.T) {
	dir, _ := fakeSSHOnPath(t)
	line := composeSSHChildLine(testChildStartCommand(), lifecyclepub.GrantRequest{
		Env: "ssh", Host: "box.example.com", User: "alice",
	})

	out := runComposedLine(t, line, dir, "hunter2")

	if strings.Contains(out, "STDIN_IS_PIPE") {
		t.Errorf("the ssh client was handed a pipe on stdin: it cannot prompt for a "+
			"password, a passphrase or a host key that way; output:\n%s", out)
	}
	if !strings.Contains(out, "STDIN_IS_TTY") {
		t.Errorf("the ssh client did not get the terminal on stdin; output:\n%s", out)
	}
}

// And the first thing it reads must be the human's answer — not a byte of
// the bootstrap. This is the owner's defect exactly: with the bootstrap
// staged ahead of authentication, the client's password attempt is our
// wrapper.
func TestComposeSSHChildLine_FirstReadIsTheTypedPassword(t *testing.T) {
	dir, _ := fakeSSHOnPath(t)
	line := composeSSHChildLine(testChildStartCommand(), lifecyclepub.GrantRequest{
		Env: "ssh", Host: "box.example.com", User: "alice",
	})

	out := runComposedLine(t, line, dir, "hunter2")

	if !strings.Contains(out, "FIRST_READ[hunter2]") {
		t.Errorf("the client's first read was not the typed password — the bootstrap "+
			"was staged ahead of authentication; output:\n%s", out)
	}
}

// The reverse forward is not a command-line value: the proven master opens it
// after authentication and frame 2 carries the allocated port.
func TestComposeSSHChildLine_CarriesDestinationWithoutAForward(t *testing.T) {
	dir, argsFile := fakeSSHOnPath(t)
	line := composeSSHChildLine(testChildStartCommand(), lifecyclepub.GrantRequest{
		Env: "ssh", Host: "box.example.com", User: "alice", Port: 2222,
	})

	runComposedLine(t, line, dir, "hunter2")

	raw, err := os.ReadFile(argsFile) // #nosec G304 — test-owned path.
	if err != nil {
		t.Fatalf("fake ssh recorded no argv: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-p", "2222", "alice@box.example.com"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q does not carry %q", joined, want)
		}
	}
	if strings.Contains(joined, "-R") {
		t.Errorf("argv %q carries a guessed reverse forward", joined)
	}
}
