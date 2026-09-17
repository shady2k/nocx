package session

import (
	"context"
	"io"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/ssh"
)

// capturingSSHFactory records the ConnectOptions it receives and returns a
// scripted channel (or error).
type capturingSSHFactory struct {
	opts []ssh.ConnectOption
	ch   ssh.Channel
	err  error
}

// fakeLauncher satisfies ssh.RemoteLauncher; the session layer only needs to
// carry it through to Connect, never to call it.
type fakeLauncher struct{}

func (f *fakeLauncher) StartCommand(_ ssh.ShellKind, _ ssh.LaunchOptions) (string, ssh.RefusalReason, bool) {
	return "", ssh.ReasonNone, false
}

// A launcher that declines has no bootstrap to prepare either: the two are
// one delivery.
func (f *fakeLauncher) Prepare(_ ssh.ShellKind, _ ssh.LaunchOptions) (string, ssh.BootstrapRun, ssh.BootstrapGate, bool) {
	return "", nil, nil, false
}

func (f *capturingSSHFactory) Connect(_ context.Context, _ string, opts ...ssh.ConnectOption) (ssh.Channel, error) {
	f.opts = append(f.opts, opts...)
	if f.err != nil {
		return nil, f.err
	}
	return f.ch, nil
}

// reasonChannel is an ssh.Channel whose ShellIntegrationReason is scripted.
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

// fingerprintChannel is an ssh.Channel whose HostKeyFingerprint is
// scripted — the shape the real RealChannel carries after a capturing dial.
type fingerprintChannel struct {
	reasonChannel
	fingerprint string
}

func (c *fingerprintChannel) HostKeyFingerprint() string { return c.fingerprint }

func launcherReg() *Reg {
	return New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})
}

