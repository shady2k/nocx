package session

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/ssh"
)

// fakeLauncher satisfies ssh.RemoteLauncher. The registry carries it into the
// session's SSH options and never calls it: the command is built where the
// shell is spawned (the helper), from the destination it was handed.
type fakeLauncher struct{}

func (f *fakeLauncher) StartCommand(_ ssh.ShellKind, _ ssh.LaunchOptions) (string, ssh.RefusalReason, bool) {
	return "", ssh.ReasonNone, false
}

// A launcher that declines has no bootstrap to prepare either: the two are
// one delivery.
func (f *fakeLauncher) Prepare(_ ssh.ShellKind, _ ssh.LaunchOptions) (string, ssh.BootstrapRun, ssh.BootstrapGate, bool) {
	return "", nil, nil, false
}

// reasonChannel is a session.Channel whose ShellIntegrationReason is scripted.
// It stands in for the helper's attached channel, which is the only channel a
// remote session has now.
type reasonChannel struct {
	reason ssh.RefusalReason
}

func (c *reasonChannel) Read(p []byte) (int, error) { return 0, io.EOF }
func (c *reasonChannel) Write(p []byte) (int, error) {
	return len(p), nil
}
func (c *reasonChannel) Close() error { return nil }
func (c *reasonChannel) Done() <-chan struct{} {
	return make(chan struct{})
}
func (c *reasonChannel) Resize(_ context.Context, _, _, _, _ uint16) error { return nil }
func (c *reasonChannel) ShellIntegrationReason() ssh.RefusalReason {
	return c.reason
}

// fingerprintChannel is a channel whose HostKeyFingerprint is scripted — the
// shape a coordinator-dialed channel used to carry after a capturing dial. It
// survives as a fake so the session's fallback order can be asserted.
type fingerprintChannel struct {
	reasonChannel
	fingerprint string
}

func (c *fingerprintChannel) HostKeyFingerprint() string { return c.fingerprint }

func launcherReg() *Reg {
	return New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})
}

// adoptRemote registers a remote session the way this machine's helper does:
// the helper mints the id and creates the channel, and the registry adopts
// both (ADR-7).
func adoptRemote(t *testing.T, reg *Reg, ch Channel, cfg Config) Session {
	t.Helper()
	cfg.Kind = KindRemote
	sess, err := reg.Adopt(context.Background(), cfg, NewID(), ch)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(sess.ID()) })
	return sess
}

// sshOptionsOf rebuilds the ConnectConfig a session's options describe — the
// same translation a helper open feeds the destination with.
func sshOptionsOf(sess Session) *ssh.ConnectConfig {
	cfg := &ssh.ConnectConfig{}
	for _, o := range sess.SSHOptions() {
		o(cfg)
	}
	return cfg
}

// TestOpen_RefusesARemoteSession is the structural boundary left by
// nocx-oalo1.3.2: an ssh destination is opened by this machine's helper, so
// the registry has no dial of its own to reach. The refusal NAMES the route
// (a caller told only "unavailable" would try again the same way), and a
// refused open registers nothing.
func TestOpen_RefusesARemoteSession(t *testing.T) {
	reg := launcherReg()

	_, err := reg.Open(context.Background(), Config{
		Kind:   KindRemote,
		Host:   "example.com",
		Remote: &ssh.ConnectConfig{},
	})
	if err == nil {
		t.Fatal("Open opened an ssh session; the registry must have no dial of its own")
	}
	if !strings.Contains(err.Error(), "helper") {
		t.Fatalf("refusal = %q, want it to name the helper route", err)
	}
	if got := len(reg.List()); got != 0 {
		t.Errorf("a refused open registered %d session(s), want 0", got)
	}
}

func TestRemoteSession_SurfacesShellIntegrationReason(t *testing.T) {
	reg := launcherReg()
	sess := adoptRemote(t, reg, &reasonChannel{reason: ssh.ReasonRemoteCommand}, Config{Host: "example.com"})

	if got := sess.ShellIntegrationReason(); got != ssh.ReasonRemoteCommand {
		t.Errorf("ShellIntegrationReason = %q, want %q", got, ssh.ReasonRemoteCommand)
	}
}

