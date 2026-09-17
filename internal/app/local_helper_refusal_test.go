package app

// WHEN THIS MACHINE'S HELPER WILL NOT COME UP, THE PERSON IS TOLD WHAT FAILED,
// WHY, AND WHAT TO DO (nocx-ie23r.4, ADR-0057, design L4).
//
// There is no fallback: every local pane is a session on this machine's
// nocx-helper daemon, so a helper that cannot be installed, started or
// handshaken means no pane opens at all — by another route or by any route.
// What replaces the terminal is a refusal, and these tests open a pane the way
// a person does — over the app's own WebSocket, on the real composition root,
// reading the frame a renderer receives — because a payload a test built
// proves the sentence is well-formed and not that the server sends it.
//
// THE THREE PARTS ARE ASSERTED SEPARATELY, and that is the point of the file.
// A test that looked for one substring about "the helper" would pass on a
// refusal that names only its category, which is the failure mode ADR-0057
// names: the person learns their terminal will not open and is given nothing
// to do about it. So each case asserts the REASON (a value from the closed
// set, on the wire), the ACTION (a value from the closed set, on the wire) and
// the CONCRETE ERROR (in the sentence), plus that the sentence carries the
// action's own words to the person.
//
// Each boundary is reached the way a machine genuinely reaches it: ONE install
// case drives a real failure — the product's own Lstat fails with ENOTDIR when
// the helper root is a file — while the rest are SEEDED states a test cannot
// produce (a full filesystem does not exist on demand), and their
// classification is pinned by the classifier table further down. The binary is
// there and not executable, and the endpoint is served by something that is not
// this build's helper. None of them is a stub of the opener.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/deploy"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// helperRefusalFrame is the refusal as it arrived: the three parts, still
// separate. Nothing here is derived from the message — the reason and the
// action are the frame's own fields, which is what lets the assertions below
// mean what they say.
type helperRefusalFrame struct {
	code   int
	reason string
	action string
	// message is the sentence a person reads.
	message string
}

