package app

// Re-establishing the lifecycle channel of a session taken back from a helper
// (nocx-k6p18.31) — the composition root's half.
//
// READ session_readopt.go FIRST. This is one step of that pass, split out
// because it is the step with the authority argument in it.
//
// WHAT WAS WRONG. nocx-k6p18.30 gave a replaced coordinator its pane back:
// live output, and the blocks the ledger already held. A command typed AFTER
// the return produced nothing. The lifecycle capability is minted by the
// coordinator's own kernel at spawn and handed to the far shell, so it names
// THAT process's kernel; after a replacement the shell goes on stamping every
// frame with a domain no live kernel recognises, and .30 attached the carrier
// with no adapter behind it purely so it would drain. Every frame was dropped,
// and nothing anywhere said so.
//
// WHICH LEG ACTUALLY NEEDS RE-ESTABLISHING. Not the shell's. Its end of the
// channel is a socketpair descriptor handed over at spawn and the HELPER holds
// the parent end, so nothing on the far side noticed the replacement: the
// shell has its accept, it never re-handshakes, and there is no message we
// could send it that it is written to read. What died is the coordinator's
// leg — and, with it, the registry that could place the shell's frames. So the
// helper hands the identity back and the new kernel ADOPTS it, and the shell
// notices nothing because there is nothing for it to notice.
//
// WHY THAT IS NOT "REVIVING A LOST DOMAIN", which docs/lifecycle-protocol.md
// §12 forbids. That rule governs a kernel that WATCHED its transport die and
// revoked authority: it exists so a renderer that has fallen back to a native
// prompt can never be told the old authority is good again. No kernel here
// ever saw a loss — the previous one was killed, and the new one never knew
// the domain. The shell's channel never broke. What is adopted is a domain
// that is still live on the only end that can speak for it.
//
// WHAT STOPS A STALE OR HOSTILE WRITER, since that is the question an
// authenticated channel exists to answer. Nothing about the check changes: the
// kernel authenticates domain, transport, epoch and capability on every frame
// before it consults any state, and a descendant of the shell that inherited
// the descriptor still cannot produce the capability — which is exactly what
// ADR-0024 decision 2 measured the capability to be for. Adoption adds one
// holder of that value: the replacing coordinator, over the same authenticated
// helper connection the value travelled out on, to the same trust class that
// minted it and that already holds the session's keyboard and its whole output
// stream. The one new vector adoption WOULD open is replay of the helper's
// retained lifecycle window into a domain whose capability is unchanged, and
// it is closed in session_readopt.go, where the attachment resumes at the
// window's head instead of its base.
//
// AND WHEN IT CANNOT BE DONE, THE PANE SAYS SO. Three ways it fails and all
// three are stated rather than logged: a helper generation from before the op
// existed (two are resident at once by design), a session the helper reports
// no launch for after having been asked, and a refusal from the kernel. Each
// leaves `conventional` with a reason on the integration axis, which is the
// surface the product already renders for every other way integration does not
// happen.

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// lifecycleAdoption is one attempt to re-establish a taken-back session's
// lifecycle channel, and the sentence the product will say about it.
//
// It is a value rather than a set of return parameters because the two halves
// travel to different places: `status`/`reason` ride the HostedSessionOpen to
// the integration axis, while the adapter and its carrier are wired into the
// same StartLifecycle/AbortLifecycle pair the ordinary open already uses.
type lifecycleAdoption struct {
	// status and reason are the integration axis's own vocabulary. status is
	// never empty: a session that was asked about has an answer.
	status string
	reason ssh.RefusalReason

	adapter *lifecyclechannel.Adapter
	peer    net.Conn
	lane    lifecycle.LaneID
}

