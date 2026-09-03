package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/bootstrapprogress"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// The local half of nocx-dvql. session.go said in as many words that local
// sessions always return ReasonNone, so the wire the product renders was laid
// and never connected on this side: a degraded local session was
// indistinguishable from an integrated one, in the UI and in the log.
//
// These tests are about the FACTORY, because it is the only thing that knows
// which binary was exec'd. The transport's half — turning these reports into
// the notification — is proven over the real socket in internal/transport.

type recordedIntegration struct {
	sid, shell, status string
	reason             ssh.RefusalReason
}

type integrationRecorder struct {
	mu   sync.Mutex
	seen []recordedIntegration
}

func (r *integrationRecorder) report(sid, shell, status string, reason ssh.RefusalReason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, recordedIntegration{sid, shell, status, reason})
}

func (r *integrationRecorder) all() []recordedIntegration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedIntegration(nil), r.seen...)
}

func newIntegrationKernel() *lifecyclepub.Publisher {
	return lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
}

// An enhanced local session enters the axis as `starting`, naming the binary
// it actually started. `starting` and not `integrated`: the shell has proved
// nothing yet, and a product that claimed either outcome here would be
// guessing for the ten seconds that matter most.
func TestLocalEnhancedSessionReportsWhatItStarted(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	rec := &integrationRecorder{}
	bash := requireShellBinary(t, "bash")
	ptf := &localPTYFactory{
		log:               logger,
		shint:             shellintegration.New(logger),
		kernel:            newIntegrationKernel(),
		reportIntegration: rec.report,
		shells:            fixedShell{path: bash},
	}
	p, err := ptf.NewPTY(context.Background(), pty.Config{
		SessionID: "0123456789abcdef0123456789abcdef",
		Enhanced:  true, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("NewPTY: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	seen := rec.all()
	if len(seen) != 1 {
		t.Fatalf("reports = %+v, want exactly one", seen)
	}
	got := seen[0]
	if got.status != transport.IntegrationStarting || got.reason != ssh.ReasonNone {
		t.Errorf("report = %+v, want status=starting with no reason", got)
	}
	if got.sid != "0123456789abcdef0123456789abcdef" {
		t.Errorf("sessionId = %q, want the id the registry minted", got.sid)
	}
	// The shell is the one fact a user cannot infer and cannot act without:
	// which shell a session runs is the single biggest thing that varies
	// between two machines running the same code. Asserted against the
	// INJECTED answer, not against a name: since nocx-wwz0 the factory starts
	// whatever the resolver returns, so a test that hard-coded "bash" would
	// pass or fail on what the developer's own account record happens to say.
	// `bash` is that injected answer and NOT what this machine would resolve
	// on its own, so the comparison still fails for a factory that reports a
	// name, a basename, the account's own shell or nothing (requireShellBinary).
	if !filepath.IsAbs(got.shell) || got.shell != bash {
		t.Errorf("shell = %q, want %q — the absolute path the resolver named", got.shell, bash)
	}
}

// A session that never asked for integration says nothing at all. Absence is
// how "conventional by design" is expressed — the surface has nothing to nag
// about, and a badge on a tab the user deliberately opened raw would be noise
// that teaches them to ignore the badge.
func TestLocalConventionalSessionReportsNothing(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	rec := &integrationRecorder{}
	ptf := &localPTYFactory{
		log:               logger,
		shint:             shellintegration.New(logger),
		kernel:            newIntegrationKernel(),
		reportIntegration: rec.report,
	}
	p, err := ptf.NewPTY(context.Background(), pty.Config{
		SessionID: "0123456789abcdef0123456789abcdef",
		Cols:      80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("NewPTY: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	if seen := rec.all(); len(seen) != 0 {
		t.Errorf("reports = %+v, want none for a session that requested no integration", seen)
	}
}

// The two packages spell the loss causes independently — the adapter owns the
// vocabulary, the transport matches on strings so it does not depend on the
// adapter package, and the composition root is the only thing that sees both.
// A rename on either side would otherwise silently stop mapping a handshake
// timeout to its reason, and the symptom would be the same silence the bead
// exists to end.
func TestLossCauseSpellingsAgree(t *testing.T) {
	if string(lifecyclechannel.LossHelloTimeout) != transport.LossCauseHelloTimeout {
		t.Errorf("hello-timeout spelled %q by the adapter and %q by the transport",
			lifecyclechannel.LossHelloTimeout, transport.LossCauseHelloTimeout)
	}
	if string(lifecyclechannel.LossClosed) != transport.LossCauseClosed {
		t.Errorf("closed spelled %q by the adapter and %q by the transport",
			lifecyclechannel.LossClosed, transport.LossCauseClosed)
	}
	// The two causes the transport does NOT name must not accidentally
	// collide with the two it does, or a broken descriptor would be
	// reported as a handshake that expired.
	for _, c := range []lifecyclechannel.LossCause{lifecyclechannel.LossEndOfStream, lifecyclechannel.LossReadError} {
		if string(c) == transport.LossCauseHelloTimeout || string(c) == transport.LossCauseClosed {
			t.Errorf("loss cause %q collides with a cause the transport treats specially", c)
		}
		if strings.TrimSpace(string(c)) == "" {
			t.Errorf("loss cause is empty: an unnamed path is the defect this bead removed")
		}
	}
}

// The bootstrap stages are spelled independently for the same reason the loss
// causes are: the reader owns the vocabulary, the transport matches on strings
// so it does not depend on the reader's package, and only the composition root
// sees both. A rename on either side would leave the transport matching a
// stage nothing ever sends — and the symptom would be the return of the
// timeout that says nothing, which is the whole defect (nocx-yww2).
func TestBootstrapStageSpellingsAgree(t *testing.T) {
	if string(bootstrapprogress.StageStartupEntered) != transport.BootstrapStageStartupEntered {
		t.Errorf("startup-entered spelled %q by the reader and %q by the transport",
			bootstrapprogress.StageStartupEntered, transport.BootstrapStageStartupEntered)
	}
	if string(bootstrapprogress.StageUserRCReturned) != transport.BootstrapStageUserRCReturned {
		t.Errorf("user-rc-returned spelled %q by the reader and %q by the transport",
			bootstrapprogress.StageUserRCReturned, transport.BootstrapStageUserRCReturned)
	}
	// The two stages must stay distinct, or "the startup returned" and "the
	// startup began" would be one fact and the diagnosis would be a coin toss.
	if bootstrapprogress.StageStartupEntered == bootstrapprogress.StageUserRCReturned {
		t.Error("the two bootstrap stages are spelled the same")
	}
}
