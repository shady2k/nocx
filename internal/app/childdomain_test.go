package app

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/ssh"
)

// composeSSHChildLine is the production composer called the way the ssh child
// calls it: our own multiplex options, the -t and the -R forward the child
// needs, the user's own words, and the remote command last. It stands in for
// the call site so each test below states only what it is about.
//
// The composer used to be a function of its own with this signature. ADR-0035
// merged it with the typed wrapper's: there is one composer for "the line a
// parent shell runs" now, and the ssh child is one of its callers.
func composeSSHChildLine(startCmd string, remotePort, localPort int, req lifecyclepub.GrantRequest) string {
	wrap := ssh.TypedWrap{MuxOptions: []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/tmp/nocx-mux-0/m-%C",
		"-o", "ControlPersist=no",
	}}
	inv := ssh.TypedInvocation{Opts: req.Opts, Host: req.Host, User: req.User, Port: req.Port}
	extra := []string{"-t", "-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", remotePort, localPort)}
	return composeSSHLine(wrap, extra, inv, startCmd)
}

// TestComposeSSHChildLine_LineIsExecutableAndCarriesTheForward is the ssh
// child's wire contract (ADR-0022: the ssh command line is the carrier):
// the grant's bootstrap is a SINGLE self-contained command line the parent
// evals — the -R reverse forward naming the child's listener, the
// destination, and the launcher command sshd runs on the far side. The line
// must parse under bash (the parent's shell) and must never lose the
// launcher command to quoting.
//
// What it must NOT do is wrap the client: nocx-beib. The bootstrap used to
// be piped into the client's stdin ahead of the connection, which took the
// terminal away from the authentication phase; the behavioural proof of
// that lives in childdomain_password_test.go, and the shape assertions
// here keep the pipe from creeping back.
func TestComposeSSHChildLine_LineIsExecutableAndCarriesTheForward(t *testing.T) {
	startCmd := `env -u BASH_ENV bash -c 'printf "it'"'"'s"'`
	line := composeSSHChildLine(startCmd, 40123, 37777, lifecyclepub.GrantRequest{
		Env: "ssh", Host: "box.example.com", User: "alice", Port: 2222,
	})

	if !strings.Contains(line, "'127.0.0.1:40123:127.0.0.1:37777'") {
		t.Errorf("line does not carry the -R forward: %s", line)
	}
	if !strings.Contains(line, "-p 2222") {
		t.Errorf("line does not carry the typed port: %s", line)
	}
	if !strings.Contains(line, "'alice@box.example.com'") {
		t.Errorf("line does not carry the quoted destination: %s", line)
	}
	// One -t: the client's stdin is the parent's terminal, so OpenSSH
	// allocates the remote pty without being forced. -tt was a consequence
	// of the pipe and must not return with it.
	if !strings.Contains(line, "'-t'") {
		t.Errorf("line does not request a remote pty with a single -t: %s", line)
	}
	// Our own two options, and only ours: the user's process is the
	// multiplex master and the interactive session both (ADR-0035).
	for _, want := range []string{"ControlMaster=auto", "ControlPath=", "ControlPersist=no"} {
		if !strings.Contains(line, want) {
			t.Errorf("line does not make the user's own process the master (%s missing): %s", want, line)
		}
	}
	if strings.Contains(line, "-tt") {
		t.Errorf("line forces a pty with -tt, which is only needed when the client's "+
			"stdin is not a terminal — the authentication phase needs it to be one: %s", line)
	}
	// No pipeline into ssh, and no termios window around it: both are the
	// in-band shape nocx-beib removed.
	if strings.Contains(line, "| ssh") || strings.Contains(line, "stty") {
		t.Errorf("line still wraps the client in a pipeline or a raw-mode window: %s", line)
	}

	// The line must parse under bash — the parent evals it verbatim, and a
	// quoting slip in the launcher command is a dead remote shell.
	f, err := os.CreateTemp(t.TempDir(), "line-*.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	// #nosec G204 — "bash" and a temp file this test just wrote; the point of
	// the test is that the composed line parses, which needs a real parser.
	cmd := exec.Command("bash", "-n", f.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("composed line does not parse under bash: %v\n%s\nline:\n%s", err, out, line)
	}
}

// TestComposeSSHChildLine_StartCommandSurvivesQuoting proves the round trip
// of the launcher command's nastiest bytes: sshd must receive it byte for
// byte as ONE argument. The launcher command is ~38 KiB of shell with
// embedded quotes and newlines, so a quoting slip here is a far shell that
// dies on a syntax error with the user watching.
func TestComposeSSHChildLine_StartCommandSurvivesQuoting(t *testing.T) {
	startCmd := "line one\nline two with 'quotes' and \"double\" and \\backslashes\\ and $dollars\n"
	line := composeSSHChildLine(startCmd, 40123, 37777, lifecyclepub.GrantRequest{Env: "ssh", Host: "h"})

	// A stand-in ssh that prints its LAST argument — the command sshd would
	// run — so the bytes can be compared against what went in.
	binDir := t.TempDir()
	fakeSSH := "#!/bin/sh\nfor a in \"$@\"; do last=\"$a\"; done\nprintf '%s' \"$last\"\n"
	// #nosec G306 — a stand-in for ssh must be executable to be found through
	// PATH; temp dir, no secret.
	if err := os.WriteFile(binDir+"/ssh", []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	prog := "PATH=" + binDir + ":$PATH\n" + line + "\n"
	// #nosec G204 — prog is the composed line under test plus a PATH pointing
	// at this test's temp dir; evaluating it under a real bash is the
	// assertion, not an accident.
	cmd := exec.Command("bash", "-c", prog)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating the composed line: %v\n%s", err, out)
	}
	if string(out) != startCmd {
		t.Errorf("sshd would receive %q, want the launcher command verbatim %q", string(out), startCmd)
	}
}

// TestComposeSSHChildLine_CarriesTheOptionsTheUserTyped guards nocx-c6z0, and
// it asserts the EXECUTED argv rather than the composed string on purpose.
//
// The line is rebuilt from the grant request rather than edited, so anything
// the request does not carry is not reordered — it is gone. It carried host,
// user and port and nothing else, while the shell's detector deliberately
// ACCEPTS a line bearing -i, -o, -F, -J, -l, -e, -b and -m. So
// `ssh -i ~/.ssh/prod -J bastion host` was executed as a bare `ssh host`: the
// wrong key, no jump host, the default host-key policy. Nothing said so,
// because the block the user sees shows the line they TYPED — which is why
// nocxify-journey's assertions on that block all passed while the connection
// it was watching never reached userauth.
//
// A stand-in ssh reports its own argv, so what is checked is what the client
// would actually receive: one argv entry per typed token, in order, with the
// spaces and quotes inside an argument intact.
func TestComposeSSHChildLine_CarriesTheOptionsTheUserTyped(t *testing.T) {
	opts := []string{
		"-i", "/home/u/.ssh/id key", // a path with a space
		"-o", "StrictHostKeyChecking=no", // the option this test exists for
		"-o", "ProxyCommand=nc -X 5 %h %p", // spaces AND a percent
		"-J", "bastion.example.com", // a jump host, silently dropped before
		"-l", "alice", // a login name given as an option
	}
	line := composeSSHChildLine("true", 40123, 37777, lifecyclepub.GrantRequest{
		Env: "ssh", Host: "box.example.com", Port: 2222, Opts: opts,
	})

	// A stand-in ssh that prints its argv one entry per line, so an argument
	// that was split or joined by quoting is visible as such.
	binDir := t.TempDir()
	fakeSSH := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"
	// #nosec G306 — a stand-in for ssh must be executable to be found through
	// PATH; temp dir, no secret.
	if err := os.WriteFile(binDir+"/ssh", []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	// #nosec G204 — the composed line under test plus a PATH pointing at this
	// test's temp dir; evaluating it under a real bash IS the assertion.
	cmd := exec.Command("bash", "-c", "PATH="+binDir+":$PATH\n"+line+"\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating the composed line: %v\n%s\nline:\n%s", err, out, line)
	}
	argv := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")

	// Every typed token is one argv entry, and they appear in the order they
	// were typed. Checked as a contiguous run: ssh reads `-o X` as a pair, so
	// an option separated from its argument is a different request.
	if !containsRun(argv, opts) {
		t.Errorf("the client would not receive the options the user typed.\nwant the run %q\ngot argv %q\nline: %s",
			opts, argv, line)
	}
	// The port is still modelled, and still appears exactly once — the
	// detector does not collect -p, so carrying it here too would send two.
	if n := countArg(argv, "-p"); n != 1 {
		t.Errorf("-p appears %d times in %q, want exactly 1", n, argv)
	}
	// And a second -t never appears: ssh reads `-t -t` as -tt, which forces a
	// pty the authentication phase must not be given.
	if n := countArg(argv, "-t"); n != 1 {
		t.Errorf("-t appears %d times in %q, want exactly 1", n, argv)
	}
}

// containsRun reports whether want appears in argv as a contiguous run.
func containsRun(argv, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(argv); i++ {
		match := true
		for j := range want {
			if argv[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func countArg(argv []string, arg string) int {
	n := 0
	for _, a := range argv {
		if a == arg {
			n++
		}
	}
	return n
}
