//go:build nocx_local_ssh

package app

// The dialing half of the live-sshd fixture: the session opener, the fixtures
// and drivers only a journey needs, and the five journeys themselves.
//
// # Why it is a file of its own
//
// The fixture — the real sshd subprocess, its config, the buffers — is SHARED
// with the untagged build, which is why it stays in live_sshd_test.go. What
// must live behind the tag is what makes a connection: `ssh.RealClient.Connect`
// here, and `AcquirePooled` for the lifecycle channel's tunnel stand. That
// split arrived with nocx-50w7p.5, which compiled internal/ssh's dial half only
// into the helper's build (nocx_local_ssh); a coordinator build has no
// `RealClient.Connect` to call at all, which is the invariant the split exists
// to hold.
//
// The five journeys drive the coordinator's own dial, and that is a STAND
// rather than the product: every ssh pane in production is opened by this
// machine's helper, and re-pointing these onto that route is plan §7's commit —
// not the split's, which is why they moved here rather than being rewritten.
// What they assert is the far side: a real OpenSSH, a real bash, a real
// lifecycle domain, and the canary that must not reach the far host.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// liveSshdDial is the state a DIALING build's fixture keeps: the pooled client
// the journeys open sessions with, the lanes the provider registered, and the
// launcher a stand replaces. Its declaration is per build
// (live_sshd_dial_state_absent_test.go is the empty half) because the fixture
// is one type in both builds and a struct's fields cannot carry a build tag.
type liveSshdDial struct {
	client *ssh.RealClient // the pooled client, for the connection-loss proof
	// registeredLanes records the lane→session bindings the provider reported
	// (the production RegisterLifecycleLane wiring); the tests assert the
	// minted lane reached the session it belongs to.
	registeredLanes []string
	// launcher, when set, replaces the launcher adapter connect() would build.
	// It is how the emitted remote command is recorded: the product
	// deliberately never logs it (it used to carry both bearers).
	launcher ssh.RemoteLauncher
}

// newLiveSshdDial answers the fixture's dial-side state. It is empty at
// construction — connect() fills the client, the provider fills the lanes, and
// a stand replaces the launcher — so it exists to be the one place the shared
// field is written in BOTH builds.
func newLiveSshdDial() liveSshdDial { return liveSshdDial{} }

// withSshdConfig appends lines to sshd_config. A Match block must be last,
// and only one caller may use it, so it is a single string rather than a
// list.
func withSshdConfig(lines string) liveSshdOption {
	return func(c *liveSshdConfig) { c.extraConfig = lines }
}

// withFarSideRecording turns on the three fixture-owned surfaces the canary
// is asserted against on the far host: a private TMPDIR, a real HISTFILE, and
// a recorder that writes the argv and the environment of the very process
// that runs our exec request.
//
// The recorder has to run INSIDE that process, because the argv exists
// nowhere else and not for long: sshd runs an exec request as
// `<login shell> -c <request>`, and the loader immediately execs, which
// replaces the argv. Nothing outside can look in time.
//
// The seam is `~/.bashrc`, and which seam it is was MEASURED rather than
// assumed. $BASH_ENV is the obvious answer and it is the wrong one here: bash
// reads $BASH_ENV for a non-interactive shell only when it does not think it
// was started by sshd, and when SSH_CLIENT is in the environment it sources
// `~/.bashrc` INSTEAD. A first attempt set BASH_ENV through sshd's SetEnv;
// the variable arrived on the far side (verified in the session's own
// environment) and the file was never sourced. So the recorder is sourced
// from the fixture's own `~/.bashrc`, and BASH_ENV is left pointing at it as
// well, so either rule fires and the records simply append.
//
// It writes to files and never to a descriptor of the session: a byte on
// stdout here would land in the middle of the loader's frame protocol.
func withFarSideRecording() liveSshdOption {
	return func(c *liveSshdConfig) { c.record = true }
}

