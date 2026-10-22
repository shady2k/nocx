package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// fakeSI Launcher is a scripted shellintegration.RemoteLauncher for adapter
// tests: it returns a canned (cmd, reason, ok) triple and records the call.
type fakeSILauncher struct {
	cmd    string
	reason shellintegration.RefusalReason
	ok     bool

	gotShell shellintegration.ShellKind
	gotOpts  shellintegration.LaunchOptions
}

func (f *fakeSILauncher) StartCommand(shell shellintegration.ShellKind, opts shellintegration.LaunchOptions) (string, shellintegration.RefusalReason, bool) {
	f.gotShell = shell
	f.gotOpts = opts
	return f.cmd, f.reason, f.ok
}

// captureAdapterLogs returns a logger that records into a buffer, so a test
// can assert the loud log that replaced the panic (nocx-axpz).
func captureAdapterLogs(t *testing.T) (log.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return log.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, nil))), &buf
}

func TestRemoteLauncherAdapter_Accepted_TranslatesAndForwards(t *testing.T) {
	inner := &fakeSILauncher{cmd: "exec bash --rcfile <(printf %b 'x') -i", reason: shellintegration.ReasonNone, ok: true}
	a := &remoteLauncherAdapter{inner: inner, logger: log.NewSlogAdapter(nil)}

	cmd, reason, ok := a.StartCommand(ssh.ShellBash, ssh.LaunchOptions{SessionID: "sess-1", Enhanced: true})
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if cmd != inner.cmd {
		t.Errorf("cmd = %q, want %q", cmd, inner.cmd)
	}
	if reason != ssh.ReasonNone {
		t.Errorf("reason = %q, want %q", reason, ssh.ReasonNone)
	}
	// The adapter must translate the type pair, not the values: the inner
	// launcher sees its own ShellKind and LaunchOptions.
	if inner.gotShell != shellintegration.ShellBash {
		t.Errorf("inner got shell %q, want %q", inner.gotShell, shellintegration.ShellBash)
	}
	if inner.gotOpts.SessionID != "sess-1" || !inner.gotOpts.Enhanced {
		t.Errorf("inner got opts %+v, want SessionID=sess-1 Enhanced=true", inner.gotOpts)
	}
}

func TestRemoteLauncherAdapter_Declines_MapEveryReasonExplicitly(t *testing.T) {
	cases := []struct {
		name   string
		inner  shellintegration.RefusalReason
		expect ssh.RefusalReason
	}{
		{"empty reason maps to none", shellintegration.ReasonNone, ssh.ReasonNone},
		{"unsupported shell maps", shellintegration.ReasonUnsupportedShell, ssh.ReasonUnsupportedShell},
		{"unsupported Fish startup maps", shellintegration.ReasonUnsupportedFishStartup, ssh.ReasonUnsupportedFishStartup},
		{"no secure temp maps", shellintegration.ReasonNoSecureTemp, ssh.ReasonNoSecureTemp},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &remoteLauncherAdapter{inner: &fakeSILauncher{reason: tc.inner, ok: false}, logger: log.NewSlogAdapter(nil)}
			cmd, reason, ok := a.StartCommand(ssh.ShellBash, ssh.LaunchOptions{})
			if ok {
				t.Fatalf("ok = true, want false")
			}
			if cmd != "" {
				t.Errorf("cmd = %q, want empty on decline", cmd)
			}
			if reason != tc.expect {
				t.Errorf("reason = %q, want %q", reason, tc.expect)
			}
		})
	}
}

// A launcher that accepts while claiming a refusal contradicts the pinned
// StartCommand contract (ok=true means the shell was integrated). The old
// response was a panic; ADR-0004:60 forbids taking the session down, so the
// adapter declines instead — the claimed reason reaches the product (it must
// never be dropped, which is exactly what ok=true would do in the ssh layer)
// — and shouts the contradiction into the log.
func TestRemoteLauncherAdapter_AcceptedWithReason_DeclinesWithClaimedReason(t *testing.T) {
	logger, buf := captureAdapterLogs(t)
	a := &remoteLauncherAdapter{inner: &fakeSILauncher{
		cmd: "exec bash -i", reason: shellintegration.ReasonUnsupportedShell, ok: true,
	}, logger: logger}

	cmd, reason, ok := a.StartCommand(ssh.ShellBash, ssh.LaunchOptions{SessionID: "sess-1"})
	if ok {
		t.Fatal("ok = true, want false (a contradicting launcher must decline, not run with a dropped reason)")
	}
	if cmd != "" {
		t.Errorf("cmd = %q, want empty on decline", cmd)
	}
	if reason != ssh.ReasonUnsupportedShell {
		t.Errorf("reason = %q, want the launcher's claimed %q", reason, ssh.ReasonUnsupportedShell)
	}
	if !strings.Contains(buf.String(), "accepted while naming a refusal reason") {
		t.Errorf("expected a loud log naming the contradiction, got:\n%s", buf.String())
	}
}

// An unmapped reason is the tripwire for a reason added to one package and
// forgotten in the other. The old response was a panic; ADR-0004:60 forbids
// taking the session down, so it degrades to the distinct ssh.ReasonUnknown —
// "integration did not happen, and I cannot say why" — never the ReasonNone
// that renders as "integration succeeded" — and shouts into the log.
func TestRemoteLauncherAdapter_UnmappedReason_DeclinesWithUnknown(t *testing.T) {
	logger, buf := captureAdapterLogs(t)
	a := &remoteLauncherAdapter{inner: &fakeSILauncher{
		reason: shellintegration.RefusalReason("brand-new-reason"), ok: false,
	}, logger: logger}

	cmd, reason, ok := a.StartCommand(ssh.ShellBash, ssh.LaunchOptions{})
	if ok {
		t.Fatal("ok = true, want false")
	}
	if cmd != "" {
		t.Errorf("cmd = %q, want empty on decline", cmd)
	}
	if reason != ssh.ReasonUnknown {
		t.Errorf("reason = %q, want distinct %q, not ReasonNone", reason, ssh.ReasonUnknown)
	}
	if !strings.Contains(buf.String(), "unmapped refusal reason") {
		t.Errorf("expected a loud log naming the unmapped value, got:\n%s", buf.String())
	}
}

// The accept-with-reason contradiction with an unmapped claimed reason:
// both safeguards compose — decline, unknown on the product, loud log.
func TestRemoteLauncherAdapter_AcceptedWithUnmappedReason_DeclinesWithUnknown(t *testing.T) {
	logger, buf := captureAdapterLogs(t)
	a := &remoteLauncherAdapter{inner: &fakeSILauncher{
		cmd: "exec bash -i", reason: shellintegration.RefusalReason("brand-new-reason"), ok: true,
	}, logger: logger}

	_, reason, ok := a.StartCommand(ssh.ShellBash, ssh.LaunchOptions{})
	if ok {
		t.Fatal("ok = true, want false")
	}
	if reason != ssh.ReasonUnknown {
		t.Errorf("reason = %q, want %q", reason, ssh.ReasonUnknown)
	}
	if !strings.Contains(buf.String(), "unmapped refusal reason") {
		t.Errorf("expected a loud log naming the unmapped value, got:\n%s", buf.String())
	}
}