// TestRemoteSession_SurfacesHostKeyFingerprint: the session passes through
// the fingerprint the dial captured — the consent key (consent design
// §3.2). A channel that does not expose one answers "", which never keys a
// consent answer.
func TestRemoteSession_SurfacesHostKeyFingerprint(t *testing.T) {
	reg := launcherReg()
	sess := adoptRemote(t, reg, &fingerprintChannel{fingerprint: "SHA256:captured"}, Config{Host: "example.com"})

	if got := sess.HostKeyFingerprint(); got != "SHA256:captured" {
		t.Errorf("HostKeyFingerprint = %q, want %q", got, "SHA256:captured")
	}
}

func TestLocalSession_HasNoHostKeyFingerprint(t *testing.T) {
	reg := launcherReg()
	sess, err := reg.Open(context.Background(), Config{Kind: KindLocal, Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if got := sess.HostKeyFingerprint(); got != "" {
		t.Errorf("HostKeyFingerprint on a local session = %q, want \"\"", got)
	}
}

// TestRecordHostKeyFingerprint_AnswersOverAChannelThatCarriesNone is
// nocx-y6fh7 items 5 and 6's shared foundation: a helper-hosted ssh session's
// own channel (AttachedSession in production; reasonChannel here, which
// carries no HostKeyFingerprint method at all) has nothing of its own to
// answer with — the coordinator captured the fact somewhere else entirely
// (its own verifyHostKey reverse handler) and must be able to hand it to the
// session after the fact, the same way RecordOwnedProcessPID already lets a
// launch fact arrive once the session already exists.
func TestRecordHostKeyFingerprint_AnswersOverAChannelThatCarriesNone(t *testing.T) {
	reg := launcherReg()
	sess := adoptRemote(t, reg, &reasonChannel{}, Config{Host: "example.com"})

	if got := sess.HostKeyFingerprint(); got != "" {
		t.Fatalf("before recording, HostKeyFingerprint = %q, want \"\"", got)
	}
	if err := reg.RecordHostKeyFingerprint(sess.ID(), "SHA256:recorded"); err != nil {
		t.Fatalf("RecordHostKeyFingerprint: %v", err)
	}
	if got := sess.HostKeyFingerprint(); got != "SHA256:recorded" {
		t.Fatalf("after recording, HostKeyFingerprint = %q, want %q", got, "SHA256:recorded")
	}
}

// TestRecordHostKeyFingerprint_RecordedValueOutranksTheChannels proves the
// order HostKeyFingerprint checks: a RECORDED fact wins even when the
// channel itself could also answer, because the recorded fact is the
// coordinator's own verified verdict and a channel's own answer is the
// fallback for when nothing was ever recorded.
func TestRecordHostKeyFingerprint_RecordedValueOutranksTheChannels(t *testing.T) {
	reg := launcherReg()
	sess := adoptRemote(t, reg, &fingerprintChannel{fingerprint: "SHA256:fromchannel"}, Config{Host: "example.com"})

	if err := reg.RecordHostKeyFingerprint(sess.ID(), "SHA256:recorded"); err != nil {
		t.Fatalf("RecordHostKeyFingerprint: %v", err)
	}
	if got := sess.HostKeyFingerprint(); got != "SHA256:recorded" {
		t.Fatalf("HostKeyFingerprint = %q, want the recorded value %q, not the channel's %q",
			got, "SHA256:recorded", "SHA256:fromchannel")
	}
}

// TestRecordHostKeyFingerprint_RefusesADifferentValueForTheSameSession is
// set-once, on the same terms RecordOwnedProcessPID already states: a
// session that somehow saw two different verdicts is a defect to surface,
// never a silent overwrite of evidence a consent decision may already be
// keyed by.
func TestRecordHostKeyFingerprint_RefusesADifferentValueForTheSameSession(t *testing.T) {
	reg := launcherReg()
	sess := adoptRemote(t, reg, &reasonChannel{}, Config{Host: "example.com"})

	if err := reg.RecordHostKeyFingerprint(sess.ID(), "SHA256:first"); err != nil {
		t.Fatalf("first RecordHostKeyFingerprint: %v", err)
	}
	if err := reg.RecordHostKeyFingerprint(sess.ID(), "SHA256:second"); err == nil {
		t.Fatal("a second, different fingerprint was accepted silently; want a refusal")
	}
	if got := sess.HostKeyFingerprint(); got != "SHA256:first" {
		t.Fatalf("HostKeyFingerprint after the refused second write = %q, want the first value %q", got, "SHA256:first")
	}
}

// TestRecordHostKeyFingerprint_RefusesAnUnknownSession names the failure
// rather than panicking on a lookup that missed, the same shape
// RecordOwnedProcessPID's own "session not found" answer has.
func TestRecordHostKeyFingerprint_RefusesAnUnknownSession(t *testing.T) {
	reg := launcherReg()
	if err := reg.RecordHostKeyFingerprint(ID("no-such-session"), "SHA256:x"); err == nil {
		t.Fatal("RecordHostKeyFingerprint on an unknown session id was accepted; want a refusal")
	}
}

// TestAdoptedRemoteSession_CarriesTheLauncherAndTheMode: the helper is the
// party that builds a shell command now, so what the registry owes it is the
// destination's resolved material — launcher, mode, shell pin — reachable
// through the session's options for whoever asks (a probe, a forward, a
// readopt).
func TestAdoptedRemoteSession_CarriesTheLauncherAndTheMode(t *testing.T) {
	launcher := &fakeLauncher{}
	reg := launcherReg()
	sess := adoptRemote(t, reg, &reasonChannel{reason: ssh.ReasonNone}, Config{
		Host: "example.com",
		Remote: &ssh.ConnectConfig{
			RemoteLauncher: launcher,
			DesiredMode:    "raw",
			Shell:          ssh.ShellZsh,
		},
	})

	cfg := sshOptionsOf(sess)
	if cfg.RemoteLauncher == nil {
		t.Error("ConnectConfig.RemoteLauncher is nil: the wired launcher did not reach the session's options")
	}
	if cfg.DesiredMode != "raw" {
		t.Errorf("ConnectConfig.DesiredMode = %q, want raw", cfg.DesiredMode)
	}
	if cfg.Shell != ssh.ShellZsh {
		t.Errorf("ConnectConfig.Shell = %q, want the pinned %q", cfg.Shell, ssh.ShellZsh)
	}
}

// TestRemoteSession_NoShellPin_LeavesShellEmpty pins the other end of the
// contract: an unpinned config must NOT invent a shell — the launcher's
// empty → ShellAuto mapping is the detect default (nocx-6rj0).
func TestRemoteSession_NoShellPin_LeavesShellEmpty(t *testing.T) {
	reg := launcherReg()
	sess := adoptRemote(t, reg, &reasonChannel{reason: ssh.ReasonNone}, Config{Host: "example.com"})

	if cfg := sshOptionsOf(sess); cfg.Shell != "" {
		t.Errorf("ConnectConfig.Shell = %q, want empty (detect default)", cfg.Shell)
	}
}

func TestLocalSession_ShellIntegrationReasonIsNone(t *testing.T) {
	reg := launcherReg()

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindLocal,
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if got := sess.ShellIntegrationReason(); got != ssh.ReasonNone {
		t.Errorf("local session ShellIntegrationReason = %q, want %q", got, ssh.ReasonNone)
	}
}

// TestRemoteSession_JumpConfig_ReachesTheSessionOptions pins the regression
// the seam exists for (nocx-8b1v): a ConnectConfig.JumpConfig must ride
// sshOptionsFromConfig into the session's options, or the bastion's auth
// material is dropped and the helper's dial offers no methods.
func TestRemoteSession_JumpConfig_ReachesTheSessionOptions(t *testing.T) {
	jumpCfg := &ssh.ConnectConfig{
		User:     "jumpuser",
		Port:     2222,
		AuthMode: "publicKey",
		KeyFile:  "/home/user/.ssh/jump_key",
	}
	reg := launcherReg()
	sess := adoptRemote(t, reg, &reasonChannel{reason: ssh.ReasonNone}, Config{
		Host: "example.com",
		Remote: &ssh.ConnectConfig{
			JumpHost:   "bastion.example.com",
			JumpConfig: jumpCfg,
		},
	})

	cfg := sshOptionsOf(sess)
	if cfg.JumpConfig == nil {
		t.Fatal("ConnectConfig.JumpConfig is nil: the recursive jump config was dropped at the session→ssh seam")
	}
	if cfg.JumpConfig.User != "jumpuser" {
		t.Errorf("JumpConfig.User = %q, want jumpuser", cfg.JumpConfig.User)
	}
	if cfg.JumpConfig.KeyFile != "/home/user/.ssh/jump_key" {
		t.Errorf("JumpConfig.KeyFile = %q, want the jump key path", cfg.JumpConfig.KeyFile)
	}
}
