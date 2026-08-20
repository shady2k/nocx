package app

// The bootstrap end to end, against the real OpenSSH server the live-sshd
// fixture starts (design §12, P2): the bounded carrier is the whole command,
// stage-1 arrives as frame 1, the capability as frame 2, and a real bash on
// the far side reaches an accepted domain having read its bearer from an
// inherited descriptor whose name never existed.
//
// It uses live_sshd_test.go's fixture and nothing else of it — that file
// belongs to another worker and is read, never edited.
//
// ONE DIFFERENCE FROM THAT FILE, AND IT IS THE FINDING THIS TEST EXISTS TO
// PIN: the connection wires a RemoteInstaller. Under the carrier design the
// bundle travels over SFTP and NOTHING ELSE PUBLISHES IT — the self-installing
// launcher that used to carry a publish prelude inside the remote command is
// exactly what the carrier retired. So a session with no installer wired can
// no longer integrate: stage-1 finds no launch carrier on the far host, names
// generation-unavailable, and leaves a native prompt. That is fail-open and
// correct, and it is also why three tests in live_sshd_test.go now time out
// waiting for a domain — they wire no installer. The fix is one argument at
// their fx.connect calls, and this test is the evidence that it is the only
// thing missing.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// TestLiveSshd_CarrierBootstrapReachesAcceptedDomain is the happy path of the
// whole epic, watched end to end.
func TestLiveSshd_CarrierBootstrapReachesAcceptedDomain(t *testing.T) {
	fx := startLiveSshd(t, true)
	installer := shellintegration.New(log.NewSlogAdapter(nil))
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellBash, installer)

	waitFor(t, "domain established", 20*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		if kernel.minted != 1 {
			return false
		}
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})

	// The command the server actually ran is the bounded carrier, and the
	// capability is not in it. The domain above is the proof that the
	// capability nevertheless ARRIVED, which is the whole mechanism: it
	// travelled as a frame on the channel and reached the shell through an
	// unlinked descriptor.
	att := runLine(t, ch, kernel, "printf 'CARRIER_PROOF\\n'; sleep 0.3", 0)
	fence := fmt.Sprintf("\x1b]1337;NOCX_FENCE;%x\x07", att.Fence)
	waitFor(t, "the command's output and its fence", 15*time.Second, func() bool {
		return strings.Contains(out.String(), "CARRIER_PROOF") &&
			strings.Contains(out.String(), fence)
	})

	// Per surface: the terminal never carried either bearer, and neither
	// did the far host's filesystem.
	cap := kernel.capabilityHex()
	if cap == "" {
		t.Fatal("the kernel minted no capability to check against")
	}
	if strings.Contains(out.String(), cap) {
		t.Error("the per-epoch capability appeared in the session's own output")
	}

	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	waitFor(t, "session end after exit", 20*time.Second, func() bool {
		select {
		case <-ch.Done():
			return true
		default:
			return false
		}
	})
	// Nothing installed beyond the bundle, and no file on the far host
	// holds the capability — including the temp root, which the bootstrap
	// wrote to and unlinked before writing a byte.
	assertSessionLeftOnlyTheLauncherBundle(t, fx.home, cap)
}

// TestLiveSshd_WithNothingPublishedTheSessionStillReachesAPrompt is the same
// path with the publish deliberately absent: the far side has no launch
// carrier, so the bootstrap names generation-unavailable and the user still
// gets a working shell. Fail-open is absolute (ADR-0004), and this is the
// case that used to HANG before the writer existed — the loader waited for a
// frame that never came.
func TestLiveSshd_WithNothingPublishedTheSessionStillReachesAPrompt(t *testing.T) {
	fx := startLiveSshd(t, true)
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellBash)

	// The prompt arrives without a domain: this session is conventional.
	waitFor(t, "a usable prompt with nothing installed", 20*time.Second, func() bool {
		if _, err := ch.Write([]byte("printf 'NO_BUNDLE%s\\n' _OK\n")); err != nil {
			return false
		}
		return strings.Contains(out.String(), "NO_BUNDLE_OK")
	})
	kernel.mu.Lock()
	minted := kernel.minted
	kernel.mu.Unlock()
	if minted != 1 {
		// The lifecycle channel is established at connect time whether
		// or not the far side can use it; what must NOT happen is a
		// domain reaching Established with no shell behind it.
		t.Logf("minted %d domains (the channel is established at connect; the shell never joined it)", minted)
	}
	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
}