func TestRemoteSession_SurfacesShellIntegrationReason(t *testing.T) {
	factory := &capturingSSHFactory{ch: &reasonChannel{reason: ssh.ReasonRemoteCommand}}
	reg := launcherReg().WithSSHFactory(factory)

	sess, err := reg.Open(context.Background(), Config{
		Kind:   KindRemote,
		Host:   "example.com",
		Remote: &ssh.ConnectConfig{},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if got := sess.ShellIntegrationReason(); got != ssh.ReasonRemoteCommand {
		t.Errorf("ShellIntegrationReason = %q, want %q", got, ssh.ReasonRemoteCommand)
	}
}

// TestRemoteSession_SurfacesHostKeyFingerprint: the session passes through
// the fingerprint the dial captured — the consent key (consent design
// §3.2). A channel that does not expose one answers "", which never keys a
// consent answer.
func TestRemoteSession_SurfacesHostKeyFingerprint(t *testing.T) {
	factory := &capturingSSHFactory{ch: &fingerprintChannel{fingerprint: "SHA256:captured"}}
	reg := launcherReg().WithSSHFactory(factory)

	sess, err := reg.Open(context.Background(), Config{
		Kind:   KindRemote,
		Host:   "example.com",
		Remote: &ssh.ConnectConfig{},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

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
	factory := &capturingSSHFactory{ch: &reasonChannel{}}
	reg := launcherReg().WithSSHFactory(factory)

	sess, err := reg.Open(context.Background(), Config{
		Kind:   KindRemote,
		Host:   "example.com",
		Remote: &ssh.ConnectConfig{},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

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
// coordinator's own verified verdict and a channel's own answer (a direct,
// non-helper dial) is the fallback for when nothing was ever recorded.
func TestRecordHostKeyFingerprint_RecordedValueOutranksTheChannels(t *testing.T) {
	factory := &capturingSSHFactory{ch: &fingerprintChannel{fingerprint: "SHA256:fromchannel"}}
	reg := launcherReg().WithSSHFactory(factory)

	sess, err := reg.Open(context.Background(), Config{
		Kind:   KindRemote,
		Host:   "example.com",
		Remote: &ssh.ConnectConfig{},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

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
	factory := &capturingSSHFactory{ch: &reasonChannel{}}
	reg := launcherReg().WithSSHFactory(factory)

	sess, err := reg.Open(context.Background(), Config{
		Kind:   KindRemote,
		Host:   "example.com",
		Remote: &ssh.ConnectConfig{},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

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

func TestRemoteSession_SessionIDMatchesAndLauncherWired(t *testing.T) {
	launcher := &fakeLauncher{}
	factory := &capturingSSHFactory{ch: &reasonChannel{reason: ssh.ReasonNone}}
	reg := launcherReg().WithSSHFactory(factory)

	sess, err := reg.Open(context.Background(), Config{
		Kind:     KindRemote,
		Host:     "example.com",
		Enhanced: true,
		Remote:   &ssh.ConnectConfig{RemoteLauncher: launcher},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	// The ID the session was registered under is the ID the launcher options
	// carried — the same pre-connect ID, not an empty string.
	cfg := &ssh.ConnectConfig{}
	for _, o := range factory.opts {
		o(cfg)
	}
	if cfg.SessionID != string(sess.ID()) {
		t.Errorf("ConnectConfig.SessionID = %q, want the session ID %q", cfg.SessionID, sess.ID())
	}
	if cfg.SessionID == "" {
		t.Error("ConnectConfig.SessionID is empty: the launcher would embed no NOCX_SESSION_ID")
	}
	if !cfg.Enhanced {
		t.Error("ConnectConfig.Enhanced = false, want true (Config.Enhanced requested marker-only mode)")
	}
	if cfg.RemoteLauncher == nil {
		t.Error("ConnectConfig.RemoteLauncher is nil: the wired launcher did not reach Connect")
	}
}

// TestRemoteSession_ShellPin_ReachesConnect pins the regression the whole
// seam exists for (nocx-pu4.1): a ConnectConfig.Shell must ride
// sshOptionsFromConfig into the Connect options, or the pin dies before the
// launcher and the far shell is detected instead of named. The launcher's
// own mapping (empty → ShellAuto) is the ssh package's contract
// (ssh_real.go), tested there; here the pin must simply arrive.
func TestRemoteSession_ShellPin_ReachesConnect(t *testing.T) {
	factory := &capturingSSHFactory{ch: &reasonChannel{reason: ssh.ReasonNone}}
	reg := launcherReg().WithSSHFactory(factory)

	sess, err := reg.Open(context.Background(), Config{
		Kind:   KindRemote,
		Host:   "example.com",
		Remote: &ssh.ConnectConfig{Shell: ssh.ShellZsh},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	cfg := &ssh.ConnectConfig{}
	for _, o := range factory.opts {
		o(cfg)
	}
	if cfg.Shell != ssh.ShellZsh {
		t.Errorf("ConnectConfig.Shell = %q, want the pinned %q", cfg.Shell, ssh.ShellZsh)
	}
}

// TestRemoteSession_NoShellPin_LeavesShellEmpty pins the other end of the
// contract: an unpinned config must NOT invent a shell — the launcher's
// empty → ShellAuto mapping is the detect default (nocx-6rj0).
func TestRemoteSession_NoShellPin_LeavesShellEmpty(t *testing.T) {
	factory := &capturingSSHFactory{ch: &reasonChannel{reason: ssh.ReasonNone}}
	reg := launcherReg().WithSSHFactory(factory)

	sess, err := reg.Open(context.Background(), Config{
		Kind:   KindRemote,
		Host:   "example.com",
		Remote: &ssh.ConnectConfig{},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	cfg := &ssh.ConnectConfig{}
	for _, o := range factory.opts {
		o(cfg)
	}
	if cfg.Shell != "" {
		t.Errorf("ConnectConfig.Shell = %q, want empty (detect default)", cfg.Shell)
	}
}

func TestRemoteSession_ConnectError_NoSessionRegistered(t *testing.T) {
	factory := &capturingSSHFactory{err: io.ErrClosedPipe}
	reg := launcherReg().WithSSHFactory(factory)

	_, err := reg.Open(context.Background(), Config{
		Kind:   KindRemote,
		Host:   "example.com",
		Remote: &ssh.ConnectConfig{},
	})
	if err == nil {
		t.Fatal("Open with failing SSH factory: expected error, got nil")
	}
	if got := len(reg.List()); got != 0 {
		t.Errorf("failed connect registered %d session(s), want 0", got)
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

// TestRemoteSession_JumpConfig_ReachesConnect pins the regression the whole
// seam exists for (nocx-8b1v): a ConnectConfig.JumpConfig must ride
// sshOptionsFromConfig into the Connect options, or the bastion's auth
// material is dropped and the dial offers no methods. The vault unlock no
// longer rides this path — a sealed vault is a sealed-vault failure the
// transport's dispatcher seam normalizes into the canonical error, and the
// renderer raises the prompt (ADR-0032).
func TestRemoteSession_JumpConfig_ReachesConnect(t *testing.T) {
	factory := &capturingSSHFactory{ch: &reasonChannel{reason: ssh.ReasonNone}}
	reg := launcherReg().WithSSHFactory(factory)

	jumpCfg := &ssh.ConnectConfig{
		User:     "jumpuser",
		Port:     2222,
		AuthMode: "publicKey",
		KeyFile:  "/home/user/.ssh/jump_key",
	}

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindRemote,
		Host: "example.com",
		Remote: &ssh.ConnectConfig{
			JumpHost:   "bastion.example.com",
			JumpConfig: jumpCfg,
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	cfg := &ssh.ConnectConfig{}
	for _, o := range factory.opts {
		o(cfg)
	}
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
