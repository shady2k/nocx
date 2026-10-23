package app

// The shipped zsh path of nocx-1ebrs.39, end to end: a REAL zsh — the
// user's own login shell carrying the widget-wrapping plugin stack the
// owner's machine has — is started through the product's own local launch
// on a real pty, with the REAL kernel → publisher → grant-builder stack
// app.go wires. The user types an inline `ssh …` at its prompt; the shell's
// nested-launch widget intercepts it; the composed grant line runs the REAL
// ssh client against the REAL fixture sshd; the far shell establishes as
// the child over the reverse forward. Then the user types `exit`.
//
// What it watches a person do: leave an ssh session and get their pane
// back — the ssh block closes with the client's real exit status, the
// local prompt owns the lane again, and no attempt anywhere is left
// running. The parent shell's widget sends domain_activated the moment the
// ssh client returns, racing the child's forwarded-channel teardown on the
// OTHER transport; before the kernel accepted that ordering (nocx-1ebrs.39)
// the activation was refused, the suspended parent rejected every later
// frame, and the block stayed "Running" forever with the local prompt
// rendering inside it.
//
// The deterministic red for that ordering is the kernel unit test
// (internal/lifecycle/activate_under_live_child_test.go): on a loopback
// fixture the child's teardown can land before the parent's activation no
// matter what the test does, so this assembled chain proves the whole
// production path settles, and the unit test pins which frame wins the
// race and why.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

// lifecyclePair is one lifecycle descriptor pair: the end the adapter pumps
// and the end the shell inherits as fd 4 (LaunchOptions.LifecycleFD).
func lifecyclePair(t *testing.T) (kernelEnd, shellEnd *os.File) {
	t.Helper()
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	kernelEnd = os.NewFile(uintptr(fds[0]), "zsh-parent-kernel-end")
	shellEnd = os.NewFile(uintptr(fds[1]), "zsh-parent-shell-end")
	t.Cleanup(func() {
		_ = kernelEnd.Close()
		_ = shellEnd.Close()
	})
	return kernelEnd, shellEnd
}

// zshPluginFixture is one widget-wrapping plugin an ordinary zsh has
// (nocx-1ebrs.27's pins), resolved the way the shellintegration test
// resolves them: the pinned checkouts first, then the package prefixes.
type zshPluginFixture struct {
	name  string
	entry string
	dirs  []string
}

var zshFixturePlugins = []zshPluginFixture{
	{name: "fast-syntax-highlighting", entry: "fast-syntax-highlighting.plugin.zsh", dirs: []string{
		"share/zsh-fast-syntax-highlighting", "share/fast-syntax-highlighting",
		"opt/zsh-fast-syntax-highlighting/share/zsh-fast-syntax-highlighting",
	}},
	{name: "zsh-syntax-highlighting", entry: "zsh-syntax-highlighting.zsh", dirs: []string{
		"share/zsh-syntax-highlighting", "opt/zsh-syntax-highlighting/share/zsh-syntax-highlighting",
		"share/zsh/plugins/zsh-syntax-highlighting",
	}},
	{name: "zsh-autosuggestions", entry: "zsh-autosuggestions.zsh", dirs: []string{
		"share/zsh-autosuggestions", "opt/zsh-autosuggestions/share/zsh-autosuggestions",
		"share/zsh/plugins/zsh-autosuggestions",
	}},
}