// openOnThisMachine opens one local pane over the app's socket and answers
// with the refusal it produced, or fails the test if a pane opened.
func openOnThisMachine(t *testing.T, a *App) helperRefusalFrame {
	t.Helper()
	conn := dialAppWS(t, a)
	defer func() { _ = conn.Close() }()

	raw := jsonrpcCallRaw(t, conn, "open", map[string]any{"cols": 80, "rows": 24}, 1)
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    *struct {
				Reason string `json:"reason"`
				Action string `json:"action"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode the open response: %v (%s)", err, raw)
	}
	if resp.Error == nil {
		t.Fatalf("a pane opened where this machine's helper cannot serve one: %s", raw)
	}
	if resp.Error.Data == nil {
		t.Fatalf("the refusal names no reason and no action: they ride the frame as values, and a sentence "+
			"a surface has to parse is a sentence it will one day parse wrong (%s)", raw)
	}
	return helperRefusalFrame{
		code:    resp.Error.Code,
		reason:  resp.Error.Data.Reason,
		action:  resp.Error.Data.Action,
		message: resp.Error.Message,
	}
}

// brokenMachine boots the shipped composition root over an isolated home with
// the local helper's install pointed at src, lets start finish, and then hands
// the caller the point a broken machine is in when somebody asks for a
// terminal: after Start and before the first pane. beforeStart breaks the home
// the install will find, which is how a case makes the install fail FOR REAL;
// afterStart breaks the machine in the specific way it is about.
func brokenMachine(
	t *testing.T,
	src deploy.ArtifactSource,
	beforeStart func(t *testing.T, home string),
	afterStart func(t *testing.T, home string),
) *App {
	t.Helper()
	home := storagetest.IsolateWithHome(t)
	if beforeStart != nil {
		beforeStart(t, home)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	a, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	if afterStart != nil {
		afterStart(t, home)
	}
	return a
}

// servedBySomeoneElse puts something on this machine's endpoint that is not
// this build's helper — the socket a second copy of nocx, or anything else
// holding it, would be answering from. It answers the handshake only when the
// case asks it to; silence is its default, because a silent peer is what a
// wedged daemon looks like and what the handshake's budget exists for.
func servedBySomeoneElse(t *testing.T, home string, gen proto.GenerationID, answer func(net.Conn)) {
	t.Helper()
	dir := endpoint.Dir(home)
	if err := os.MkdirAll(dir, endpoint.DirMode); err != nil {
		t.Fatalf("create the endpoint directory: %v", err)
	}
	path, err := endpoint.Path(dir, gen)
	if err != nil {
		t.Fatalf("the endpoint path for %s: %v", gen, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// Every accepted connection is HELD until the test ends, and that is not
	// hygiene: an unreferenced net.Conn is finalized by the garbage collector,
	// and finalizing a socket CLOSES it. The silent case below therefore used
	// to present as "connection reset by peer" — a peer that answered by
	// hanging up — whenever the collector happened to run inside the
	// handshake's budget, which is the one thing this case exists to
	// distinguish from a timeout (nocx-50w7p.10 measured it: the same test,
	// the same tree, red once and green twice with nothing changed in
	// between).
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			_ = c.Close()
		}
	})
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
			if answer == nil {
				// Held open and never answered: the endpoint is being served
				// and says nothing, which is exactly what the handshake's
				// budget is for.
				continue
			}
			go answer(conn)
		}
	}()
}

// answeredAsAnotherBuild speaks the helper protocol well enough to be heard
// and answers with a hello-ok that is not this build's — the wire shape of "a
// different generation is serving this endpoint".
//
// The sentinel line is restated here because internal/helper/client keeps its
// own copy private, and this file may not edit that package. A drift would be
// LOUD: the peer would stop being recognized as a handshaking helper at all
// and the case would assert the wrong refusal, in red.
func answeredAsAnotherBuild(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	payload, err := json.Marshal(proto.HelloOK{
		Version:     proto.Version,
		Nonce:       "0198f2b0-0000-7000-8000-00000000ffff",
		ContentHash: strings.Repeat("f", 64),
		InstanceID:  "another-build",
	})
	if err != nil {
		return
	}
	answered := append([]byte("nocx-helper "+proto.Version+" ready\n"), proto.EncodeFrame(proto.TypeHelloOK, 0, 0, payload)...)
	_, _ = conn.Write(answered)
	// Drained and held open: the client must be the one that decides this is
	// not its helper, and a peer that hung up would race that decision against
	// the transport-loss watcher (internal/helper/local says why: over a socket
	// those are one event with three observers).
	buf := make([]byte, 4096)
	for {
		if _, rerr := conn.Read(buf); rerr != nil {
			return
		}
	}
}

// THE REFUSAL, BOUNDARY BY BOUNDARY.
func TestAPaneRefusalNamesWhatFailedWhyAndWhatToDo(t *testing.T) {
	// A payload the install can write, so the cases that are about the START
	// and the HANDSHAKE have a generation on disk to reach for.
	installed := fakeArtifacts{payload: []byte("#!/bin/sh\nexit 0\n")}

	for _, tc := range []struct {
		name string
		// src is what Start installs, or fails to.
		src deploy.ArtifactSource
		// beforeStart breaks the home the install finds, so a case can make
		// the install fail for real rather than through a seeded error.
		beforeStart func(t *testing.T, home string)
		// afterStart breaks the machine the way this case is about.
		afterStart func(t *testing.T, home string)
		// wantReason and wantAction are the two closed-set values the frame
		// must carry, spelled as they go over the wire.
		wantReason string
		wantAction string
		// wantWhy is the concrete error, and wantDo a fragment of the action's
		// own words — the two halves a person reads, asserted apart from the
		// values above.
		wantWhy string
		wantDo  string
		// wantAlso are further fragments the sentence must carry, for a case
		// whose real error text is not one fixed string.
		wantAlso []string
	}{
		{
			name: "the install had no room, so the disk is the remedy",
			src: fakeArtifacts{err: fmt.Errorf(
				"deploy: write helper: %w", syscall.ENOSPC)},
			wantReason: "install",
			wantAction: "free-space",
			wantWhy:    "no space left on device",
			wantDo:     "Free space",
		},
		{
			name: "this build carries no local artifact at all",
			src: fakeArtifacts{err: errors.New(
				"deploy: no helper artifact for this platform")},
			wantReason: "install",
			wantAction: "reinstall-nocx",
			wantWhy:    "no helper artifact for this platform",
			wantDo:     "Reinstall or update nocx",
		},
		{
			name: "the installed helper is not executable",
			src:  installed,
			afterStart: func(t *testing.T, home string) {
				t.Helper()
				binary := filepath.Join(helperRoot(home, installed.hash()), "nocx-helper")
				// 0600: the file is there, the bytes are right, and the exec
				// fails — the machine state the "not executable" part of the
				// bead names.
				if err := os.Chmod(binary, 0o600); err != nil {
					t.Fatalf("make the installed helper non-executable: %v", err)
				}
			},
			wantReason: "start",
			wantAction: "reinstall-nocx",
			wantWhy:    "permission denied",
			wantDo:     "Reinstall or update nocx",
		},
		{
			name: "something answered and it is not this build",
			src:  installed,
			afterStart: func(t *testing.T, home string) {
				t.Helper()
				servedBySomeoneElse(t, home, proto.GenerationID(installed.hash()), answeredAsAnotherBuild)
			},
			wantReason: "handshake",
			wantAction: "quit-other-nocx",
			wantWhy:    "not our helper",
			wantDo:     "Quit every other nocx window",
		},
		{
			name: "the endpoint accepted and said nothing",
			src:  installed,
			afterStart: func(t *testing.T, home string) {
				t.Helper()
				// No answer: the peer holds the connection and speaks. The
				// refusal is the handshake's own budget expiring, which is the
				// only thing a silent endpoint can be said to have done.
				servedBySomeoneElse(t, home, proto.GenerationID(installed.hash()), nil)
			},
			wantReason: "handshake",
			wantAction: "retry-open",
			wantWhy:    "sentinel timeout",
			wantDo:     "Open the pane again",
		},
		{
			// FOR REAL: a plain file where the helper root belongs makes the
			// installer's OWN Lstat (deploy's completeness check reads
			// .install-complete under the generation directory) fail with the
			// OS's ENOTDIR, so the refusal quotes an error the PRODUCT
			// produced rather than one this test wrote. The seeded cases above
			// still carry the states a test cannot produce without a full
			// filesystem (ENOSPC/EDQUOT are pinned by the classification table
			// instead).
			name: "the helper root is a file, so the install fails for real",
			src:  installed,
			beforeStart: func(t *testing.T, home string) {
				t.Helper()
				root := filepath.Join(home, ".nocx")
				if err := os.MkdirAll(root, 0o700); err != nil {
					t.Fatalf("create the nocx directory: %v", err)
				}
				if err := os.WriteFile(filepath.Join(root, "helper"), []byte("a file, not the helper root"), 0o600); err != nil {
					t.Fatalf("put a file where the helper root belongs: %v", err)
				}
			},
			wantReason: "install",
			wantAction: "reinstall-nocx",
			wantWhy:    "not a directory",
			wantDo:     "Reinstall or update nocx",
			wantAlso:   []string{".nocx/helper", ".install-complete"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := brokenMachine(t, tc.src, tc.beforeStart, tc.afterStart)

			got := openOnThisMachine(t, a)

			if got.code != -32603 {
				t.Fatalf("code = %d, want -32603: a helper that cannot serve a pane is not the caller's mistake "+
					"(-32602) and not a plain internal error with nothing said", got.code)
			}
			if got.reason != tc.wantReason {
				t.Fatalf("what failed = %q, want %q (message %q)", got.reason, tc.wantReason, got.message)
			}
			if got.action != tc.wantAction {
				t.Fatalf("what to do = %q, want %q (message %q)", got.action, tc.wantAction, got.message)
			}
			if !strings.Contains(got.message, tc.wantWhy) {
				t.Fatalf("why = %q, want it to carry %q: the second part is the concrete error the boundary "+
					"produced, never the category the reason already names", got.message, tc.wantWhy)
			}
			if !strings.Contains(got.message, tc.wantDo) {
				t.Fatalf("the sentence a person reads says nothing about what to do: %q does not carry %q",
					got.message, tc.wantDo)
			}
			for _, also := range tc.wantAlso {
				if !strings.Contains(got.message, also) {
					t.Fatalf("why = %q, want it to carry %q: the real error the boundary produced", got.message, also)
				}
			}
		})
	}
}

// NOT INSTALLED AT ALL is its own machine state, and it is the one every test
// that does not opt the install in reaches: no artifact source is wired, so
// nothing was ever attempted. It must still refuse with all three parts.
func TestAPaneRefusesWhenNothingInstalledThisMachinesHelper(t *testing.T) {
	storagetest.IsolateWithHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := newTestApp(t) // the default: no local helper install at all
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	defer a.Shutdown(context.Background())

	got := openOnThisMachine(t, a)
	if got.reason != "install" {
		t.Fatalf("what failed = %q, want \"install\": nothing was ever put on disk", got.reason)
	}
	if got.action != "reinstall-nocx" {
		t.Fatalf("what to do = %q, want \"reinstall-nocx\"", got.action)
	}
	if !strings.Contains(got.message, "not installed") {
		t.Fatalf("why = %q, want the state named: no generation was installed", got.message)
	}
}

// countingHost is an attention surface that records what it was asked to
// raise. It is the product's notification boundary, not a log line: L4's rule
// is about a person being interrupted, and a banner is what does that.
type countingHost struct {
	mu     sync.Mutex
	raised int
}

func (h *countingHost) Banner(context.Context, notify.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.raised++
	return nil
}

func (h *countingHost) Badge(context.Context, int) error { return nil }
func (h *countingHost) Bounce(context.Context) error     { return nil }

func (h *countingHost) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.raised
}

// A FAILED PROBE AT START IS NOT NEWS (design L4, ADR-0057). A person who has
// not asked for a terminal has not been harmed, and a startup banner about a
// daemon is noise they cannot act on. It surfaces at the ACT instead — the
// pane they do open is refused, with the three parts.
//
// BOTH ATTENTION SURFACES ARE BOUND, because "no notification" has to mean no
// notification anywhere: the banner host (countingHost) and the toast recorder
// (recordingToast, the same surface app_notify_toast_test.go owns) share the
// one raise path the product uses (notifyIngress), so a route that skipped one
// of them would show up here.
//
// THE POSITIVE CONTROL is what makes the zeros mean something: it raises ONE
// notification through that same path and waits for BOTH observers to move, so
// a zero can only be "nothing was raised" rather than "the observer is not on
// the live route". The wait is on the observable with a hang limit (waitFor),
// never on a duration. Start raises nothing at all here, so the simple
// zero-assertion form is the one the criterion needs; had Start raised an
// unrelated notification, the discrimination would have been equality against
// a successful install instead, per the criterion.
func TestAFailedLocalInstallRaisesNoNotification(t *testing.T) {
	home := storagetest.IsolateWithHome(t)
	refused := errors.New("deploy: no helper artifact for this platform")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := newTestApp(t, withLocalHelperArtifacts(fakeArtifacts{err: refused}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	host := &countingHost{}
	a.SetAttentionHost(host)
	toast := &recordingToast{}
	a.notifyToast.Set(toast)

	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	defer a.Shutdown(context.Background())

	// Nothing was installed, and nothing was raised about it.
	if _, statErr := os.Lstat(filepath.Join(home, ".nocx", "helper")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused artifact created the helper root: %v", statErr)
	}
	if host.count() != 0 || len(toast.seen()) != 0 {
		t.Fatalf("the failed install raised notification(s) at start: banner=%d toast=%d — a person who has "+
			"not asked for a terminal must not be interrupted by a daemon they cannot act on",
			host.count(), len(toast.seen()))
	}

	// It surfaces HERE, at the act, and still without a notification: a
	// refused open is an answer the pane shows, not an interruption.
	got := openOnThisMachine(t, a)
	if got.reason != "install" || got.action == "" {
		t.Fatalf("the refusal at the act = %+v, want an install refusal that names an action", got)
	}
	if host.count() != 0 || len(toast.seen()) != 0 {
		t.Fatalf("opening a pane raised notification(s): banner=%d toast=%d — the refusal rides the open's own "+
			"answer", host.count(), len(toast.seen()))
	}

	// THE POSITIVE CONTROL: one notification through the product's own raise
	// path, and BOTH observers must move, or the zeros above prove nothing.
	if out := a.notifyIngress.Raise(ctx, programEvent("hr1-control")); out.Err != nil {
		t.Fatalf("the control notification was refused before acceptance: %v", out.Err)
	}
	waitFor(t, "the control notification to reach both attention surfaces", func() bool {
		return host.count() > 0 && len(toast.seen()) > 0
	})
}

// The refusal reaches the wire whole: the WHOLE sentence is intact — not
// truncated, not re-wrapped, not replaced by the taxonomy's "Internal error"
// — and the two values beside it survive JSON.
//
// "Intact" is asserted as equality against the three parts assembled here:
// the reason's opening words, the COMPLETE concrete error the boundary
// produced, and the COMPLETE action sentence for the free-space remedy, in
// that order, on one line. A suffix check and a not-contains check would pass
// on a sentence that had lost a part in the middle; equality does not.
func TestAHelperRefusalSurvivesTheWireIntact(t *testing.T) {
	refused := fmt.Errorf("deploy: write helper: %w", syscall.ENOSPC)
	a := brokenMachine(t, fakeArtifacts{err: refused}, nil, nil)

	// Read the frame a second time through the same seam and compare it with
	// what the first read produced: a sentence assembled per response would
	// differ between two opens of one broken machine.
	first := openOnThisMachine(t, a)
	second := openOnThisMachine(t, a)

	if first.message != second.message {
		t.Fatalf("the refusal is not stable across opens:\n first  %q\n second %q", first.message, second.message)
	}
	// The three parts, in order, on one line. The middle part is built from
	// the SAME fake error the boundary failed with, so the assertion says the
	// sentence quotes it completely rather than containing a fragment of it.
	want := "Nocx could not install the helper that owns every pane on this machine: " +
		refused.Error() + ". " +
		"Free space on the disk that holds nocx's own directory, then open the pane again."
	if first.message != want {
		t.Fatalf("the sentence is not the three parts intact:\n got  %q\n want %q", first.message, want)
	}
	if first.code != -32603 || first.reason != "install" || first.action != "free-space" {
		t.Fatalf("the refusal's values changed between the socket and the assertion: %+v", first)
	}
}

// THE CLASSIFICATION IS PINNED FOR EVERY SENTINEL, deterministically, BESIDE
// the socket tests rather than instead of them (nocx-ie23r.4, design L4).
//
// The two classifiers are exercised directly here because their inputs cannot
// all be reached deterministically over a socket: a peer closing mid-handshake
// produces THREE different errors depending on which goroutine wins — a bare
// EPIPE from the hello write, ErrLost, or ErrNotOurHelper (internal/helper/local
// says so itself) — and the version mismatch needs a peer that refuses the
// protocol version. So the end-to-end path is pinned by the socket cases in
// this file for the boundaries a machine can genuinely be put into, and the
// classification itself — the mapping from a sentinel to a reason and an
// action — is pinned here for every sentinel internal/helper/local's local
// open path can hand this classifier. Four client sentinels are ABSENT
// because the LOCAL carrier cannot produce them: ErrExecForbidden and
// ErrHelperNotServing belong to the remote exec path, ErrRequestTooLarge is a
// frame-size refusal, and *RefusalError is a reply to an op the local open
// path never sends.
//
// The assertion is on the SENTENCE, because the sentence is what the wire
// carries: each case's message must contain the reason's own words AND the
// action's own words. Each reason fragment is chosen so it cannot match another
// reason's sentence, so a classifier that named the wrong boundary fails here
// rather than passing on a shared word.
func TestHelperRefusalClassificationNamesReasonAndAction(t *testing.T) {
	// The three reason fragments, each unique to its reason's sentence.
	const (
		installReason   = "could not install the helper"
		startReason     = "and it did not start"
		handshakeReason = "did not answer as the build that installed it"
	)
	// The action fragments, each unique to its action's words.
	const (
		reinstallAction = "Reinstall or update nocx"
		freeSpaceAction = "Free space"
		fixPermsAction  = "Make sure nocx's own directory"
		quitOtherAction = "Quit every other nocx window"
		retryAction     = "Open the pane again"
	)
	const gen = "4-linux-amd64-deadbeef"

	cases := []struct {
		name       string
		classify   func(error) error
		cause      error
		wantReason string
		wantAction string
	}{
		{
			// Nothing serving and nothing to dial: the start boundary, with no
			// more specific fact to override the reason's own action.
			name:       "a bare endpoint with no helper behind it",
			classify:   refuseLocalHelperUnreachable,
			cause:      endpoint.ErrNoEndpoint,
			wantReason: startReason,
			wantAction: reinstallAction,
		},
		{
			// A start that failed because the directory is closed: same
			// boundary, same remedy — the start classifier knows only that the
			// binary did not come up.
			name:       "the start could not reach the helper because the directory is closed",
			classify:   refuseLocalHelperUnreachable,
			cause:      fmt.Errorf("endpoint: start a helper for %s: %w", gen, fs.ErrPermission),
			wantReason: startReason,
			wantAction: reinstallAction,
		},
		{
			// The peer answered and hung up on the hello write: one observer
			// of the mid-handshake close, and a handshake failure.
			name:       "the peer hung up on the hello write",
			classify:   refuseLocalHelperUnreachable,
			cause:      fmt.Errorf("helper: write hello: %w", syscall.EPIPE),
			wantReason: handshakeReason,
			wantAction: retryAction,
		},
		{
			name:       "the handshake's sentinel timed out",
			classify:   refuseLocalHelperUnreachable,
			cause:      helperclient.ErrSentinelTimeout,
			wantReason: handshakeReason,
			wantAction: retryAction,
		},
		{
			name:       "the carrier reported the peer closed",
			classify:   refuseLocalHelperUnreachable,
			cause:      helperclient.ErrLost,
			wantReason: handshakeReason,
			wantAction: retryAction,
		},
		{
			name:       "another build is answering the endpoint",
			classify:   refuseLocalHelperUnreachable,
			cause:      helperclient.ErrNotOurHelper,
			wantReason: handshakeReason,
			wantAction: quitOtherAction,
		},
		{
			name:       "the content hash was not this build's",
			classify:   refuseLocalHelperUnreachable,
			cause:      helperclient.ErrHashMismatch,
			wantReason: handshakeReason,
			wantAction: quitOtherAction,
		},
		{
			name:       "the peer refused the protocol version",
			classify:   refuseLocalHelperUnreachable,
			cause:      helperclient.ErrVersionMismatch,
			wantReason: handshakeReason,
			wantAction: reinstallAction,
		},
		{
			// The endpoint directory (<home>/.nocx/run) belongs to another
			// account. This arm is reached only AFTER a successful install,
			// when the dial finds a run directory somebody else owns, so the
			// reason is START — the install did succeed and nothing of ours is
			// serving — and the remedy is the directory the person can fix.
			name:       "the endpoint directory belongs to another account",
			classify:   refuseLocalHelperUnreachable,
			cause:      endpoint.ErrForeignDir,
			wantReason: startReason,
			wantAction: fixPermsAction,
		},
		{
			name:       "the install had no room",
			classify:   refuseLocalHelperNotInstalled,
			cause:      fmt.Errorf("deploy: write helper: %w", syscall.ENOSPC),
			wantReason: installReason,
			wantAction: freeSpaceAction,
		},
		{
			name:       "the install hit an allocation limit",
			classify:   refuseLocalHelperNotInstalled,
			cause:      fmt.Errorf("deploy: write helper: %w", syscall.EDQUOT),
			wantReason: installReason,
			wantAction: freeSpaceAction,
		},
		{
			name:       "the install directory is not writable",
			classify:   refuseLocalHelperNotInstalled,
			cause:      fmt.Errorf("deploy: install directory: %w", fs.ErrPermission),
			wantReason: installReason,
			wantAction: fixPermsAction,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.classify(tc.cause).Error()
			if !strings.Contains(msg, tc.wantReason) {
				t.Fatalf("the sentence does not name WHAT FAILED: %q carries neither %q", msg, tc.wantReason)
			}
			if !strings.Contains(msg, tc.wantAction) {
				t.Fatalf("the sentence does not name WHAT TO DO: %q does not carry %q", msg, tc.wantAction)
			}
		})
	}
}
