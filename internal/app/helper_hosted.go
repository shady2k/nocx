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

// run spawns, attaches and adopts.
//
// THE ORDER IS THE ROLLBACK and each step names what is true if the next one
// fails: a lifecycle leg that is established and then not used is aborted; a
// session that is spawned and then not attached is closed on the helper; a
// session that is attached and then not adopted is closed on both sides. The
// one thing no arm does is close the connection, for the reason the file
// header gives.
func (h hostedSpawn) run(ctx context.Context, cfg session.Config, params proto.SpawnParams) (hostedSpawnResult, error) {
	var subscriberRaw [16]byte
	if _, err := rand.Read(subscriberRaw[:]); err != nil {
		return hostedSpawnResult{}, err
	}
	subscriber := proto.SubscriberID(hex.EncodeToString(subscriberRaw[:]))

	var lifecycleAdapter *lifecyclechannel.Adapter
	var lifecyclePeer net.Conn
	if h.lifecycle != nil {
		coordinatorConn, peerConn := net.Pipe()
		opts := []lifecyclechannel.Option{lifecyclechannel.WithLossReporter(h.loss)}
		if h.helloTimeout > 0 {
			opts = append(opts, lifecyclechannel.WithHelloTimeout(h.helloTimeout))
		}
		adapter, err := lifecyclechannel.NewStream(
			log.NewSlogAdapter(h.log), h.lifecycle, coordinatorConn, opts...)
		if err != nil {
			_ = peerConn.Close()
			return hostedSpawnResult{}, err
		}
		lifecycleAdapter, lifecyclePeer = adapter, peerConn
		launch := adapter.Launch()
		params.Lifecycle = &proto.LifecycleLaunch{
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

	entry, err := h.client.Spawn(ctx, params)
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

	sess, err := h.registry.Adopt(cfg, session.ID(entry.HostSessionID.Session), attached)
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
			startOnce.Do(func() { bridgeLifecycle(lifecyclePeer, attached.Lifecycle()) })
		}
		var abortOnce sync.Once
		out.AbortLifecycle = func() { abortOnce.Do(abortLifecycleNow) }
	}
	return out, nil
}