// requireZshFixturePlugin fails — never skips — when one plugin is absent:
// a skip would report the owner's ordinary zsh setup as covered on every
// machine that has none.
func requireZshFixturePlugin(t *testing.T, p zshPluginFixture) string {
	t.Helper()
	var roots []string
	if dir := os.Getenv("NOCX_ZSH_PLUGINS"); dir != "" {
		roots = append(roots, filepath.Join(dir, p.name))
	}
	for _, prefix := range []string{os.Getenv("HOMEBREW_PREFIX"), "/opt/homebrew", "/usr/local", "/usr"} {
		if prefix == "" {
			continue
		}
		for _, d := range p.dirs {
			roots = append(roots, filepath.Join(prefix, d))
		}
	}
	for _, root := range roots {
		path := filepath.Join(root, p.entry)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	t.Fatalf("%s is required by this test and is not installed.\n"+
		"The fixture stands in for the widget-wrapping plugin stack an ordinary\n"+
		"zsh carries, and a skip here would report that setup as covered where\n"+
		"it is not. Provision the pinned checkouts and re-run:\n"+
		"  scripts/install-zsh-zle-plugins.sh <dir>\n"+
		"  NOCX_ZSH_PLUGINS=<dir> go test -count=1 -run '%s' ./internal/app/",
		p.name, t.Name())
	return ""
}

// writeZshPluginFixtureHome materialises the fixture $HOME: an ordinary zsh
// setup whose .zshrc loads the widget-wrapping plugins BEFORE nocx — the
// order the launcher's own phases produce for everybody who sources plugins
// the usual way.
func writeZshPluginFixtureHome(t *testing.T, wrapperDir string) string {
	t.Helper()
	var body strings.Builder
	body.WriteString("# An ordinary zsh setup: the widget-wrapping plugins most people have.\n")
	for _, p := range zshFixturePlugins {
		body.WriteString(". " + shellQuoteForSh(requireZshFixturePlugin(t, p)) + "\n")
	}
	// The fixture's ssh shim rides on PATH ahead of everything else; only
	// the directory is quoted, so $PATH keeps expanding. A test that runs no
	// ssh names no shim directory.
	if wrapperDir != "" {
		body.WriteString("export PATH=" + shellQuoteForSh(wrapperDir) + ":$PATH\n")
	}
	body.WriteString(`print -r -- "AGENT=${SSH_AUTH_SOCK-unset} WRAPPED=${widgets[accept-line]-none}"` + "\n")
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(body.String()), 0o600); err != nil {
		t.Fatalf("write fixture .zshrc: %v", err)
	}
	// A full-screen program with no dependencies: it enters the alternate
	// screen, draws, and leaves when its stdin names the quit - the exact
	// terminal shape of an editor session, the same bytes on every machine,
	// so the full-screen tests never depend on what a runner happens to
	// have installed (a pod without vi measured this).
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatalf("mkdir fixture bin: %v", err)
	}
	program := "#!/bin/sh\n" +
		"printf '\\033[?1049h\\033[H\\033[2J'\n" +
		"printf 'FIXTURE-SCREEN\\n'\n" +
		"read -r __line\n" +
		"printf '\\033[?1049l'\n"
	// #nosec G306 — the program is executable by design (0700, root-owned
	// test temp dir): it is run, never sourced.
	if err := os.WriteFile(filepath.Join(bin, "fixture-alt"), []byte(program), 0o700); err != nil {
		t.Fatalf("write fixture-alt: %v", err)
	}
	return home
}