// forceInstalledVersion turns the current committed bundle into an older,
// still-valid activation. The next EnsureInstalledRemote must therefore
// stage a new generation and atomically replace the existing manifest.
func forceInstalledVersion(t *testing.T, home, oldVersion string) {
	t.Helper()
	root := filepath.Join(home, ".nocx")
	manifestPath := filepath.Join(root, "manifest.json")
	data, readErr := os.ReadFile(manifestPath) // #nosec G304 — manifestPath is under the fixture-owned t.TempDir home.
	if readErr != nil {
		t.Fatalf("read installed manifest: %v", readErr)
	}
	var manifest map[string]any
	if decodeErr := json.Unmarshal(data, &manifest); decodeErr != nil {
		t.Fatalf("decode installed manifest: %v", decodeErr)
	}
	generation, ok := manifest["generation"].(string)
	if !ok || generation == "" {
		t.Fatalf("installed manifest generation = %#v", manifest["generation"])
	}
	oldGeneration := "v" + oldVersion
	if renameErr := os.Rename(
		filepath.Join(root, "integration", generation),
		filepath.Join(root, "integration", oldGeneration),
	); renameErr != nil {
		t.Fatalf("rename installed generation: %v", renameErr)
	}
	manifest["version"] = oldVersion
	manifest["generation"] = oldGeneration
	data, encodeErr := json.MarshalIndent(manifest, "", "  ")
	if encodeErr != nil {
		t.Fatalf("encode older manifest: %v", encodeErr)
	}
	data = append(data, '\n')
	if writeErr := os.WriteFile(manifestPath, data, 0o600); writeErr != nil {
		t.Fatalf("write older manifest: %v", writeErr)
	}
}

// authCount is how many times the server accepted an authentication — the
// observable that says whether a refusal cost the user a second credential
// use. LogLevel VERBOSE is what makes it observable.
func (fx *liveSshd) authCount() int {
	return strings.Count(fx.logBuf.String(), "Accepted publickey")
}

