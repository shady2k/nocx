package app

// The half of a hosted open that is the same wherever the helper is.
//
// A hosted open is three acts: spawn a shell on a helper this coordinator
// already holds, attach to it, and adopt the result into the session registry
// under the id the HELPER minted. Nothing in those three depends on whether
// the helper was reached over an ssh exec lane or over this machine's own
// socket — the carrier is decided before any of it runs and is not consulted
// again — so they live here once rather than twice.
//
// WHY THIS FILE EXISTS AT ALL. Until nocx-ie23r.3 there was one hosted opener
// and the three acts were inline in it. The local route needed the same three,
// and a copy would have been a second implementation of one concept: the two
// would have agreed on the day they were written and disagreed the first time
// either moved — over which failure closes the attachment, whether the
// lifecycle leg is aborted before or after the session is closed, whether a
// refused write lease is a failure. Each of those is a decision with an
// argument behind it (AGENTS.md, "look for the existing answer").
//
// WHAT IS DELIBERATELY NOT HERE is everything the two routes genuinely do not
// share: which helper to reach and how to get a connection to it, what the
// durable binding says, and who owns the connection afterwards. A remote open
// holds one helper process per session and closes it when the open fails; a
// local one holds a single connection to this machine's daemon for every pane
// and must not close it because one pane could not start. So this function
// NEVER closes the client it was handed — the caller owns it, on both sides of
// every return.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// hostedSpawn is the three acts, with the seams they need and nothing that
// names a carrier.
type hostedSpawn struct {
	client   *helperclient.Client
	registry *session.Reg
	// lifecycle is the authenticated-channel kernel (ADR-0024). Nil is a
	// legitimate wiring — a server built without lifecycle publishing — and
	// produces a conventional session rather than a failure.
	lifecycle lifecyclechannel.Kernel
	// loss carries the adapter's loss cause to the session integration axis.
	// Nil reports nowhere and the adapter still logs it.
	loss func(lifecycle.LaneID, lifecyclechannel.LossCause)
	// helloTimeout bounds how long a shell may take to prove itself before
	// the session falls back to conventional. It is passed rather than left
	// to the adapter's default because it is a PRODUCT decision and the
	// composition root is where product decisions belong — the same argument
	// internal/app's local pty factory made when it set the same bound on the
	// same adapter. Zero keeps the adapter's default, which is what the
	// remote hosted route has always used.
	helloTimeout time.Duration
	// publishScreen is the screen plane's transport half: one reassembled
	// session.frame document, published to the pane's subscriber on the
	// data plane's reserved seat. Nil is a legitimate wiring — a server
	// built without a transport — and registers no observer rather than
	// dropping frames nobody asked for.
	publishScreen        func(sid session.ID, revision uint64, doc []byte) bool
	publishSandboxAccess func(transport.SandboxAccessChanged)
	// blockRows is the streamed block output's transport half
	// (helper_block_rows.go): the rows that leave the screen and each
	// command's end become the command's block in history. Nil wires nothing.
	blockRows blockRowsSink
	// environmentEntries registers this pane's lane against its own
	// completion downlink (environment_entry.go, nocx-2v80t.3.21), so a
	// LATER child domain's hello — authenticated on a transport of its own
	// (an ssh child's forwarded listener, never this pane's own descriptor)
	// — can still be told down to the SAME runtime that owns this pane's
	// PTY. Nil wires nothing, the same shape blockRows and publishScreen
	// already have.
	environmentEntries *environmentEntryRegistry
	// cursors keeps the pane's lifecycle cursor (lifecycle_cursor.go,
	// ADR-0077) with its binding, for the coordinator that takes the session
	// back. Nil keeps nothing.
	cursors lifecycleCursorStore
	// stopping is the coordinator's stopping signal. Nil never stops.
	stopping *atomic.Bool
	log      *slog.Logger
}

