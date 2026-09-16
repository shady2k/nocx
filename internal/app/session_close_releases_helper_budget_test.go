package app

// nocx-isjh4, closer 1's coordinator-level acceptance: a helper-hosted
// session's window budget is released the moment the coordinator ends its
// side FOR GOOD, through the REAL seam a pane close actually runs —
// session.Reg.Adopt/EndSession over a real, in-process helpersession.Service
// — rather than through the transport wire or the layout chain, which are
// exercised separately (internal/transport's layout and session-handler
// tests). EndSession is deliberately not Close: Close only detaches, and is
// what shutdown and a lost re-adopt race use to leave a helper-hosted
// session alive for a later coordinator (internal/session/session.go's own
// doc on the two verbs names the invariant this split protects). Before
// AttachedSession.EndSession was wired in, nothing in the ordinary course
// ever told the helper a session was over, and this loop's SECOND iteration
// would have refused to start: the first session's reservation was never
// given back.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"testing"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	helperhost "github.com/shady2k/nocx/internal/helper/host"
	helperproto "github.com/shady2k/nocx/internal/helper/proto"
	helper "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

func TestClosingAHelperHostedSessionReleasesItsHelperWindowBudget(t *testing.T) {
	ctx := context.Background()
	logger := discardLogger(t)
	sessionLog := log.NewSlogAdapter(logger)
	const generation = "1111111111111111aaaaaaaaaaaaaaaa"

	// The window sizes sit at D8's floor (2*creditLimit,
	// internal/helper/session/session.go) rather than a round number below
	// it, which withDefaults silently raises — a budget test that ignored
	// that would be asserting arithmetic it never actually ran under. The
	// aggregate admits exactly ONE session at a time: the smallest budget
	// that can prove a release happened, and small so the test stays fast.
	const windowBytes = 2 * 64 << 10 // 128 KiB
	limits := helper.Limits{
		DefaultWindowBytes: windowBytes,
		MinWindowBytes:     windowBytes,
		MaxWindowBytes:     windowBytes,
		BudgetBytes:        windowBytes,
	}
	svc := helper.New(helper.Options{
		Generation: helperproto.GenerationID(generation),
		Spawner:    helper.NewLocalSpawner(logger, helper.Shell{Path: "/bin/sh", Args: []string{"-i"}}, ""),
		Log:        logger,
		Limits:     limits,
	})

	serverConn, clientConn := net.Pipe()
	peer := helperhost.New(serverConn, serverConn, generation, "instance-a", logger)
	peer.Register(svc)
	release := svc.Bind(peer)
	served := make(chan error, 1)
	go func() { served <- peer.Serve(ctx) }()
	t.Cleanup(func() {
		_ = clientConn.Close()
		release()
		svc.Close()
		if err := <-served; err != nil {
			t.Errorf("helper host: %v", err)
		}
	})

	c, err := helperclient.Dial(ctx, helperclient.Config{
		Exec:        helperclient.NewSocketConn(clientConn),
		ExpectHash:  generation,
		SentinelTTL: time.Second,
		Log:         logger,
	})
	if err != nil {
		t.Fatalf("helperclient.Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	reg := session.New(sessionLog, nil)

	// More iterations than the budget could ever admit at once if a close
	// leaked its reservation — the whole point of the loop over a single
	// open-close pair.
	const iterations = 5
	for i := 0; i < iterations; i++ {
		entry, err := c.Spawn(ctx, helperproto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24})
		if err != nil {
			t.Fatalf("iteration %d: spawn refused (the previous close did not release its budget): %v", i, err)
		}

		var subRaw [16]byte
		if _, rerr := rand.Read(subRaw[:]); rerr != nil {
			t.Fatalf("iteration %d: minting a subscriber id: %v", i, rerr)
		}
		attached, err := c.Attach(ctx, helperproto.AttachParams{
			Subscriber: helperproto.SubscriberID(hex.EncodeToString(subRaw[:])),
			Session: helperproto.HostSessionID{
				Generation: helperproto.GenerationID(generation),
				Session:    entry.HostSessionID.Session,
			},
			Fresh: true, RequestWrite: true,
		})
		if err != nil {
			t.Fatalf("iteration %d: attach: %v", i, err)
		}

		sess, err := reg.Adopt(ctx, session.Config{Kind: session.KindLocal, PaneID: "pane-under-test"},
			session.ID(entry.HostSessionID.Session), attached)
		if err != nil {
			t.Fatalf("iteration %d: adopt: %v", i, err)
		}

		if used := svc.WindowBytesInUse(); used != windowBytes {
			t.Fatalf("iteration %d: window bytes in use = %d while the session is open, want %d", i, used, windowBytes)
		}

		// This is the seam under test: the SAME EndSession a pane's removal
		// from the layout, or the explicit "close" RPC, already runs
		// (internal/transport/ws.go's closeSessionsForPanes and
		// sessionOpsHandlers.handleClose both call registry.EndSession —
		// deliberately not registry.Close, which only detaches and is what
		// shutdown and a lost re-adopt race use to leave a helper-hosted
		// session alive for someone else to take back).
		if err := reg.EndSession(sess.ID()); err != nil {
			t.Fatalf("iteration %d: close: %v", i, err)
		}

		if used := svc.WindowBytesInUse(); used != 0 {
			t.Fatalf("iteration %d: window bytes in use = %d after close, want 0 — the helper session was not released", i, used)
		}
	}
}
