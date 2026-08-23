package transport

import (
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/procwatch"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// The acceptance check for nocx-cgzc and nocx-viil.3, off the real socket
// (AGENTS.md testing rule 5) and with the production wiring: a real
// lifecycle channel over a real socketpair, a real process observer watching
// a real child, a real server, and the frame the renderer would receive
// validated against the contract.
//
// The handshake bound is set to an hour ON PURPOSE. It is the provenance
// proof and it needs no clock: the bound cannot possibly have produced this
// notification, so the only thing that can have is the observation — which
// is the bead's whole claim, that the product no longer has to wait ten
// seconds to learn something it can already see. detail.observedProcess is
// the second half of the same proof: the expiry path has never carried one.
//
// What a user gets that they could not before: the card that explains a
// hijacked shell appears while they are still looking at the first prompt,
// and it names what took the shell over.
func TestSessionIntegrationChanged_ObservedProcessOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.integrationChanged.schema.json")
	logger := log.NewSlogAdapter(nil)
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	e := newLifecycleTestEnv(t, WithLifecyclePublisher(pub))
	pub.SetEmitter(e.ws)
	sid := e.openSession(t, 1)

	ch, child, err := lifecyclechannel.New(logger, pub,
		lifecyclechannel.WithHelloTimeout(time.Hour),
		lifecyclechannel.WithLossReporter(func(lane lifecycle.LaneID, cause lifecyclechannel.LossCause) {
			e.ws.NoteIntegrationLoss(lane, string(cause))
		}))
	if err != nil {
		t.Fatalf("lifecyclechannel.New: %v", err)
	}
	t.Cleanup(func() { _ = child.Close() })
	e.ws.RegisterLifecycleLane(ch.Lane(), session.ID(sid))
	e.ws.RegisterIntegration(session.ID(sid), "/bin/zsh", IntegrationStarting, ssh.ReasonNone)
	e.ws.emitIntegration(session.ID(sid))

	raw := readNotification(t, e.conn, "session.integrationChanged", wantWithin)
	var starting integrationChangedParams
	if derr := json.Unmarshal(raw, &starting); derr != nil {
		t.Fatalf("decode: %v", derr)
	}
	if starting.Status != IntegrationStarting {
		t.Fatalf("first fact = %+v, want status=starting", starting)
	}
	// The session identity rides the fact (nocx-3oupk): the renderer
	// compares it against the open ack's pair, so a status out of a
	// previous incarnation is refused. Assert the values are the session's
	// own, not minted at emit time.
	sess, err := e.ws.registry.Get(session.ID(sid))
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	if starting.InstanceID != string(sess.Identity().InstanceID) {
		t.Errorf("instanceId = %q, want %q", starting.InstanceID, sess.Identity().InstanceID)
	}
	if starting.SessionEpoch != sess.Identity().Epoch {
		t.Errorf("sessionEpoch = %d, want %d", starting.SessionEpoch, sess.Identity().Epoch)
	}

	// A stand-in for the session's shell: it holds until the test says go,
	// then hands its process over to another executable, which is the
	// takeover measured on the owner's machine.
	shell := exec.Command("/bin/zsh", "-c", "read gate; exec sleep 60")
	in, err := shell.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	shell.Stdout = io.Discard
	shell.Stderr = io.Discard
	if serr := shell.Start(); serr != nil {
		t.Skipf("no shell to hijack on this machine: %v", serr)
	}
	t.Cleanup(func() {
		_ = shell.Process.Kill()
		_ = shell.Wait()
	})

	// The composition root's seam, verbatim: an observation becomes the
	// session's status and nothing else.
	watcher := procwatch.New(logger)
	t.Cleanup(func() { _ = watcher.Close() })
	stop, err := watcher.Started(shell.Process.Pid, "/bin/zsh", func(o procwatch.Observation) {
		e.ws.NoteShellReplaced(session.ID(sid), o.Name)
	})
	if errors.Is(err, procwatch.ErrUnsupported) {
		t.Skip("this platform does not observe process replacement; the handshake bound is the detector here")
	}
	if err != nil {
		t.Fatalf("watch the shell: %v", err)
	}
	t.Cleanup(stop)

	if _, err := io.WriteString(in, "go\n"); err != nil {
		t.Fatalf("release the shell: %v", err)
	}
	_ = in.Close()

	// By the property that makes it the outcome, never by position: this env
	// enters the axis after `open` has returned, which races the open
	// handler's own emit, and the frame that lands where the outcome was
	// expected is then a second `starting` (readIntegrationWhere says why).
	raw, got := readIntegrationWhere(t, e.conn, sid,
		"the fact that concludes the axis", integrationConcluded)
	validateJSON(t, schema, raw, "session.integrationChanged params (real socket, observed process)")
	if got.Status != IntegrationConventional {
		t.Errorf("status = %q, want conventional", got.Status)
	}
	if got.Reason != string(ssh.ReasonStartupDidNotReturn) {
		t.Errorf("reason = %q, want startup-did-not-return", got.Reason)
	}
	if got.Detail == nil {
		t.Fatal("no detail: the details chain has no observation line, which is the defect nocx-viil.3 names")
	}
	if got.Detail.ObservedProcess != "sleep" {
		t.Errorf("observedProcess = %q, want the executable that took the shell's place",
			got.Detail.ObservedProcess)
	}
	if got.Shell != "/bin/zsh" {
		t.Errorf("shell = %q, want the launch's own answer, unrevised", got.Shell)
	}
}