// hostedSpawnResult is what the three acts produced, as facts rather than as a
// half-filled wire shape: each caller composes its OWN
// transport.HostedSessionOpen from these plus what only it knows — the host,
// the account, the generation, the route back.
type hostedSpawnResult struct {
	Session       session.Session
	Entry         helperclient.SessionEntry
	LifecycleLane lifecycle.LaneID
	// LifecycleTransport names the transport the lane rides, which is what a
	// nested sudo/su's grant is composed against (nocx-u7uh.11). Carried
	// beside the lane rather than derived from it because only the adapter
	// knows it, and a caller that guessed would compose a child bootstrap for
	// a transport the parent is not on.
	LifecycleTransport lifecycle.TransportID
	StartLifecycle     func()
	AbortLifecycle     func()
	// DetachLifecycle ends the pane's lifecycle leg as an ORDERLY HANDOVER —
	// the coordinator giving the session back to its helper (process
	// shutdown, a re-adopt that lost the write-lease) — with no loss
	// anywhere: the kernel is not told, the open attempts stay, and the
	// store's open entry and open block survive for whichever coordinator
	// re-adopts (ADR-0076). AbortLifecycle above is the failure rollback;
	// this is the departure.
	DetachLifecycle    func()
	ObserveOutputHoles func(func(lost uint64, reason string))
	candidate          *hostedSpawnCandidate
}

// spawnFunc is the ONE act that differs between the panes this opener hosts:
// which op carries the spawn request.
//
// It is a parameter rather than a second copy of run, on the file header's own
// argument. The lifecycle leg, the attach, the adoption and the rollback order
// between them are decisions with reasons behind them, and a second
// implementation would carry the code without the reasons. The op is a fourth
// member of the list of things that genuinely differ — beside which helper to
// reach, what the durable binding says, and who owns the connection afterwards
// — rather than a reason to fork the other three.
//
// Why the op differs at all is the wire's own rule (contracts/helper): every
// shape is `additionalProperties: false`, so a destination folded into `spawn`
// would be a payload an older generation REJECTS, while `spawn-ssh` is a new op
// it answers `unknown_op` to — which a coordinator already reads as "this
// machine's helper is older than this app". So the two params structs are two
// shapes on purpose, and life is handed to the caller because only the caller
// knows which of them carries the launch.
type spawnFunc func(ctx context.Context, life *proto.LifecycleLaunch) (helperclient.SessionEntry, error)

func (h hostedSpawn) run(ctx context.Context, cfg session.Config, spawn spawnFunc) (hostedSpawnResult, error) {
	candidate, err := h.openCandidate(ctx, cfg, spawn)
	if err != nil {
		return hostedSpawnResult{}, err
	}
	return candidate.Publish(ctx)
}

func (h hostedSpawn) openCandidate(ctx context.Context, cfg session.Config, spawn spawnFunc) (*hostedSpawnCandidate, error) {
	out, err := h.openCandidateStages(ctx, cfg, spawn)
	if err != nil {
		return nil, err
	}
	if out.candidate == nil {
		return nil, errors.New("hosted helper open did not produce a private candidate")
	}
	return out.candidate, nil
}

type hostedSpawnCandidate struct {
	entry       helperclient.SessionEntry
	publish     func(context.Context) (hostedSpawnResult, error)
	abort       func(context.Context) error
	publishOnce sync.Once
	abortOnce   sync.Once
	mu          sync.Mutex
	started     bool
	completed   bool
	published   bool
	aborted     bool
	result      hostedSpawnResult
	err         error
	abortErr    error
}

func (c *hostedSpawnCandidate) Publish(ctx context.Context) (hostedSpawnResult, error) {
	if c == nil {
		return hostedSpawnResult{}, errors.New("nil hosted candidate")
	}
	c.publishOnce.Do(func() {
		c.mu.Lock()
		if c.aborted {
			c.completed = true
			c.err = errors.New("hosted candidate was aborted before publication")
			c.mu.Unlock()
			return
		}
		c.started = true
		c.mu.Unlock()
		result, err := c.publish(ctx)
		c.mu.Lock()
		c.result, c.err = result, err
		c.published, c.completed = result.Session != nil, true
		c.mu.Unlock()
	})
	return c.result, c.err
}

func (c *hostedSpawnCandidate) Abort(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	started, completed, published := c.started, c.completed, c.published
	if !started && !completed && !published {
		c.aborted = true
	}
	err := c.err
	c.mu.Unlock()
	if published || (started && !completed) {
		return errors.New("cannot abort a publishing or published hosted session")
	}
	if completed {
		return err
	}
	c.abortOnce.Do(func() { c.abortErr = c.abort(ctx) })
	return c.abortErr
}

