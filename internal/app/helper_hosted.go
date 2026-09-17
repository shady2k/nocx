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
	"log/slog"
	"net"
	"sync"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
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
	log          *slog.Logger
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
	ObserveOutputHoles func(func(lost uint64, reason string))
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

// run spawns, attaches and adopts.
//
// THE ORDER IS THE ROLLBACK and each step names what is true if the next one
// fails: a lifecycle leg that is established and then not used is aborted; a
// session that is spawned and then not attached is closed on the helper; a
// session that is attached and then not adopted is closed on both sides. The
// one thing no arm does is close the connection, for the reason the file
// header gives.
func (h hostedSpawn) run(ctx context.Context, cfg session.Config, spawn spawnFunc) (hostedSpawnResult, error) {
	var subscriberRaw [16]byte
	if _, err := rand.Read(subscriberRaw[:]); err != nil {
		return hostedSpawnResult{}, err
	}
	subscriber := proto.SubscriberID(hex.EncodeToString(subscriberRaw[:]))

	var lifecycleAdapter *lifecyclechannel.Adapter
	var lifecyclePeer net.Conn
	// life is the launch the adapter mints, held here rather than written onto
	// a params struct this function no longer owns: which struct carries it is
	// the caller's business (see spawnFunc).
	var life *proto.LifecycleLaunch
	if h.lifecycle != nil {
		coordinatorConn, peerConn := net.Pipe()
		opts := []lifecyclechannel.Option{lifecyclechannel.WithLossReporter(h.loss)}
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
			log.NewSlogAdapter(h.log).WithContext(ctx), h.lifecycle, coordinatorConn, opts...)
		if err != nil {
			_ = peerConn.Close()
			return hostedSpawnResult{}, err
		}
		lifecycleAdapter, lifecyclePeer = adapter, peerConn
		launch := adapter.Launch()
		life = &proto.LifecycleLaunch{
			Lane: string(launch.Lane), Domain: string(launch.Domain),
			Epoch: launch.Epoch, Capability: launch.Capability, Recovery: launch.Recovery,
		}
	}
	abortLifecycleNow := func() {
		if lifecycleAdapter != nil {
			_ = lifecycleAdapter.Close()
			_ = lifecyclePeer.Close()
		}
	}

	entry, err := spawn(ctx, life)
	if err != nil {
		abortLifecycleNow()
		return hostedSpawnResult{}, err
	}

	attached, err := h.client.Attach(ctx, proto.AttachParams{
		Subscriber: subscriber,
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		},
		Offset: proto.StreamOffset(entry.Window.Base), Fresh: true,
		LifecycleOffset: 0, LifecycleFresh: true, RequestWrite: true,
	})
	if err != nil {
		abortLifecycleNow()
		_ = h.client.CloseSession(ctx, entry.HostSessionID)
		return hostedSpawnResult{}, err
	}

	// THE HELPER'S OWN KEEPALIVE PROBER, RELAYED (nocx-y6fh7 item 6). A local
	// pane has no far end to probe (cfg.Host is empty, by the same rule
	// readopt's own comment states — a local carrier reports no destination),
	// so the wire notification never arrives for one and this is a no-op
	// registration rather than a local/remote branch to keep in step by
	// hand. ObserveHost is the SAME producer the coordinator's own
	// non-helper dials have always fed (hostLivenessObserver); a helper's
	// notification is just a second producer for the one function that
	// decides what either means.
	if cfg.Host != "" {
		attached.OnLiveness(func(responsive bool, roundTripMS int64) {
			h.registry.ObserveHost(cfg.Host, ssh.Reachability{
				Responsive: responsive,
				RoundTrip:  time.Duration(roundTripMS) * time.Millisecond,
			})
		})
	}

	sess, err := h.registry.Adopt(ctx, cfg, session.ID(entry.HostSessionID.Session), attached)
	if err != nil {
		_ = attached.Close()
		abortLifecycleNow()
		_ = h.client.CloseSession(ctx, entry.HostSessionID)
		return hostedSpawnResult{}, err
	}

	out := hostedSpawnResult{
		Session: sess, Entry: entry,
		ObserveOutputHoles: attached.OnOutputHole,
	}
	if lifecycleAdapter != nil {
		out.LifecycleLane = lifecycleAdapter.Lane()
		out.LifecycleTransport = lifecycleAdapter.TransportID()
		var startOnce sync.Once
		out.StartLifecycle = func() {
			startOnce.Do(func() {
				bridgeLifecycle(log.NewSlogAdapter(h.log).WithContext(ctx),
					lifecycleAdapter.TransportID(), lifecyclePeer, attached.Lifecycle())
			})
		}
		var abortOnce sync.Once
		out.AbortLifecycle = func() { abortOnce.Do(abortLifecycleNow) }
	}
	return out, nil
}