func TestLiveSshd_ZshParentBlockClosesWhenTheSshChildExits(t *testing.T) {
	fx := startLiveSshd(t, true, withAssemblyBashEndpoint(t))
	logger := fx.log()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Fatalf("zsh is required by this test and was not found on PATH; "+
			"CI provisions it through scripts/install-zsh-ci.sh: %v", err)
	}

	k := lifecycle.New(lifecycle.Options{Rand: fx.rand})
	sessions := newSessionRegistry()
	transports := newTransportRegistry()
	// The lane is the adapter's own mint: the registration below names it,
	// not a caller-chosen id.

	// The typed-ssh delivery, wired as the composition root wires it (the
	// same shape newSSHChildHarness builds).
	sockRoot, err := os.MkdirTemp(filepath.Dir(ssh.DefaultControlRoot()), "nx")
	if err != nil {
		t.Fatalf("socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockRoot) })
	win := newHarnessWindow()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	typed := &typedRunner{
		log: logger,
		wrapper: ssh.NewTypedWrapper(logger,
			ssh.NewSSHConfigResolver(logger, os.DevNull, ""), sockRoot),
		dial:     DialTypedMux,
		publish:  &remoteInstallerAdapter{inner: shellintegration.New(logger)},
		sessions: harnessTerminals{win: win, done: done},
		probes:   defaultMasterProbes,
	}

	var pub *lifecyclepub.Publisher
	pub = lifecyclepub.New(k,
		lifecyclepub.WithGrantBuilder(newChildGrantBuilder(logger,
			func() *lifecyclepub.Publisher { return pub }, transports, sessions, nil, typed,
			func() string { return "" }, func() string { return "" }, func() []string { return nil })))
	facts := &factLog{}
	pub.SetEmitter(facts)

	// The parent's lifecycle channel: the local descriptor transport a real
	// local pane's shell speaks on, pumped by the production adapter.
	kernelEnd, shellEnd := lifecyclePair(t)
	adapter, err := lifecyclechannel.NewStream(logger, pub, kernelEnd)
	if err != nil {
		t.Fatalf("lifecycle adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	transports.register(adapter.TransportID(), transportKind{local: true})
	addr := adapter.Launch()
	lane := addr.Lane
	sessions.register(lane, assemblySID)

	// The parent shell: the user's own login shell, plugins and all,
	// started the way the helper's spawner starts a local pane.
	prevZDOTDIR, hadZDOTDIR := os.LookupEnv("ZDOTDIR")
	_ = os.Unsetenv("ZDOTDIR")
	t.Cleanup(func() {
		if hadZDOTDIR {
			_ = os.Setenv("ZDOTDIR", prevZDOTDIR)
		}
	})
	agentSock := startInProcessAgent(t, fx)
	wrapperDir := installSSHWrapper(t, fx)

	home := writeZshPluginFixtureHome(t, wrapperDir)

	opts := shellintegration.LaunchOptions{
		SessionID:   assemblySID,
		Enhanced:    true,
		Capability:  addr.Capability,
		Recovery:    addr.Recovery,
		Lane:        string(addr.Lane),
		Domain:      string(addr.Domain),
		Epoch:       addr.Epoch,
		LifecycleFD: 4,
	}
	lo, err := shellintegration.LocalEnhancedLaunchInMemory(zsh, shellintegration.ShellZsh, opts)
	if err != nil {
		t.Fatalf("LocalEnhancedLaunchInMemory: %v", err)
	}
	// #nosec G204 — lo.Command is the resolved login shell; starting a real
	// interactive shell is the only way to observe what a user gets.
	cmd := exec.Command(lo.Command, lo.Args...)
	cmd.ExtraFiles = append(append([]*os.File{}, lo.ExtraFiles...), shellEnd)
	// The shell inherits the test process's whole environment minus the
	// NOCX_* noise (the same shape the shellintegration launch tests use),
	// plus the fixture's own home and the ssh fixture's agent and wrapper:
	// the composed grant line runs INSIDE this shell and must find both.
	// DEDUPLICATED, later value wins: a raw append can leave two entries of
	// one name in the array, and a child that reads its environment with
	// libc getenv takes the FIRST — the inherited one — where the shell's
	// own table takes the last, so the shell and its children would
	// disagree about facts this test pins (measured on the pod:
	// SSH_AUTH_SOCK).
	envSet := map[string]string{}
	order := []string{}
	set := func(kv string) {
		k, _, _ := strings.Cut(kv, "=")
		if _, seen := envSet[k]; !seen {
			order = append(order, k)
		}
		envSet[k] = kv
	}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "NOCX_") || strings.HasPrefix(e, "__nocx_") || strings.HasPrefix(e, "ZDOTDIR=") {
			continue
		}
		set(e)
	}
	set("HOME=" + home)
	set("TMPDIR=" + t.TempDir())
	set("TERM=xterm")
	set("HISTFILE=/dev/null")
	set("PATH=" + wrapperDir + string(os.PathListSeparator) + os.Getenv("PATH"))
	set("SSH_AUTH_SOCK=" + agentSock)
	// NOCX_LIFECYCLE_TIMEOUT_MS stays at the script's own 10 s default (it
	// matches the kernel's hello timeout): the grant path runs the ssh -G
	// oracle, and a bound tightened for a fast machine turns a loaded
	// runner's grant into a conventional-ssh fallback instead of a failure.
	for _, kv := range lo.Env {
		set(kv)
	}
	set("NOCX_SNAPSHOT_WAIT_MS=15000")
	var env []string
	for _, k := range order {
		env = append(env, envSet[k])
	}
	cmd.Env = env

	ptmx, err := pty.Start(cmd)
	if err != nil {
		lo.Abort()
		t.Fatalf("pty start: %v", err)
	}
	lo.Cleanup()
	win.attach(ptmx)
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
	})
	var outMu sync.Mutex
	var out []byte
	paneOutput := func() string {
		outMu.Lock()
		defer outMu.Unlock()
		return string(out)
	}
	readPump := func() {
		buf := make([]byte, 8192)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				outMu.Lock()
				out = append(out, buf[:n]...)
				outMu.Unlock()
				win.feed(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}
	go readPump()

	parentDomain := addr.Domain
	// The parent integrated: its hello established the domain the adapter
	// minted, and the fixture really is the owner-shaped stack (the wrapped
	// accept-line is the accept-line).
	waittest.WaitForTimeoutDetail(t, "the parent domain established", 30*time.Second,
		func() string { return fmt.Sprintf("facts=%d", len(facts.all)) },
		func() bool {
			d, ok := k.Domain(parentDomain)
			return ok && d.State == lifecycle.DomainEstablished
		})
	waittest.WaitForTimeoutDetail(t, "the plugin stack bound the accept-line", 15*time.Second,
		func() string { return paneOutput() },
		func() bool {
			return strings.Contains(paneOutput(), "WRAPPED=user:_zsh_highlight_widget_") &&
				strings.Contains(paneOutput(), "AGENT="+agentSock)
		})

	// The prompt is up before anything is typed: the first prompt boundary
	// (its A marker) is the observable, and the first prompt is also where
	// the command snapshot finishes arriving.
	waittest.WaitForTimeoutDetail(t, "the first prompt", 30*time.Second,
		func() string { return fmt.Sprintf("pane bytes=%d", len(paneOutput())) },
		func() bool { return strings.Contains(paneOutput(), "\x1b]133;A") })

	// The user types an inline ssh line at the integrated prompt.
	line := fmt.Sprintf("ssh %s@127.0.0.1:%d", fx.user, fx.fixturePort())
	if _, err := ptmx.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("type the ssh line: %v", err)
	}

	// The nested chain runs: request → grant → suspend → the composed line
	// → the far shell establishes as the child over the reverse forward.
	var childDomain lifecycle.DomainID
	waittest.WaitForTimeoutDetail(t, "the ssh child established", 60*time.Second,
		func() string {
			o := paneOutput()
			if len(o) > 9000 {
				o = o[len(o)-9000:]
			}
			return fmt.Sprintf("commands=%v paneTail=%q", facts.commands(parentDomain), o)
		},
		func() bool {
			st, err := k.State(lane)
			if err != nil || len(st.Stack) != 2 {
				return false
			}
			top := st.Stack[len(st.Stack)-1]
			d, ok := k.Domain(top)
			if !ok || d.State != lifecycle.DomainEstablished {
				return false
			}
			childDomain = top
			return true
		})
	if dest, ok := facts.destinationOf(childDomain); ok {
		if dest.Host != "127.0.0.1" || dest.User != fx.user || dest.Port != fx.fixturePort() {
			t.Fatalf("destination = %+v, want the fixture sshd", dest)
		}
	} else {
		t.Fatalf("no published fact named the child's destination; commands=%v", facts.commands(parentDomain))
	}

	// The user leaves the remote session.
	if _, err := ptmx.Write([]byte("exit\n")); err != nil {
		t.Fatalf("type exit: %v", err)
	}

	// THE BLOCK CLOSES: the parent's ssh attempt completes with the client's
	// real exit status, and the lane is the parent's at a ready prompt.
	var sshAtt lifecycle.AttemptID
	waittest.WaitForTimeoutDetail(t, "the ssh block completed with the client's own exit status", 30*time.Second,
		func() string {
			return fmt.Sprintf("commands=%v facts=%d", facts.commands(parentDomain), len(facts.all))
		},
		func() bool {
			id, ok := facts.attemptFor(parentDomain, line)
			if !ok {
				return false
			}
			a, ok := k.Attempt(id)
			if !ok {
				return false
			}
			sshAtt = id
			return a.State == lifecycle.AttemptCompleted && a.ExitCode != nil && *a.ExitCode == 0
		})
	waittest.WaitForTimeoutDetail(t, "the lane back at the parent's ready prompt", 15*time.Second,
		func() string { return fmt.Sprintf("commands=%v", facts.commands(parentDomain)) },
		func() bool {
			st, err := k.State(lane)
			return err == nil && st.Domain == parentDomain && st.Lifecycle == lifecycle.LifecyclePromptReady
		})
	// And nothing of the closed child is left running: a block that can
	// never end must not exist anywhere on the lane.
	if _, open := k.OpenAttempt(childDomain); open {
		t.Fatal("the closed child domain still has an open attempt")
	}
	if a, _ := k.Attempt(sshAtt); a.State != lifecycle.AttemptCompleted {
		t.Fatalf("the ssh attempt = %v after everything settled, want completed", a.State)
	}
}