func (h hostedSpawn) openCandidateStages(ctx context.Context, cfg session.Config, spawn spawnFunc) (hostedSpawnResult, error) {
	var subscriberRaw [16]byte
	if _, err := rand.Read(subscriberRaw[:]); err != nil {
		return hostedSpawnResult{}, err
	}
	subscriber := proto.SubscriberID(hex.EncodeToString(subscriberRaw[:]))

	var lifecycleAdapter *lifecyclechannel.Adapter
	var lifecyclePeer net.Conn
	var lifecyclePublished atomic.Bool
	// life is the launch the adapter mints, held here rather than written onto
	// a params struct this function no longer owns: which struct carries it is
	// the caller's business (see spawnFunc).
	var life *proto.LifecycleLaunch
	// THE COMPLETION DOWNLINK (owner decision 2026-09-19). The lifecycle
	// channel is authenticated HERE, in the coordinator's kernel; the
	// emulator and the rendezvous live in the helper. So the coordinator
	// carries each already-authenticated completion DOWN to the helper
	// session that owns the pane, over session.lifecycle-complete, and
	// authentication does not move: the observing wrapper adds no gate — an
	// err==nil from Ingest IS the kernel's acceptance, and a finish the
	// kernel refused is observed by nothing. The wrapper is built BEFORE
	// the spawn and the bind happens only when the spawn RPC has answered,
	// because a shell that completes a command inside that window is
	// accepted by the kernel while the helper session's identity is still
	// unknown; the downlink buffers what it accepted in that window and
	// delivers it, in acceptance order, the moment the bind names the
	// session.
	var downlink *helperclient.CompletionDownlink
	// stopDownlink ends the delivery context; nil with the downlink. Its
	// lifetime is the hosted session's, and the two ends are named below:
	// AbortLifecycle's rollback arms, and the session's own end.
	var stopDownlink context.CancelFunc
	// paneLife is that delivery context: the pane's own lifetime, which the
	// lane's registration below is tied to as well (nocx-2v80t.3.32).
	var paneLife context.Context
	// cursor is the leg's applied cursor (ADR-0077): built with the adapter
	// it is an option of, bound to the session once the helper names it.
	var cursor *lifecycleCursor
	if h.lifecycle != nil {
		// THE DELIVERY CONTEXT IS THE HOSTED SESSION'S LIFETIME, not the open
		// request's. The request context is cancelled the moment the
		// renderer's socket drops — while the PTY deliberately lives on
		// (AD-9) — and a completion the kernel accepts after that would die
		// with it instead of reaching the helper session that owns the pane.
		// WithoutCancel detaches from the request's cancellation and keeps
		// its values (the log fields); the cancel is hung off the session's
		// own end — bindDownlinkToSession below, and the rollback arms —
		// which is the lifetime's existing owner: the transport's teardown
		// goroutine waits on the same Done.
		sessionCtx, cancelSession := context.WithCancel(context.WithoutCancel(ctx))
		stopDownlink, paneLife = cancelSession, sessionCtx
		// A delivery that fails is retried, and one that is lost is logged
		// by the downlink itself, through log.From on this same context.
		downlink = helperclient.NewCompletionDownlink(h.client, sessionCtx, boundaryLossTo(h.blockRows))
		driveKernel := helperclient.NewCompletionObservingKernel(h.lifecycle, downlink)

		coordinatorConn, peerConn := net.Pipe()
		cursor = newLifecycleCursor(ctx, h.cursors, h.stopping)
		opts := []lifecyclechannel.Option{
			lifecyclechannel.WithLossReporter(func(lane lifecycle.LaneID, cause lifecyclechannel.LossCause) {
				if lifecyclePublished.Load() && h.loss != nil {
					h.loss(lane, cause)
				}
			}),
			lifecyclechannel.WithFrameScope(cursor.applyFrame),
		}
		if h.helloTimeout > 0 {
			opts = append(opts, lifecyclechannel.WithHelloTimeout(h.helloTimeout))
		}
		// THE CAUSE LINE JOINS THE EXCHANGE (nocx-n14oo.3). The adapter's
		// loss is reported from a hello timer and a read pump, neither of
		// which holds a context — and a hello-timeout is the single most
		// diagnostic line a failed pane produces. Binding the caller's
		// exchange onto its logger HERE, where the identity exists, is what
		// puts it beside the wait it explains instead of a timestamp away.
		adapter, err := lifecyclechannel.NewStream(
			log.NewSlogAdapter(h.log).WithContext(ctx), driveKernel, coordinatorConn, opts...)
		if err != nil {
			_ = peerConn.Close()
			stopDownlink()
			return hostedSpawnResult{}, err
		}
		lifecycleAdapter, lifecyclePeer = adapter, peerConn
		launch := adapter.Launch()
		life = &proto.LifecycleLaunch{
			Lane: string(launch.Lane), Domain: string(launch.Domain),
			Epoch: launch.Epoch, Capability: launch.Capability, Recovery: launch.Recovery,
		}
	}
	// THE ROLLBACK ENDS THE DOWNLINK TOO: every arm below that aborts the
	// lifecycle leg is an open that never became a pane, and a delivery
	// context with no pane is exactly the lifetime this file refuses to keep.
	abortLifecycleNow := func() error {
		var lifecycleErr, peerErr error
		if lifecycleAdapter != nil {
			lifecycleErr = lifecycleAdapter.Close()
			peerErr = lifecyclePeer.Close()
		}
		if stopDownlink != nil {
			stopDownlink()
		}
		return errors.Join(lifecycleErr, peerErr)
	}
	// The detach mirrors the rollback but ends the adapter with Detach, not
	// Close: the leg's adapter learns the handover from this side instead of
	// reading its own carrier's EOF as the loss it is not.
	detachLifecycleNow := func() {
		if lifecycleAdapter != nil {
			_ = lifecycleAdapter.Detach()
			_ = lifecyclePeer.Close()
		}
		if stopDownlink != nil {
			stopDownlink()
		}
	}

	entry, err := spawn(ctx, life)
	if err != nil {
		return hostedSpawnResult{}, errors.Join(err, abortLifecycleNow())
	}

	// The rows plane is held from the first frame, for the reason the
	// re-adopt's is (heldRows): the stream is bound below.
	holdOpt, held := holdRowsBeforeAttach(h.blockRows)
	attached, err := h.client.Attach(ctx, proto.AttachParams{
		Subscriber: subscriber,
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		},
		Offset: proto.StreamOffset(entry.Window.Base), Fresh: true,
		LifecycleOffset: 0, LifecycleFresh: true, RequestWrite: true,
	}, holdOpt)
	if err != nil {
		closeErr := h.client.CloseSession(ctx, entry.HostSessionID)
		return hostedSpawnResult{}, errors.Join(err, abortLifecycleNow(), closeErr)
	}
	if cursor != nil {
		cursor.bind(entry.HostSessionID.Session, attached.LifecycleIngested())
	}

	candidate := &hostedSpawnCandidate{entry: entry}
	closeCandidate := func(closeCtx context.Context, stopRows func()) error {
		if stopRows != nil {
			stopRows()
		}
		attachErr := attached.Close()
		lifecycleErr := abortLifecycleNow()
		closeErr := h.client.CloseSession(closeCtx, entry.HostSessionID)
		return errors.Join(attachErr, lifecycleErr, closeErr)
	}
	candidate.abort = func(abortCtx context.Context) error {
		return closeCandidate(abortCtx, nil)
	}
	candidate.publish = func(publishCtx context.Context) (hostedSpawnResult, error) {
		lifecyclePublished.Store(true)
		if downlink != nil {
			downlink.Bind(entry.HostSessionID)
		}
		// Public projections are installed only after this publication stage
		// is entered. Attach may drain privately while the durable commit runs.
		if cfg.Host != "" {
			attached.OnLiveness(func(responsive bool, roundTripMS int64) {
				h.registry.ObserveHost(cfg.Host, ssh.Reachability{
					Responsive: responsive,
					RoundTrip:  time.Duration(roundTripMS) * time.Millisecond,
				})
			})
		}
		if h.publishScreen != nil {
			screenSid := session.ID(entry.HostSessionID.Session)
			attached.OnScreenFrame(func(revision uint64, doc []byte) {
				h.publishScreen(screenSid, revision, doc)
			})
			attached.OnScreenLost(func(reason string) {
				log.From(publishCtx).Warn("screen assembly lost on the carrier",
					"session", string(screenSid), "reason", reason)
			})
		}
		stopBlockRows := bindHeldBlockRows(publishCtx, h.blockRows, session.ID(entry.HostSessionID.Session), attached, held)
		sess, adoptErr := h.registry.Adopt(publishCtx, cfg, session.ID(entry.HostSessionID.Session), attached)
		if adoptErr != nil {
			if cfg.LaunchBinding.LaunchID != "" {
				// Selection already committed. Detach this failed publisher;
				// exact-head recovery, not rollback, owns the live process.
				if stopBlockRows != nil {
					stopBlockRows()
				}
				detachLifecycleNow()
				return hostedSpawnResult{}, errors.Join(adoptErr, attached.Close())
			}
			return hostedSpawnResult{}, errors.Join(adoptErr, closeCandidate(publishCtx, stopBlockRows))
		}
		if h.publishSandboxAccess != nil && cfg.LaunchBinding.LaunchID != "" && cfg.PaneID != "" {
			expected := proto.HostSessionID{
				Generation: proto.GenerationID(entry.HostSessionID.Generation),
				Session:    entry.HostSessionID.Session,
			}
			publishNotice := sandboxAccessNoticeRelay(sess, h.publishSandboxAccess)
			attached.OnSandboxAccessChanged(func(change proto.SandboxAccessChanged) {
				if change.Session != expected || change.LaunchID != cfg.LaunchBinding.LaunchID {
					return
				}
				publishNotice(transport.SandboxAccessChanged{
					PaneID: cfg.PaneID, LaunchID: change.LaunchID,
					Revision: change.Revision, Dropped: change.Dropped,
					Observer: change.Observer, Total: change.Total,
				})
			})
		}
		bindBlockEndToSession(sess, attached, h.blockRows, session.ID(entry.HostSessionID.Session), stopBlockRows)
		if stopDownlink != nil {
			bindDownlinkToSession(sess, stopDownlink)
		}
		out := hostedSpawnResult{Session: sess, Entry: entry, ObserveOutputHoles: attached.OnOutputHole}
		if lifecycleAdapter != nil {
			out.LifecycleLane = lifecycleAdapter.Lane()
			out.LifecycleTransport = lifecycleAdapter.TransportID()
			if h.environmentEntries != nil && downlink != nil {
				h.environmentEntries.register(paneLife, out.LifecycleLane, downlink)
			}
			var startOnce sync.Once
			out.StartLifecycle = func() {
				startOnce.Do(func() {
					bridgeLifecycle(log.NewSlogAdapter(h.log).WithContext(publishCtx),
						lifecycleAdapter.TransportID(), lifecyclePeer, attached.Lifecycle(), cursor, attached)
				})
			}
			var abortOnce sync.Once
			out.AbortLifecycle = func() { abortOnce.Do(func() { _ = abortLifecycleNow() }) }
			var detachOnce sync.Once
			out.DetachLifecycle = func() { detachOnce.Do(detachLifecycleNow) }
		}
		return out, nil
	}
	return hostedSpawnResult{Entry: entry, candidate: candidate}, nil
}