// connect opens a real SSH session to the fixture sshd through ssh.RealClient
// with the app package's own lifecycle provider and launcher adapter — the
// same composition root types app.go wires — and starts collecting the
// terminal output. An optional installer exercises the saved-profile
// publication path inside RealClient.Connect.
func (fx *liveSshd) connect(t *testing.T, kernel *recordingKernel, shell ssh.ShellKind, installers ...ssh.RemoteInstaller) (ssh.Channel, *outputBuffer) {
	t.Helper()
	logger := fx.log()
	client, err := ssh.NewReal(logger, ssh.WithKnownHostsFile(fx.knownHostsPath(t)))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	fx.client = client

	provider := &remoteLifecycleProvider{
		// The tunnel this journey's lifecycle channel rides. See
		// liveSshdTunnelTransport's own doc for why the transport is the one
		// stand in a journey whose subject is the far side.
		tunnels: liveSshdTunnelTransport{lease: fx.tunnelLease(t)},
		kernel:  kernel,
		logger:  logger,
		registerLane: func(lane lifecycle.LaneID, sid string) {
			fx.registeredLanes = append(fx.registeredLanes, string(lane)+"->"+sid)
		},
	}
	var launcher ssh.RemoteLauncher = &remoteLauncherAdapter{inner: shellintegration.NewRemoteLauncher(), logger: logger}
	if fx.launcher != nil {
		launcher = fx.launcher
	}

	opts := []ssh.ConnectOption{
		ssh.WithUser(fx.user),
		ssh.WithPTYSize(100, 30, 0, 0),
		ssh.WithTimeout(20 * time.Second),
		ssh.WithSessionID("sid-live-sshd"),
		ssh.WithEnhanced(),
		ssh.WithShell(shell),
		ssh.WithRemoteLifecycle(provider),
		ssh.WithRemoteLauncher(launcher),
	}
	if fx.clientKey != "" {
		opts = append(opts, ssh.WithKeyFile(fx.clientKey))
	} else {
		opts = append(opts, ssh.WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(fx.signer)}))
	}
	if len(installers) > 0 {
		opts = append(opts, ssh.WithRemoteInstaller(installers[0]))
	}
	ch, err := client.Connect(context.Background(), fx.addr, opts...)
	if err != nil {
		t.Fatalf("connect to %s: %v", fx.addr, err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	out := &outputBuffer{}
	go func() { _, _ = io.Copy(out, ch) }()
	return ch, out
}

// The proofs.

// TestLiveSshd_BashReachesAcceptedDomain proves the primary path (nocx-u7uh.4
// acceptance): a real bash shell on the other side of a real sshd reaches the
// accepted domain with nothing installed there, and commands run there
// produce authenticated start/complete with the right exit status and the
// render fence on the terminal.
func TestLiveSshd_BashReachesAcceptedDomain(t *testing.T) {
	fx := startLiveSshd(t, true)
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellBash, liveBundleCarrier(t, fx))

	waittest.WaitForTimeout(t, "domain established", 15*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		if kernel.minted != 1 {
			return false
		}
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	// The minted lane reached the session that owns it: the production
	// RegisterLifecycleLane wiring (without it, every published fact is
	// dropped at the transport and enhanced mode never engages).
	if len(fx.registeredLanes) != 1 || !strings.HasSuffix(fx.registeredLanes[0], "->sid-live-sshd") {
		t.Fatalf("registered lanes = %v, want exactly one lane bound to sid-live-sshd", fx.registeredLanes)
	}

	// Line 1: print a sentinel and stay open long enough for the test to
	// observe the open attempt.
	att0 := runLine(t, ch, kernel, "printf 'PROOF_BASH_123\\n'; sleep 0.3", 0)
	// The render fence the kernel recorded must be the exact bytes the shell
	// wrote to the terminal (protocol doc §8).
	fence := fmt.Sprintf("\x1b]1337;NOCX_FENCE;%x\x07", att0.Fence)
	waittest.WaitForTimeout(t, "sentinel output and fence", 10*time.Second, func() bool {
		return strings.Contains(out.String(), "PROOF_BASH_123") &&
			strings.Contains(out.String(), fence)
	})

	// Line 2: a failing command completes with exit status 1.
	runLine(t, ch, kernel, "sh -c 'sleep 0.3; exit 1'", 1)

	// The lane is back at a ready prompt for the domain.
	waittest.WaitForTimeout(t, "lane back at PromptReady", 10*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		st, err := kernel.State(kernel.lane)
		if err != nil {
			return false
		}
		return st.Lifecycle == lifecycle.LifecyclePromptReady && st.Domain == kernel.domain
	})

	// The session ends with the shell. domain_closed is deliberately
	// best-effort (the hook's own contract: the process exit may race the
	// send, and the kernel then ends the domain via the transport-loss
	// path, which the connection-loss test proves separately), so the
	// assertion is the session ending, not a promised terminal state.
	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	waittest.WaitForTimeout(t, "session end after exit", 15*time.Second, func() bool {
		select {
		case <-ch.Done():
			return true
		default:
			return false
		}
	})

	// Nothing installed: the session home holds only the fixture .bashrc and
	// the launcher's own ~/.nocx bundle, and no file carries the capability.
	assertSessionLeftOnlyTheLauncherBundle(t, fx.home, kernel.capabilityHex())
}