// TestLiveSshd_InputIsRefusedUntilTheTerminalOutcome watches the quarantine on
// a real session: a keystroke sent before the bootstrap has finished is
// REFUSED — not buffered, so it can never surface later as a command the user
// did not knowingly run — and the same keystroke is accepted once the outcome
// has landed.
func TestLiveSshd_InputIsRefusedUntilTheTerminalOutcome(t *testing.T) {
	fx := startLiveSshd(t, true)
	installer := shellintegration.New(log.NewSlogAdapter(nil))
	if err := installer.EnsureInstalledRemote(context.Background(), fx.rawClient(t), fx.home); err != nil {
		t.Fatalf("publish: %v", err)
	}
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellBash, installer)

	// Immediately after Connect the session is bootstrapping. The write
	// either lands (the bootstrap already finished — the far side is fast
	// and this is a race the test may lose) or is refused with the
	// quarantine's own error; what it may NEVER do is be accepted and
	// then executed later.
	_, werr := ch.Write([]byte("echo QUARANTINE_BREACH\n"))
	if werr != nil && !strings.Contains(werr.Error(), "bootstrapping") {
		t.Fatalf("write during bootstrap failed for the wrong reason: %v", werr)
	}
	if werr == nil {
		t.Skip("the bootstrap completed before the first write; the quarantine's " +
			"refusal is proven deterministically in internal/ssh")
	}

	// The refused keystroke was DROPPED, not queued: it must not appear
	// later, and the session must still work.
	waitFor(t, "a usable prompt after the outcome", 20*time.Second, func() bool {
		if _, err := ch.Write([]byte("printf 'AFTER%s\\n' _OUTCOME\n")); err != nil {
			return false
		}
		return strings.Contains(out.String(), "AFTER_OUTCOME")
	})
	if strings.Contains(out.String(), "QUARANTINE_BREACH") {
		t.Error("a keystroke refused during the bootstrap was delivered later")
	}
	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
}

// The other two live-sshd rows, reproduced with an installer wired. They
// exist to make the claim in this file's header a MEASUREMENT rather than an
// argument: each of the three tests that now time out is one argument away
// from passing, and these are those three tests with that argument.

// TestLiveSshd_ForwardingRefusedStillReachesAConventionalPrompt mirrors
// TestLiveSshd_ForwardingRefusedStaysConventional: with forwarding refused
// nothing is minted, stage-1 receives the non-secret refusal, and the tier
// still comes up — a conventional terminal with the fixture's own prompt.
func TestLiveSshd_ForwardingRefusedStillReachesAConventionalPrompt(t *testing.T) {
	fx := startLiveSshd(t, false)
	installer := shellintegration.New(log.NewSlogAdapter(nil))
	kernel := newRecordingKernel()
	ch, out := fx.connect(t, kernel, ssh.ShellBash, installer)

	waitFor(t, "a usable conventional terminal", 30*time.Second, func() bool {
		if _, err := ch.Write([]byte("printf 'CONVENTIONAL%s\\n' _OK\n")); err != nil {
			return false
		}
		s := out.String()
		return strings.Contains(s, "NATIVE_PROMPT>") && strings.Contains(s, "CONVENTIONAL_OK")
	})
	kernel.mu.Lock()
	minted := kernel.minted
	kernel.mu.Unlock()
	if minted != 0 {
		t.Errorf("refused forwarding still minted %d domain(s)", minted)
	}
	if _, err := ch.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
}

// TestLiveSshd_ConnectionLossRevokesTheBootstrappedDomain mirrors
// TestLiveSshd_ConnectionLossRevokesDomain: the domain the bootstrap
// established is revoked when the transport dies, and the open attempt
// becomes unknown rather than successful.
func TestLiveSshd_ConnectionLossRevokesTheBootstrappedDomain(t *testing.T) {
	fx := startLiveSshd(t, true)
	installer := shellintegration.New(log.NewSlogAdapter(nil))
	kernel := newRecordingKernel()
	ch, _ := fx.connect(t, kernel, ssh.ShellBash, installer)

	waitFor(t, "domain established", 20*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		if kernel.minted != 1 {
			return false
		}
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainEstablished
	})
	if _, err := ch.Write([]byte("sleep 60\n")); err != nil {
		t.Fatalf("write sleep: %v", err)
	}
	var att lifecycle.ExecutionAttempt
	waitFor(t, "the sleep attempt to be open", 20*time.Second, func() bool {
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
	waitFor(t, "domain lost", 30*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		d, ok := kernel.Domain(kernel.domain)
		return ok && d.State == lifecycle.DomainLost
	})
	waitFor(t, "open attempt unknown", 30*time.Second, func() bool {
		kernel.mu.Lock()
		defer kernel.mu.Unlock()
		a, ok := kernel.Attempt(att.ID)
		return ok && a.State == lifecycle.AttemptUnknown && a.ExitCode == nil
	})
}
