package client_test

// nocx-xn63t.6.3: git.open over a live far-helper connection killed the
// session's lifecycle channel in about 3ms, and with it the whole session.
// Before ADR-0057/ADR-0068 a pane's PTY was never opened through the far
// helper from the start, so git.open on an ALREADY-LIVE far-helper
// connection carrying a session — the session and git services sharing one
// host.Host / client.Client pair, exactly as cmd/nocx-helper's `serve`
// registers them — was never exercised. This file drives that traffic for
// real: a real session service (a real /bin/sh under a real PTY, with its
// lifecycle plane spawned exactly as a coordinator spawns it) and the real
// git service (hostsvc over internal/git/local, a real repository on disk),
// registered on ONE host.Host, over ONE client.Client — the same sharing
// internal/app/helper_git.go's hostHelper does when git.open reuses the
// session's own client rather than dialing a second one.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// hostedSessionAndGit builds a helper peer that registers BOTH the real
// session service and the real git service on one host.Host — the shape
// cmd/nocx-helper/main.go's serve() registers on every connection
// (services.go: h.Register(git); h.Register(sessions)) — over one
// client.Client, the shape a live far-helper pane shares with its git.open
// (internal/app/helper_git.go's hostHelper: one connectLocked dial, reused
// by both Attach and the git factory).
func hostedSessionAndGit(t *testing.T) *client.Client {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := session.New(session.Options{
		Generation: "testhash",
		Spawner:    session.NewLocalSpawner(log, session.Shell{Path: "/bin/sh"}, ""),
		Inspector:  session.NewInspector(),
		Log:        log,
		Limits:     session.DefaultLimits(),
	})
	t.Cleanup(svc.Close)
	gitSvc := hostsvc.New(localgit.NewFactory())

	conn := newFakeConn(func(in io.Reader, out io.Writer) int {
		h := hostFor(in, out, log)
		h.Register(gitSvc)
		h.Register(svc)
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	})
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// realRepo builds a real git repository on disk (a plain `git init` is
// enough for git.open to answer OpenOK) and skips the test when this
// machine has no git — the same guard hostsvc's own fixture uses.
func realRepo(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	dir := t.TempDir()
	home := t.TempDir()
	cmd := exec.Command(gitBin, "init", "-q") // #nosec G204 -- gitBin is LookPath-resolved, args are literals
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + filepath.Dir(gitBin) + ":" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

// TestGitOpenOnALiveConnectionLeavesTheSessionsLifecycleChannelOpen is the
// reproduction the bead's acceptance criteria ask for: git traffic on a
// connection already carrying a live session must not end that session.
//
// THE INVARIANT, BOTH ENDS NAMED: the lifecycle channel opens at Attach
// (LifecycleFresh: true, this test's own call below) and must stay open
// AT LEAST until this attachment's own Close/EndSession — an unrelated
// git.open on the SAME shared client is not a closing event for it. The
// assertions below are that invariant: after git.open traffic on the same
// client, the lifecycle read has not ended, the attachment has not
// finished, and the shared client itself has not been lost.
func TestGitOpenOnALiveConnectionLeavesTheSessionsLifecycleChannelOpen(t *testing.T) {
	repo := realRepo(t)
	c := hostedSessionAndGit(t)
	ctx := context.Background()

	var spawned proto.SpawnResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpSpawn, proto.SpawnParams{
		Cols: 80, Rows: 24,
		// A lifecycle-carrying session, exactly as openFarHelper spawns one
		// (internal/app/helper_git.go): the far helper opens the extra
		// descriptor and the session service starts pumping it regardless
		// of whether the shell ever writes to it.
		Lifecycle: &proto.LifecycleLaunch{
			Lane: "lane-1", Domain: "dom-1", Epoch: 1,
			Capability: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	}, &spawned); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	attached, err := c.Attach(ctx, proto.AttachParams{
		Subscriber:      proto.SubscriberID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Session:         spawned.Entry.Session,
		Offset:          0,
		Fresh:           true,
		LifecycleOffset: 0,
		LifecycleFresh:  true,
		RequestWrite:    true,
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(func() { _ = attached.Close() })

	// Drain the PTY plane continuously, exactly as a coordinator's own pump
	// would, so the shell never blocks on a full window while this test
	// hammers the git service beside it.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := attached.Read(buf); err != nil {
				return
			}
		}
	}()

	// Read the lifecycle plane in the background and report the FIRST error
	// it produces — the same shape lifecyclechannel.Adapter's own reader
	// takes when it decides "end-of-stream". Nothing is ever written to fd4
	// by /bin/sh, so this read legitimately never returns data; what it must
	// not do is return before this test says the attachment is over.
	lifecycle := attached.Lifecycle()
	t.Cleanup(func() { _ = lifecycle.Close() })
	lifecycleEnded := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		_, err := lifecycle.Read(buf)
		lifecycleEnded <- err
	}()

	// A concurrent writer on the PTY plane, so the git traffic below is not
	// the only thing moving on the wire: production never has git.open as
	// the sole occupant of a live connection, and a race that needs
	// interleaved session-data frames to show up would stay hidden without
	// this.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for i := 0; i < 50; i++ {
			if _, err := attached.Write([]byte("echo hi\n")); err != nil {
				return
			}
		}
	}()

	// THE TRAFFIC UNDER TEST: git.open, CONCURRENTLY and repeatedly, on the
	// SAME client that the live session above shares — the exact reuse
	// hostHelper.open makes in production once ADR-0057/ADR-0068 route a
	// pane's PTY through the far helper from the start. Concurrent, not
	// sequential: the production failure is dispatch-level (host.Host runs
	// every request on its own goroutine, D13), and a bug that depends on
	// two requests actually overlapping would not show up one at a time.
	const openers = 8
	const opensEach = 15
	errs := make(chan error, openers*opensEach)
	var wg sync.WaitGroup
	for w := 0; w < openers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opensEach; i++ {
				var res hostsvc.OpenResult
				if err := c.Call(ctx, "git", "open", hostsvc.OpenParams{Cwd: repo}, &res); err != nil {
					errs <- fmt.Errorf("git.open: %w", err)
					return
				}
				if res.State != git.OpenOK {
					errs <- fmt.Errorf("git.open: state=%q message=%q", res.State, res.Message)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	<-writeDone

	// The session's own control-plane traffic must also still work: a
	// session that silently lost its transport would still answer this
	// with ErrLost rather than hang, so this is a fast, deterministic check
	// that the shared client is still the one the session was attached on.
	if err := c.Call(ctx, proto.ServiceSession, proto.OpResize, proto.ResizeParams{
		Session: spawned.Entry.Session, Cols: 81, Rows: 25,
	}, nil); err != nil {
		t.Fatalf("resize after git.open traffic: %v", err)
	}

	select {
	case err := <-lifecycleEnded:
		t.Fatalf("the lifecycle channel ended while git.open ran on the shared connection: %v", err)
	default:
	}

	select {
	case <-attached.Done():
		t.Fatalf("the session's attachment ended while git.open ran on the shared connection")
	default:
	}

	select {
	case <-c.Done():
		t.Fatalf("the shared helper connection was lost while git.open ran on it: %v", c.LostErr())
	default:
	}

	// Close deliberately, and prove that IS what ends the lifecycle read —
	// the negative case above is only informative if this positive case
	// still holds: the same read used to report a premature loss reports
	// the deliberate close as ErrAttachmentClosed once we ask for it.
	_ = attached.Close()
	select {
	case err := <-lifecycleEnded:
		// take()'s own contract (sessions.go): the queue is drained AND the
		// attachment is done, delivered as (zero value, false) — Read turns
		// that into io.EOF. That is the SAME value an unexpected loss would
		// report, which is exactly why the negative assertions above (no
		// close, no Done, no Lost) are what carries the proof — this closing
		// arm only demonstrates that a deliberate close reaches this read at
		// all.
		if !errors.Is(err, io.EOF) {
			t.Fatalf("lifecycle read after a deliberate close: got %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("lifecycle read did not observe the deliberate close")
	}
}