// TestLiveSshd_RemoteBundleRepublishReplacesManifest proves nocx-340t
// against OpenSSH itself: after a host has a committed older activation, a
// second SFTP publish atomically replaces manifest.json instead of receiving
// SSH_FX_FAILURE, and a subsequent enhanced session establishes its domain.
func TestLiveSshd_RemoteBundleRepublishReplacesManifest(t *testing.T) {
	fx := startLiveSshd(t, true)
	installer := liveBundleCarrier(t, fx)
	if err := installer.EnsureInstalledRemote(context.Background(), fx.addr); err != nil {
		t.Fatalf("first remote publish: %v", err)
	}
	forceInstalledVersion(t, fx.home, "0")

	kernel := newRecordingKernel()
	ch, _ := fx.connect(t, kernel, ssh.ShellBash, installer)
	waittest.WaitForTimeout(t, "domain established after republish", 15*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		if kernel.minted != 1 {
			return false
		}
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
}

// TestLiveSshd_ForwardingRefusedStaysConventional proves the refusal
// contract: a host whose sshd will not forward (AllowTcpForwarding no)
// produces a conventional terminal with a visible native prompt, no dialog,
// and no diagnostic naming a policy — refusal is detectable synchronously
// but not distinguishable (ADR-0024 decision 4).
func TestLiveSshd_ForwardingRefusedStaysConventional(t *testing.T) {
	fx := startLiveSshd(t, false)
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellBash, liveBundleCarrier(t, fx))

	// The refusal is synchronous: no domain may ever be minted. The native
	// prompt is the observable that the bootstrap has finished and the
	// channel is ready for ordinary terminal input.
	waittest.WaitForTimeoutDetail(t, "native prompt after refused forwarding", 20*time.Second,
		func() string {
			kernel.mu.Lock()
			minted := kernel.minted
			kernel.mu.Unlock()
			return fmt.Sprintf("minted %d domain(s); terminal:\n%s", minted, out.String())
		},
		func() bool {
			return strings.Contains(out.String(), "NATIVE_PROMPT>")
		})
	kernel.mu.Lock()
	minted := kernel.minted
	kernel.mu.Unlock()
	if minted != 0 {
		t.Fatalf("refused forwarding still minted %d domain(s)", minted)
	}

	// The fixture .bashrc names the prompt NATIVE_PROMPT>; with no live
	// channel the marker-only overlay keeps it visible (ADR-0024 decision 9).
	// Run a command only after the bootstrap has released input: the terminal
	// is an ordinary usable shell.
	if _, err := ch.Write([]byte("echo CONVENTIONAL_OK\n")); err != nil {
		t.Fatalf("write echo: %v", err)
	}
	waittest.WaitForTimeout(t, "a usable conventional terminal", 20*time.Second, func() bool {
		s := out.String()
		return strings.Contains(s, "NATIVE_PROMPT>") && strings.Contains(s, "CONVENTIONAL_OK")
	})

	// No diagnostic naming the policy may leak into the user-visible output
	// (the Go client's tcpip-forward refusal is not a terminal message, and
	// nothing in the launcher or the hooks may print one). The scan runs on
	// the control-stripped text: the terminal's structured markers (OSC 636
	// command snapshots etc.) are telemetry, not diagnostics.
	low := strings.ToLower(stripControl(out.String()))
	for _, word := range []string{"forward", "tcpip", "AllowTcpForwarding", "refused"} {
		if strings.Contains(low, word) {
			t.Fatalf("refusal leaked a policy diagnostic (%q) into the terminal:\n%s", word, out.String())
		}
	}

	// And the shell still ends cleanly.
	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
}

// TestLiveSshd_ConnectionLossRevokesDomain proves protocol §12: losing the
// SSH connection revokes the domain and abandons its open attempt as
// unknown — never success.
func TestLiveSshd_ConnectionLossRevokesDomain(t *testing.T) {
	fx := startLiveSshd(t, true)
	kernel := newRecordingKernel()
	ch, _ := fx.connect(t, kernel, ssh.ShellBash, liveBundleCarrier(t, fx))

	waittest.WaitForTimeout(t, "domain established", 15*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		if kernel.minted != 1 {
			return false
		}
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	// Open a long-running attempt, then lose the SSH connection under it.
	// Closing the pooled client is the faithful loss trigger: real OpenSSH
	// forks per connection, so killing the sshd parent would leave the
	// session's connection (and the forwarded port) alive — the transport
	// loss path protocol §12 is about is the connection shutting down, which
	// is what the client's Close does.
	if _, err := ch.Write([]byte("sleep 60\n")); err != nil {
		t.Fatalf("write sleep: %v", err)
	}
	var att lifecycle.ExecutionAttempt
	waittest.WaitForTimeout(t, "the sleep attempt to be open", 15*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		a, ok := kernel.OpenAttempt(kernel.domain)
		if ok {
			att = a
		}
		return ok
	})
	if err := fx.client.Close(); err != nil {
		t.Fatalf("close pooled client: %v", err)
	}

	// The domain is lost and the open attempt becomes unknown — never
	// completed, never successful.
	waittest.WaitForTimeout(t, "domain lost", 20*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainLost
	})
	waittest.WaitForTimeout(t, "open attempt unknown", 20*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		a, ok := kernel.Attempt(att.ID)
		return ok && a.State == lifecycle.AttemptUnknown && a.ExitCode == nil
	})
}