// bindDownlinkToSession ends a completion downlink's delivery context when
// the hosted session's own lifetime ends. sess.Done() is the signal the
// transport's teardown owner already waits on (monitorExit), so the downlink
// hangs off the existing edge rather than owning a lifetime of its own — two
// owners of one lifetime being the defect whichever wins. One goroutine per
// hosted pane, exactly like that monitor; it exits at the session's end.
func bindDownlinkToSession(sess session.Session, stop context.CancelFunc) {
	go func() {
		<-sess.Done()
		stop()
	}()
}

// bindBlockEndToSession ends a hosted session's block-rows streaming when
// the session's own lifetime ends. sess.Done() is the helper-reported end
// of the shell (the same edge monitorExit waits on), so the block the
// helper still holds open settles HERE — the one detach that seals
// (ADR-0076) — and the coordinator-side teardown afterwards changes no
// block. One goroutine per hosted pane, exactly like monitorExit; it exits
// at the session's end.
func bindBlockEndToSession(sess session.Session, attached *helperclient.AttachedSession, sink blockRowsSink, sid session.ID, stop context.CancelFunc) {
	go func() {
		<-sess.Done()
		// sess.Done fires on coordinator wire loss too; only the HELPER'S
		// OWN exit report (recordExit, the status monitorExit reads) is
		// the session's end as the helper states it (ADR-0076). A wire
		// loss seals nothing: the open block and its cursor survive for
		// whichever coordinator re-adopts the session. No attached session
		// object means no helper-reported exit either.
		if attached != nil && sink != nil {
			if _, reported := attached.WaitErr(); reported {
				sink.HelperSessionEnded(sid)
			}
		}
		stop()
	}()
}

// sandboxAccessNoticeRelay keeps the helper client's read loop out of the
// coordinator's durable-head lookup and renderer broadcast path.
func sandboxAccessNoticeRelay(sess session.Session, publish func(transport.SandboxAccessChanged)) func(transport.SandboxAccessChanged) {
	var mu sync.Mutex
	var latest transport.SandboxAccessChanged
	wake := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-sess.Done():
				return
			case <-wake:
				mu.Lock()
				change := latest
				mu.Unlock()
				publish(change)
			}
		}
	}()
	return func(change transport.SandboxAccessChanged) {
		mu.Lock()
		latest = change
		mu.Unlock()
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}