// adoptLifecycle asks the helper for the identity this session's shell is
// still speaking with and takes the domain over.
//
// Every failure arm returns a STATED degrade rather than an error, and that is
// deliberate: a lifecycle channel that cannot be re-established does not make
// the session unusable and must not refuse the re-adoption. The pane comes
// back either way — live output, restored ledger, a working terminal — and the
// difference is whether it goes on producing blocks or says why it cannot.
func (rp *readoptPass) adoptLifecycle(ctx context.Context, c *client.Client, entry client.SessionEntry) lifecycleAdoption {
	kernel, ok := rp.registry.lifecycle.(lifecyclechannel.AdoptingKernel)
	if !ok || rp.registry.lifecycle == nil {
		// A coordinator built without a lifecycle kernel (a headless tool, a
		// test harness). There is nothing to state, because this process
		// never offers shell integration to anybody.
		return lifecycleAdoption{}
	}
	launch, err := c.AdoptLifecycle(ctx, entry.HostSessionID)
	if err != nil {
		rp.registry.log.Warn("the lifecycle channel of a session taken back could not be re-established; its pane will not produce blocks",
			"session_id", entry.HostSessionID.Session, "error", err)
		return lifecycleAdoption{
			status: transport.IntegrationConventional,
			reason: ssh.ReasonChannelUnavailable,
		}
	}
	if launch == nil {
		// The helper answered, and the answer is that this session never had
		// a lifecycle channel. A conventional pane that is correct exactly as
		// it stands: the axis says nothing, because absence on it means
		// "conventional by design" and this session genuinely is.
		return lifecycleAdoption{}
	}
	coordinatorConn, peerConn := net.Pipe()
	adapter, err := lifecyclechannel.NewAdoptedStream(
		log.NewSlogAdapter(rp.registry.log), kernel, coordinatorConn,
		lifecyclechannel.Launch{
			Lane:       lifecycle.LaneID(launch.Lane),
			Domain:     lifecycle.DomainID(launch.Domain),
			Epoch:      launch.Epoch,
			Capability: launch.Capability,
			Recovery:   launch.Recovery,
		},
		lifecyclechannel.WithLossReporter(rp.registry.reportLifecycleLoss))
	if err != nil {
		_ = peerConn.Close()
		rp.registry.log.Warn("the lifecycle identity a helper handed back was refused; the pane will not produce blocks",
			"session_id", entry.HostSessionID.Session, "error", err)
		return lifecycleAdoption{
			status: transport.IntegrationConventional,
			reason: ssh.ReasonChannelUnavailable,
		}
	}
	return lifecycleAdoption{
		// STARTING and not INTEGRATED, even though the domain is already
		// Established: "a domain is live" is the kernel's word, and the axis
		// reads it from the published fact (noteIntegrationLive) rather than
		// re-deriving it here. Two answers to that question is the defect
		// AD-8 names, and the transport's replay of the lane is what turns
		// this into `integrated` a moment later.
		status:  transport.IntegrationStarting,
		reason:  ssh.ReasonNone,
		adapter: adapter,
		peer:    peerConn,
		lane:    adapter.Lane(),
	}
}

// attachTo wires a successful adoption into the open the transport receives:
// the lane to register, the bridge to start once the session is installed, and
// the undo for a transport that refuses afterwards.
func (a lifecycleAdoption) attachTo(open *transport.HostedSessionOpen, attached lifecycleCarrierSource) {
	if a.adapter == nil {
		return
	}
	adapter, peer := a.adapter, a.peer
	var startOnce sync.Once
	var abortOnce sync.Once
	open.LifecycleLane = a.lane
	open.StartLifecycle = func() {
		startOnce.Do(func() { bridgeLifecycle(peer, attached.Lifecycle()) })
	}
	open.AbortLifecycle = func() {
		abortOnce.Do(func() {
			_ = adapter.Close()
			_ = peer.Close()
		})
	}
}

// abort disposes an adoption whose session never came to exist. Safe on the
// zero value, which is what every non-adopting arm returns.
func (a lifecycleAdoption) abort() {
	if a.adapter == nil {
		return
	}
	_ = a.adapter.Close()
	_ = a.peer.Close()
}

// lifecycleCarrierSource is the attachment's lifecycle half, named as the one
// method this file uses so the wiring does not depend on the whole attachment.
type lifecycleCarrierSource interface {
	Lifecycle() io.ReadWriteCloser
}