// TestLiveSshd_ZshAdapterReachesAcceptedDomain proves the zsh tier end to
// end (deliverable 2): the zsh hook reaches the forwarded port through
// zmodload zsh/net/tcp + ztcp, performs the same hello/accept handshake with
// the same capability gating, and reports start/complete with the exit
// status — no prompt suppression happens before accept.
func TestLiveSshd_ZshAdapterReachesAcceptedDomain(t *testing.T) {
	// zsh is a hard prerequisite (the launcher execs it on the far host);
	// fail, never skip, with the container guidance — the go-tests image
	// carries zsh, the host may not (nocx-gd84).
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Fatalf("zsh is required by this test and missing from PATH.\n" +
			"The zsh tier proof must not silently skip (nocx-gd84). Run the suite in the\n" +
			"containerized runner: .githooks/containerized-tests.sh (the go-tests image\n" +
			"carries zsh), or provision zsh on this host and re-run.")
	}

	fx := startLiveSshd(t, true)
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellZsh, liveBundleCarrier(t, fx))

	waittest.WaitForTimeoutDetail(t, "domain established", 15*time.Second,
		func() string { return fmt.Sprintf("terminal:\n%s", out.String()) },
		func() bool {
			kernel.mu.Lock()
			defer kernel.mu.Unlock()
			if kernel.minted != 1 {
				return false
			}
			d, ok := kernel.Domain(kernel.domain)
			return ok && d.State == lifecycle.DomainEstablished
		})

	att := runLine(t, ch, kernel, "printf 'PROOF_ZSH_123\\n'; sleep 0.3", 0)
	fence := fmt.Sprintf("\x1b]1337;NOCX_FENCE;%x\x07", att.Fence)
	waittest.WaitForTimeout(t, "zsh sentinel output and fence", 10*time.Second, func() bool {
		return strings.Contains(out.String(), "PROOF_ZSH_123") &&
			strings.Contains(out.String(), fence)
	})

	// A failing command completes with exit status 1 over the zsh hook.
	runLine(t, ch, kernel, "sh -c 'sleep 0.3; exit 1'", 1)

	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	// domain_closed is best-effort (see the bash proof); assert the session
	// ended, not a promised terminal state.
	waittest.WaitForTimeout(t, "session end after exit", 15*time.Second, func() bool {
		select {
		case <-ch.Done():
			return true
		default:
			return false
		}
	})
}

// ---------------------------------------------------------------------------
// The lifecycle channel's tunnel, for a journey whose subject is the FAR side

// liveSshdTunnelTransport is the lease the live-sshd journey's remote lifecycle
// channel rides: the coordinator's OWN pooled connection to the fixture, with
// the -R seam (Listen/Done/LostErr/Close) and nothing else.
//
// # Why this exists, and what it is standing in for
//
// It is a test double for a production path that no longer dials from this
// process: nocx-50w7p.8 moved every tunnel onto THIS MACHINE'S HELPER
// (internal/helper/tunnelchan), and the helper's own ssh service is what asks a
// real sshd for the tcpip-forward this journey is about. Driving THAT would mean
// installing, starting and trusting a helper daemon inside a unit test — which
// is what the containerized e2e suite does and what the tunnel package's own
// tests do against an in-process ssh server, with the whole helper path and its
// failure modes covered there.
//
// What this file's subject is, and what it therefore must not fake, is the FAR
// side: a real OpenSSH server, a real login shell, the real launcher and the
// real lifecycle kernel. The transport underneath is the one part of this
// journey that is not that server — so it is the one part that is allowed to be
// a stand, and it is built out of the same library, on the same pooled
// connection, with the same loss watcher the pool lease used to carry.
type liveSshdTunnelTransport struct {
	lease ssh.TunnelConn
}