// THE OWNER'S SHAPE (a) OF nocx-1ebrs.39, IN A PANE WITH NO PRIOR SSH: a
// plain full-screen program run at the first prompt of a fresh integrated
// zsh - the widget-wrapping plugin stack loaded, nothing nested ever
// requested - must end as an ordinary block: the shell's complete accepted
// with the program's real exit status, and the prompt printed exactly ONCE
// after the program's screen ends. Twice would be two prompt boundaries for
// one command; inside a stale block would mean an interval that never
// sealed. The coordinator asked this shape proved (or reproduced) on its
// own, because the owner's vi case may never have had an ssh before it.
// The program is the fixture's own (writeZshPluginFixtureHome): the
// alternate-screen shape of an editor session, with no editor installed.
func TestZshPane_FreshPaneViBlockClosesAndPromptsOnce(t *testing.T) {
	fx := startLiveSshd(t, true) // the fixture's randomness and logger; no ssh runs here
	logger := fx.log()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Fatalf("zsh is required by this test and was not found on PATH; "+
			"CI provisions it through scripts/install-zsh-ci.sh: %v", err)
	}

	k := lifecycle.New(lifecycle.Options{Rand: fx.rand})
	pub := lifecyclepub.New(k)
	facts := &factLog{}
	pub.SetEmitter(facts)

	kernelEnd, shellEnd := lifecyclePair(t)
	adapter, err := lifecyclechannel.NewStream(logger, pub, kernelEnd)
	if err != nil {
		t.Fatalf("lifecycle adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	addr := adapter.Launch()

	home := writeZshPluginFixtureHome(t, "")
	prevZDOTDIR, hadZDOTDIR := os.LookupEnv("ZDOTDIR")
	_ = os.Unsetenv("ZDOTDIR")
	t.Cleanup(func() {
		if hadZDOTDIR {
			_ = os.Setenv("ZDOTDIR", prevZDOTDIR)
		}
	})

	opts := shellintegration.LaunchOptions{
		SessionID:   assemblySID,
		Enhanced:    true,
		Capability:  addr.Capability,
		Recovery:    addr.Recovery,
		Lane:        string(addr.Lane),
		Domain:      string(addr.Domain),
		Epoch:       addr.Epoch,
		LifecycleFD: 4,
	}
	// #nosec G204 — addr-resolved login shell; a real interactive zsh is the
	// thing under test.
	lo, err := shellintegration.LocalEnhancedLaunchInMemory(zsh, shellintegration.ShellZsh, opts)
	if err != nil {
		t.Fatalf("LocalEnhancedLaunchInMemory: %v", err)
	}
	// #nosec G204 — lo.Command is the resolved login shell; starting a real
	// interactive shell is the only way to observe what a user gets.
	cmd := exec.Command(lo.Command, lo.Args...)
	cmd.ExtraFiles = append(append([]*os.File{}, lo.ExtraFiles...), shellEnd)
	// The full environment minus this test's own noise (see the ssh fixture
	// for why it is deduplicated, later value wins). The lifecycle read
	// bound stays at the script's own default.
	envSet := map[string]string{}
	order := []string{}
	set := func(kv string) {
		key, _, _ := strings.Cut(kv, "=")
		if _, seen := envSet[key]; !seen {
			order = append(order, key)
		}
		envSet[key] = kv
	}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "NOCX_") || strings.HasPrefix(e, "__nocx_") || strings.HasPrefix(e, "ZDOTDIR=") {
			continue
		}
		set(e)
	}
	set("HOME=" + home)
	set("TMPDIR=" + t.TempDir())
	set("TERM=xterm")
	set("HISTFILE=/dev/null")
	for _, kv := range lo.Env {
		set(kv)
	}
	set("NOCX_SNAPSHOT_WAIT_MS=15000")
	var env []string
	for _, key := range order {
		env = append(env, envSet[key])
	}
	cmd.Env = env

	ptmx, err := pty.Start(cmd)
	if err != nil {
		lo.Abort()
		t.Fatalf("pty start: %v", err)
	}
	lo.Cleanup()
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
	})
	var outMu sync.Mutex
	var out []byte
	paneOutput := func() string {
		outMu.Lock()
		defer outMu.Unlock()
		return string(out)
	}
	go func() {
		buf := make([]byte, 8192)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				outMu.Lock()
				out = append(out, buf[:n]...)
				outMu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()

	parentDomain := addr.Domain
	lane := addr.Lane
	waittest.WaitForTimeoutDetail(t, "the fresh pane integrated", 30*time.Second,
		func() string { return fmt.Sprintf("facts=%d", len(facts.all)) },
		func() bool {
			d, ok := k.Domain(parentDomain)
			return ok && d.State == lifecycle.DomainEstablished
		})
	// The first prompt is up, and the fixture really is the owner-shaped
	// stack: the wrapped accept-line is the accept-line.
	waittest.WaitForTimeoutDetail(t, "the first prompt with the plugin stack bound", 30*time.Second,
		func() string { return paneOutput() },
		func() bool {
			o := paneOutput()
			return strings.Contains(o, "WRAPPED=user:_zsh_highlight_widget_") &&
				strings.Contains(o, "\x1b]133;A")
		})

	// The user runs a full-screen program and quits it. Every wait below
	// ends on an observable state: the alternate screen entered, the
	// program's screen drawn, the alternate screen left. The program is the
	// fixture's own (writeZshPluginFixtureHome): the same alternate-screen
	// shape an editor makes, with nothing installed.
	if _, err := ptmx.Write([]byte("~/bin/fixture-alt\n")); err != nil {
		t.Fatalf("type the full-screen program: %v", err)
	}
	waittest.WaitForTimeoutDetail(t, "the program entered the alternate screen", 30*time.Second,
		func() string { return paneOutput() },
		func() bool { return strings.Contains(paneOutput(), "\x1b[?1049h") })
	waittest.WaitForTimeoutDetail(t, "the program drew its screen", 30*time.Second,
		func() string { return paneOutput() },
		func() bool {
			o := paneOutput()
			i := strings.LastIndex(o, "\x1b[?1049h")
			return i >= 0 && strings.Contains(o[i:], "FIXTURE-SCREEN")
		})
	if _, err := ptmx.Write([]byte("q\n")); err != nil {
		t.Fatalf("quit the program: %v", err)
	}
	waittest.WaitForTimeoutDetail(t, "the program left the alternate screen", 30*time.Second,
		func() string { return paneOutput() },
		func() bool {
			o := paneOutput()
			i := strings.LastIndex(o, "\x1b[?1049h")
			return i >= 0 && strings.Contains(o[i:], "\x1b[?1049l")
		})

	// THE BLOCK CLOSES: the shell's complete for vi is accepted with the
	// program's real exit status, and the lane is back at a ready prompt.
	var viAtt lifecycle.AttemptID
	waittest.WaitForTimeoutDetail(t, "the full-screen block completed with the program's own exit status", 30*time.Second,
		func() string { return fmt.Sprintf("commands=%v", facts.commands(parentDomain)) },
		func() bool {
			id, ok := facts.attemptFor(parentDomain, "~/bin/fixture-alt")
			if !ok {
				return false
			}
			a, ok := k.Attempt(id)
			if !ok {
				return false
			}
			viAtt = id
			return a.State == lifecycle.AttemptCompleted && a.ExitCode != nil && *a.ExitCode == 0
		})
	waittest.WaitForTimeoutDetail(t, "the lane back at a ready prompt", 15*time.Second,
		func() string { return fmt.Sprintf("commands=%v", facts.commands(parentDomain)) },
		func() bool {
			st, err := k.State(lane)
			return err == nil && st.Domain == parentDomain && st.Lifecycle == lifecycle.LifecyclePromptReady
		})
	// ...and zle is back: the boundary's own B mark after the prompt's A is
	// the observable that the boundary finished rendering before the count.
	waittest.WaitForTimeoutDetail(t, "the line editor re-armed after the boundary", 15*time.Second,
		func() string { return paneOutput() },
		func() bool {
			o := paneOutput()
			i := strings.LastIndex(o, "\x1b[?1049l")
			return i >= 0 && strings.Contains(o[i:], "\x1b]133;B")
		})

	if _, open := k.OpenAttempt(parentDomain); open {
		t.Fatal("a fresh pane left an open attempt behind the full-screen program")
	}
	if a, _ := k.Attempt(viAtt); a.State != lifecycle.AttemptCompleted {
		t.Fatalf("the vi attempt = %v after everything settled, want completed", a.State)
	}

	// THE PROMPT PRINTED ONCE. The region after the program's screen ends
	// holds exactly one boundary: one fence, one D, one A - not the two
	// boundaries (two prompts, blank lines between) the owner's pane showed.
	o := paneOutput()
	i := strings.LastIndex(o, "\x1b[?1049l")
	if i < 0 {
		t.Fatal("no alternate-screen exit in the pane; nothing to count")
	}
	region := o[i:]
	if got := strings.Count(region, "\x1b]1337;NOCX_FENCE;"); got != 1 {
		t.Errorf("fences after the program quit = %d, want exactly 1 (one closed boundary): %q", got, printableEsc(region))
	}
	if got := strings.Count(region, "\x1b]133;D;"); got != 1 {
		t.Errorf("end marks after the program quit = %d, want exactly 1: %q", got, printableEsc(region))
	}
	if got := strings.Count(region, "\x1b]133;A"); got != 1 {
		t.Errorf("prompt marks after the program quit = %d, want exactly 1 (the prompt printed once): %q", got, printableEsc(region))
	}
}

// printableEsc renders control bytes visibly for a failure message.
func printableEsc(b string) string {
	var sb strings.Builder
	for _, r := range b {
		switch {
		case r == '\x1b':
			sb.WriteString("<ESC>")
		case r == '\a':
			sb.WriteString("<BEL>")
		case r == '\r':
			sb.WriteString("<CR>")
		case r == '\n':
			sb.WriteString("<LF>")
		case r < 0x20:
			sb.WriteString(fmt.Sprintf("<%02x>", r))
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