func (t liveSshdTunnelTransport) TunnelConn(context.Context, string, ...ssh.ConnectOption) (ssh.TunnelConn, error) {
	return t.lease, nil
}

// liveSshdTunnelLease builds that lease over the fixture: one pooled reference
// on the connection the coordinator's own client holds, released by Close (or
// by the loss watcher, whichever fires first).
type liveSshdTunnelLease struct {
	pool      *ssh.PooledConn
	client    *gossh.Client
	done      chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	lostErr error
}

func (fx *liveSshd) tunnelLease(t *testing.T) *liveSshdTunnelLease {
	t.Helper()
	host, portStr, err := net.SplitHostPort(fx.addr)
	if err != nil {
		t.Fatalf("split %q: %v", fx.addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	pool, err := fx.client.AcquirePooled(context.Background(), ssh.PooledSpec{
		Host: host,
		Port: port,
		User: fx.user,
		// The identity component of the pool key: this lease's own, so it never
		// shares an entry with a session's connection here (the journey is
		// about one channel, and sharing would make "which reference closed
		// it" a question about this test).
		Identity: "live-sshd-lifecycle",
		Config: &gossh.ClientConfig{
			User:            fx.user,
			Auth:            []gossh.AuthMethod{gossh.PublicKeys(fx.signer)},
			HostKeyCallback: gossh.FixedHostKey(fx.hostKey),
		},
	})
	if err != nil {
		t.Fatalf("acquire the pooled connection for the lifecycle tunnel: %v", err)
	}
	l := &liveSshdTunnelLease{pool: pool, client: pool.Client(), done: make(chan struct{})}
	// One watcher, as the pool lease carried: gossh.Client.Wait returns when the
	// transport shuts down, which is the loss the lifecycle adapter reports.
	go func() {
		lost := l.client.Wait()
		l.mu.Lock()
		l.lostErr = lost
		l.mu.Unlock()
		close(l.done)
		l.closeOnce.Do(func() { _ = l.pool.Close() })
	}()
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func (l *liveSshdTunnelLease) Dial(addr string) (net.Conn, error) {
	return l.client.Dial("tcp", addr)
}

func (l *liveSshdTunnelLease) Listen(addr string) (net.Listener, error) {
	return l.client.Listen("tcp", addr)
}

func (l *liveSshdTunnelLease) Done() <-chan struct{} { return l.done }

func (l *liveSshdTunnelLease) LostErr() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lostErr
}

func (l *liveSshdTunnelLease) Close() error {
	l.closeOnce.Do(func() { _ = l.pool.Close() })
	return nil
}

// knownHostsPath writes a known_hosts file carrying the fixture's host key
// for the dial address and returns its path.
func (fx *liveSshd) knownHostsPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{fx.addr}, fx.hostKey)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// homeEntries lists the session home recursively, relative paths.
func homeEntries(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// assertSessionLeftOnlyTheLauncherBundle fails unless the session home holds
// nothing but the fixture's .bashrc and the launcher's own ~/.nocx bundle
// (nocx-k47n), and no file anywhere contains the per-epoch capability — the
// channel installs nothing and persists nothing beyond the rcfile text
// (ADR-0024 decision 2).
func assertSessionLeftOnlyTheLauncherBundle(t *testing.T, home, capability string) {
	t.Helper()
	for _, e := range homeEntries(home) {
		if e == ".bashrc" || e == ".nocx" || strings.HasPrefix(e, ".nocx/") {
			continue
		}
		t.Fatalf("session left an artifact in the remote home: %s", e)
	}
	found := ""
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		b, err := os.ReadFile(p) // #nosec G304 — p comes from WalkDir over the fixture-owned session home.
		if err == nil && strings.Contains(string(b), capability) {
			found = p
		}
		return nil
	})
	if found != "" {
		t.Fatalf("the per-epoch capability persisted on the remote host at %s — "+
			"the channel must install nothing and persist nothing", found)
	}
}

// stripControl removes ANSI/OSC escape sequences from terminal bytes,
// leaving the text a user could see. The policy-diagnostic check runs on
// this: the terminal legitimately carries structured markers (OSC 133
// lifecycle, OSC 636 command snapshots, OSC 7 cwd) whose payloads — command
// lists, function names — are telemetry, not diagnostics naming a policy.
func stripControl(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i+1] {
		case '[': // CSI ... final byte
			for j := i + 2; j < len(s); j++ {
				if s[j] >= 0x40 && s[j] <= 0x7e {
					i = j
					break
				}
			}
		case ']', 'P', '^', '_': // OSC / DCS / PM / APC ... BEL or ST
			for j := i + 2; j < len(s); j++ {
				if s[j] == 0x07 {
					i = j
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					i = j + 1
					break
				}
			}
		default: // plain two-byte escape; drop both
			i++
		}
	}
	return b.String()
}

// ackingEmitter used to acknowledge every published establishment
// immediately, as the renderer does after committing the editor presentation
// — required for the live-sshd proofs, which drive the real shell over the
// real sshd against the production composition, to ever see enhanced mode.
// ADR-0062 removed the wait the acknowledgement used to release: the accept
// is now flushed on the backend's own authority as soon as the kernel mints
// it, so this emitter has nothing left to do but exist as the Emitter the
// publisher requires.
type ackingEmitter struct{}

func (ackingEmitter) PublishLifecycle(lifecyclepub.Fact) {}

// newRecordingKernel builds the observation seam the way production wires
// it: publisher over the raw kernel, acking emitter bound, the publisher
func newRecordingKernel(opts ...lifecyclepub.Option) *recordingKernel {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k, opts...)
	pub.SetEmitter(ackingEmitter{})
	return &recordingKernel{Publisher: pub}
}

// capabilityHex is the bearer as the launch embedded it: the provider
// hex-encodes the handle's capability into the rcfile text (never the
// environment), so this is byte-for-byte the value the shell received.
func (r *recordingKernel) capabilityHex() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("%x", r.capability)
}

// recoveryHex is the one-shot recovery fence as the launch embedded it. It is
// the second bearer §11 assertion 7 names, and it is asserted against every
// surface alongside the capability — "neither bearer" is two statements.
func (r *recordingKernel) recoveryHex() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("%x", r.recovery)
}

// runLine types one command line into the remote shell and waits for its
// authenticated completion, returning the completed attempt.
func runLine(t *testing.T, ch ssh.Channel, kernel *recordingKernel, line string, wantExit int) lifecycle.ExecutionAttempt {
	t.Helper()
	kernel.mu.Lock()
	domain := kernel.domain
	kernel.mu.Unlock()
	if domain == "" {
		t.Fatal("runLine called before a domain was minted")
	}
	if _, err := ch.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write %q: %v", line, err)
	}
	// The command is deliberately slow enough to observe the open attempt:
	// the kernel mints the attempt id on start, and the test needs it to
	// follow the completion.
	var att lifecycle.ExecutionAttempt
	waittest.WaitForTimeout(t, "an open attempt for "+line, 15*time.Second, func() bool {
		a, ok := kernel.OpenAttempt(domain)
		if ok {
			att = a
		}
		return ok
	})
	waittest.WaitForTimeout(t, "completion of "+line, 15*time.Second, func() bool {
		a, ok := kernel.Attempt(att.ID)
		if !ok {
			return false
		}
		if a.State != lifecycle.AttemptCompleted {
			return false
		}
		if a.ExitCode == nil || *a.ExitCode != wantExit {
			t.Fatalf("attempt %s completed with exit %v, want %d", att.ID, a.ExitCode, wantExit)
		}
		att = a // the completed record: the fence it carries is the one on the wire
		return true
	})
	return att
}
