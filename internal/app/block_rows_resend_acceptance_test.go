package app

// THE ACCEPTANCE (nocx-zg3k3.5.3), over the real helper daemon: the
// coordinator goes away, a command then prints its WHOLE output for nobody
// and finishes, and the coordinator comes back — the block in history ends
// up with the command's whole output. See the test's own doc for the
// deterministic shape.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

// THE ACCEPTANCE (nocx-zg3k3.5.3), over the real helper daemon: the
// coordinator goes away, a command then prints its whole output for nobody
// and finishes, and the coordinator comes back — the block in history ends
// up with the command's whole output. The first composition root shuts
// down before a row is printed; the command polls a release file (the
// deterministic hold, never a duration) and writes a done file when its
// whole output has departed; the second root re-adopts the surviving
// session and the pump resends everything the scrollback still holds. The
// paired half — a short absence loses nothing — reads the block's own
// metadata the way the renderer does: sealed, no truncation, no lost and
// no unavailable rows.
func TestABlockEndsWithTheWholeOutputAfterACoordinatorRestart(t *testing.T) {
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	binary := filepath.Join(helperRoot(home, src.hash()), "nocx-helper")
	t.Cleanup(func() { endTheDaemon(t, binary) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}

	release := filepath.Join(t.TempDir(), "resend-release")
	done := filepath.Join(t.TempDir(), "resend-done")
	// The pane id is the client's own: the real renderer mints one per
	// pane, creates it in the workspace, and opens with it -- and the
	// record's pane anchor, everything the restored block is listed and
	// read through, hangs off it. The same two steps, over the same wire.
	conn := dialAppWS(t, a)
	// The workspace the app seeded at first run: the tab goes there, the
	// way the real client's own first pane does.
	state := callAppWS(t, conn, "layout.read", map[string]any{}, 1)
	if state.Error != nil {
		t.Fatalf("layout.read: %+v", state.Error)
	}
	var layout struct {
		DefaultWorkspaceID string `json:"defaultWorkspaceId"`
	}
	if unmarshalErr := json.Unmarshal(state.Result, &layout); unmarshalErr != nil || layout.DefaultWorkspaceID == "" {
		t.Fatalf("layout.read = %s (err %v): no default workspace", state.Result, unmarshalErr)
	}
	paneID := uuid.Must(uuid.NewV7()).String()
	tabCreated := callAppWS(t, conn, "tabs.create", map[string]any{
		"id":          uuid.Must(uuid.NewV7()).String(),
		"workspaceId": layout.DefaultWorkspaceID,
		"position":    0,
		"layout":      "column",
		"firstPane": map[string]any{
			"id": paneID, "cwd": "/", "kind": "local", "sizeShare": 1,
		},
	}, 2)
	if tabCreated.Error != nil {
		t.Fatalf("tabs.create: %+v", tabCreated.Error)
	}
	opened, err := a.Transport.OpenSession(ctx, transport.OpenSpec{
		Cols: 80, Rows: 24, PaneID: paneID,
	})
	if err != nil {
		t.Fatalf("opening a local pane through the shipped opener: %v", err)
	}
	p := &pane{sess: opened.Session}
	watchCtx, cancelWatch := context.WithCancel(context.Background())
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- a.Transport.WatchSessionOutput(watchCtx, opened.Session.ID(), func(data []byte) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.out.Write(data)
		})
	}()
	t.Cleanup(func() {
		cancelWatch()
		if werr := <-watchDone; werr != nil && !errors.Is(werr, context.Canceled) {
			t.Errorf("reading the pane's output from its ring: %v", werr)
		}
		_ = opened.Session.Close()
	})

	// The command prints its first two hundred rows at once, then holds
	// on the release file: the restart happens while it is mid-command.
	cmd := "r=" + release + "; i=1; while [ $i -le 200 ]; do echo R$i; i=$((i+1)); done; " +
		"while [ ! -f $r ]; do sleep 0.1; done; " +
		"while [ $i -le 300 ]; do echo R$i; i=$((i+1)); done; echo done > " + done
	if _, writeErr := p.sess.Write([]byte(cmd + "\n")); writeErr != nil {
		t.Fatalf("writing the command into the pane: %v", writeErr)
	}
	// The ring seeing R150 means every rows frame up to it was already
	// processed by the same read loop: the store holds a real prefix when
	// the restart comes.
	p.await(t, regexp.MustCompile("R150"))

	// THE RESTART: the process equivalent of quitting and relaunching. The
	// daemon survives; the session and its PTY are its own; the command
	// never noticed.
	a.Shutdown(ctx)

	// THE TAIL DEPARTS FOR NOBODY: with no coordinator anywhere, the
	// release lets the command print its last hundred rows and finish.
	// The done file is the state that says the whole tail has departed;
	// only then does the replacement root come up, so the rows can reach
	// the block only through the helper's resend from scrollback.
	if werr := os.WriteFile(release, []byte("go"), 0o600); werr != nil {
		t.Fatalf("releasing the command: %v", werr)
	}
	for {
		if _, statErr := os.Stat(done); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			t.Fatalf("stat the done file: %v", statErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	a2, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	if startErr := a2.Start(ctx); startErr != nil {
		t.Fatalf("Start after restart: %v", startErr)
	}
	defer a2.Shutdown(ctx)
	// Every post-restart question is asked of the SECOND root: the first
	// one is shut down, and a connection it once served answers nothing
	// worth asserting about this incarnation.
	conn2 := dialAppWS(t, a2)
	defer func() { _ = conn2.Close() }()

	// The block closes on the command's completion and reads back whole:
	// every row, in order, however many restarts it crossed. The entry is
	// found by its command through history.query (scope everywhere: the
	// command ran before this incarnation was even built) and read through
	// the item read, which takes the entry directly.
	deadline := time.Now().Add(30 * time.Second)
	var itemID string
	var settledStatus string
	for {
		resp := callAppWS(t, conn2, "history.query", map[string]any{
			"scope": "everywhere", "text": "resend-release", "limit": 50,
		}, 7)
		if resp.Error != nil {
			t.Fatalf("history.query: %+v", resp.Error)
		}
		var q struct {
			Entries []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"entries"`
		}
		if unmarshalErr := json.Unmarshal(resp.Result, &q); unmarshalErr != nil {
			t.Fatalf("decode history.query: %v (raw %s)", unmarshalErr, resp.Result)
		}
		if len(q.Entries) == 1 && q.Entries[0].Status != "running" {
			itemID = q.Entries[0].ID
			settledStatus = q.Entries[0].Status
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the command's block never closed: entries = %+v", q.Entries)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if settledStatus != "success" {
		t.Fatalf("the command settled %q, want success: the completion is carried across the restart", settledStatus)
	}
	// The entry closes the instant the completion lands; the tail rows'
	// appends are still in flight behind the same single-writer store. The
	// observable that says no more rows are coming is the artifact's seal
	// — the resent end marker, wire-ordered after every row. The count and
	// the seal are BOTH required before the wait ends: a break on the
	// count alone races the seal op queued behind it on the same
	// single-writer store, and a break on the seal alone would bless a
	// block that sealed short of the command's whole output.
	deadline = time.Now().Add(30 * time.Second)
	for {
		// The seal is read FIRST and the rows after it: the seal commits
		// after the block's last append, so rows read once the seal is seen
		// are the whole stored body. Read the other way round, the rows can
		// be a snapshot taken before the last append and the seal one taken
		// after it, and a complete block reads as one sealed short.
		got := callAppWS(t, conn2, "ledger.get", map[string]any{"id": itemID}, 8)
		if got.Error != nil {
			t.Fatalf("ledger.get: %+v", got.Error)
		}
		var probe struct {
			Artifacts []struct {
				MediaType string `json:"mediaType"`
				State     string `json:"state"`
			} `json:"artifacts"`
		}
		if unmarshalErr := json.Unmarshal(got.Result, &probe); unmarshalErr != nil {
			t.Fatalf("decode ledger.get: %v (raw %s)", unmarshalErr, got.Result)
		}
		sealed := false
		for _, art := range probe.Artifacts {
			if art.MediaType == string(content.MediaBlockRows) && art.State == "sealed" {
				sealed = true
			}
		}
		read, readErr := a2.Transport.ReadSessionItem(ctx, string(p.sess.ID()), itemID, 0, 400)
		if readErr != nil {
			t.Fatalf("ReadSessionItem after the restart: %v", readErr)
		}
		if read.Total >= 300 && sealed {
			break
		}
		if sealed && read.Total < 300 {
			t.Fatalf("the block sealed at %d rows, want the command's whole output of 300 — the rows the helper still owed never landed", read.Total)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the block never finished draining: %d rows, artifact unsealed", read.Total)
		}
		time.Sleep(20 * time.Millisecond)
	}
	read, err := a2.Transport.ReadSessionItem(ctx, string(p.sess.ID()), itemID, 0, 400)
	if err != nil {
		t.Fatalf("ReadSessionItem after the restart: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(read.Text, "\n"), "\n")
	// THE BLOCK HOLDS THE COMMAND'S WHOLE OUTPUT: three hundred rows, in
	// order. The coordinator went away mid-command and changed nothing —
	// the first root's rows stayed stored, the second root's attach
	// re-bound the open block from the store (ADR-0076 decision 3), the
	// resend and the live stream appended behind its cursor, and the
	// helper's own end report settled it.
	if len(lines) != 300 {
		t.Fatalf("the block holds %d rows (status %q) spanning %q..%q, want the command's whole output of 300", len(lines), settledStatus, lines[0], lines[len(lines)-1])
	}
	for i, line := range lines {
		if want := fmt.Sprintf("R%d", i+1); line != want {
			t.Fatalf("row %d reads %q, want %q — the output crossed the restart out of order or lossy", i, line, want)
		}
	}
	// THE PAIRED HALF — the short absence, nothing pruned: the store
	// marks nothing missing. The block's own metadata, read the way the
	// renderer reads it, is sealed, names no truncation, and carries no
	// lost and no unavailable rows.
	got := callAppWS(t, conn2, "ledger.get", map[string]any{"id": itemID}, 8)
	if got.Error != nil {
		t.Fatalf("ledger.get: %+v", got.Error)
	}
	var entry struct {
		Artifacts []struct {
			MediaType string          `json:"mediaType"`
			State     string          `json:"state"`
			Truncated *string         `json:"truncated"`
			Payload   json.RawMessage `json:"payload"`
		} `json:"artifacts"`
	}
	if unmarshalErr := json.Unmarshal(got.Result, &entry); unmarshalErr != nil {
		t.Fatalf("decode ledger.get: %v (raw %s)", unmarshalErr, got.Result)
	}
	var rowsArt *struct {
		MediaType string          `json:"mediaType"`
		State     string          `json:"state"`
		Truncated *string         `json:"truncated"`
		Payload   json.RawMessage `json:"payload"`
	}
	for i := range entry.Artifacts {
		if entry.Artifacts[i].MediaType == string(content.MediaBlockRows) {
			rowsArt = &entry.Artifacts[i]
		}
	}
	if rowsArt == nil {
		t.Fatalf("ledger.get holds no rows artifact: %+v", entry.Artifacts)
	}
	if rowsArt.State != "sealed" {
		t.Fatalf("rows artifact state = %q, want sealed", rowsArt.State)
	}
	if rowsArt.Truncated != nil {
		t.Fatalf("rows artifact truncated = %q, want nothing marked missing", *rowsArt.Truncated)
	}
	var payload struct {
		LostRows        uint64 `json:"lostRows"`
		UnavailableRows uint64 `json:"unavailableRows"`
	}
	if unmarshalErr := json.Unmarshal(rowsArt.Payload, &payload); unmarshalErr != nil {
		t.Fatalf("decode the rows payload: %v (raw %s)", unmarshalErr, rowsArt.Payload)
	}
	if payload.LostRows != 0 || payload.UnavailableRows != 0 {
		t.Fatalf("the payload marks %d lost / %d unavailable rows, want the short absence that lost nothing", payload.LostRows, payload.UnavailableRows)
	}
}

// THE PAIRED HALF, end to end over the real helper: the command ENDS and the
// shell EXITS while the coordinator is away. On return the block is settled
// with the command's own status — the end the shell spoke while nobody was
// attached, delivered to the returning coordinator from the cursor the first
// one stored (ADR-0076 decision 4, ADR-0077) — not left running, and not
// flattened to the "unknown" the helper's session-end report alone would
// give it: the entry reads success and the block's artifact is sealed.
func TestAShellThatExitsWhileTheCoordinatorIsAwaySettlesItsBlockOnReturn(t *testing.T) {
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	binary := filepath.Join(helperRoot(home, src.hash()), "nocx-helper")
	t.Cleanup(func() { endTheDaemon(t, binary) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if startErr := a.Start(ctx); startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}

	release := filepath.Join(t.TempDir(), "exit-release")
	done := filepath.Join(t.TempDir(), "exit-done")
	conn := dialAppWS(t, a)
	state := callAppWS(t, conn, "layout.read", map[string]any{}, 1)
	if state.Error != nil {
		t.Fatalf("layout.read: %+v", state.Error)
	}
	var layout struct {
		DefaultWorkspaceID string `json:"defaultWorkspaceId"`
	}
	if unmarshalErr := json.Unmarshal(state.Result, &layout); unmarshalErr != nil || layout.DefaultWorkspaceID == "" {
		t.Fatalf("layout.read = %s (err %v): no default workspace", state.Result, unmarshalErr)
	}
	paneID := uuid.Must(uuid.NewV7()).String()
	tabCreated := callAppWS(t, conn, "tabs.create", map[string]any{
		"id":          uuid.Must(uuid.NewV7()).String(),
		"workspaceId": layout.DefaultWorkspaceID,
		"position":    0,
		"layout":      "column",
		"firstPane": map[string]any{
			"id": paneID, "cwd": "/", "kind": "local", "sizeShare": 1,
		},
	}, 2)
	if tabCreated.Error != nil {
		t.Fatalf("tabs.create: %+v", tabCreated.Error)
	}
	opened, err := a.Transport.OpenSession(ctx, transport.OpenSpec{
		Cols: 80, Rows: 24, PaneID: paneID,
	})
	if err != nil {
		t.Fatalf("opening a local pane through the shipped opener: %v", err)
	}
	p := &pane{sess: opened.Session}
	watchCtx, cancelWatch := context.WithCancel(context.Background())
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- a.Transport.WatchSessionOutput(watchCtx, opened.Session.ID(), func(data []byte) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.out.Write(data)
		})
	}()
	t.Cleanup(func() {
		cancelWatch()
		if werr := <-watchDone; werr != nil && !errors.Is(werr, context.Canceled) {
			t.Errorf("reading the pane's output from its ring: %v", werr)
		}
		_ = opened.Session.Close()
	})

	// The command opens its block, prints its first row, and holds on the
	// release file; releasing it lets the command finish, and the `exit`
	// typed ahead behind it then ends the shell — both while the
	// coordinator is away, in the second half below.
	cmd := "echo started; while [ ! -f " + release + " ]; do sleep 0.1; done; " +
		"echo bye > " + done
	if _, writeErr := p.sess.Write([]byte(cmd + "\nexit\n")); writeErr != nil {
		t.Fatalf("writing the command into the pane: %v", writeErr)
	}
	p.await(t, regexp.MustCompile("started"))
	deadline := time.Now().Add(30 * time.Second)
	// The block exists before the restart: the shell's DEBUG trap records
	// the first simple command ("echo started") as the entry's command.
	for {
		resp := callAppWS(t, conn, "history.query", map[string]any{
			"scope": "everywhere", "text": "echo started", "limit": 50,
		}, 7)
		if resp.Error != nil {
			t.Fatalf("history.query: %+v", resp.Error)
		}
		var q struct {
			Entries []struct {
				ID string `json:"id"`
			} `json:"entries"`
		}
		if unmarshalErr := json.Unmarshal(resp.Result, &q); unmarshalErr != nil {
			t.Fatalf("decode history.query: %v (raw %s)", unmarshalErr, resp.Result)
		}
		if len(q.Entries) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the command's entry never appeared: %+v", q.Entries)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// THE COORDINATOR GOES AWAY. The shell holds; nothing about its block
	// changes (the detach is no-seal, ADR-0076).
	a.Shutdown(ctx)

	// THE SHELL EXITS WHILE THE COORDINATOR IS AWAY: with no coordinator
	// anywhere, the release lets the command finish and the `exit` end
	// the shell. The done file is the state that says the command ran;
	// the exit record the poll below waits for is the pane actually gone.
	if werr := os.WriteFile(release, []byte("go"), 0o600); werr != nil {
		t.Fatalf("releasing the command: %v", werr)
	}
	for {
		if _, statErr := os.Stat(done); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			t.Fatalf("stat the done file: %v", statErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// THE SHELL HAS ACTUALLY EXITED, on the helper's own record. The
	// coordinator-side session object died with a1's shutdown (measured:
	// its Done is closed the instant the transport stops), so it can
	// never observe the exit — and the done file precedes `exit`, so it
	// says the command ran and not that the pane is gone. The helper's
	// inventory carries the process's exit record — the same fact the
	// exit-status carry reads — and an inventory read neither attaches
	// nor adopts, so the pane's end stays unwatched while this waits
	// for its record.
	endpointHome := home
	probe, err := helperlocal.Open(ctx, helperlocal.Config{
		Dir:        endpoint.Dir(endpointHome),
		Generation: proto.GenerationID(src.hash()),
		Binary:     "", // a probe may not start a helper
		Log:        discardLogger(t),
	})
	if err != nil {
		t.Fatalf("ask the daemon's inventory about the pane: %v", err)
	}
	exited := false
	for !exited {
		entries, perr := probe.Sessions(ctx)
		if perr != nil {
			t.Fatalf("read the daemon's inventory: %v", perr)
		}
		for _, e := range entries {
			if e.HostSessionID.Session == string(opened.Session.ID()) && e.Exit != nil {
				exited = true
			}
		}
		if !exited {
			if cerr := ctx.Err(); cerr != nil {
				t.Fatalf("the daemon never recorded the pane's exit: %v", cerr)
			}
		}
	}
	if cerr := probe.Close(); cerr != nil {
		t.Fatalf("close the inventory read: %v", cerr)
	}

	a2, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	if startErr := a2.Start(ctx); startErr != nil {
		t.Fatalf("Start after restart: %v", startErr)
	}
	defer a2.Shutdown(ctx)
	conn2 := dialAppWS(t, a2)
	defer func() { _ = conn2.Close() }()

	// On return the block is settled, not left running: the entry is
	// terminal with the status the command's own completion carried — the
	// frame the shell spoke while nobody was attached, which only the
	// returning coordinator's resume from its stored cursor can deliver —
	// and the block's artifact is sealed rather than open.
	deadline = time.Now().Add(30 * time.Second)
	var itemID, itemStatus string
	for {
		resp := callAppWS(t, conn2, "history.query", map[string]any{
			"scope": "everywhere", "text": "echo started", "limit": 50,
		}, 7)
		if resp.Error != nil {
			t.Fatalf("history.query: %+v", resp.Error)
		}
		var q struct {
			Entries []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"entries"`
		}
		if unmarshalErr := json.Unmarshal(resp.Result, &q); unmarshalErr != nil {
			t.Fatalf("decode history.query: %v (raw %s)", unmarshalErr, resp.Result)
		}
		if len(q.Entries) == 1 && q.Entries[0].Status != "running" {
			itemID = q.Entries[0].ID
			itemStatus = q.Entries[0].Status
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the block was left running after the shell's exit: entries = %+v", q.Entries)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if itemStatus != "success" {
		t.Fatalf("the block settled %q, want success: the command completed while the coordinator was away, and its completion never reached the one that came back", itemStatus)
	}
	// The lifecycle status and the rows-plane close arrive on separate paths.
	// Wait for the artifact's persisted state rather than assuming the single
	// history response also orders that independent close event.
	var entry struct {
		Artifacts []struct {
			MediaType string `json:"mediaType"`
			State     string `json:"state"`
		} `json:"artifacts"`
	}
	sealed := false
	for requestID := 8; !sealed; requestID++ {
		got := callAppWS(t, conn2, "ledger.get", map[string]any{"id": itemID}, requestID)
		if got.Error != nil {
			t.Fatalf("ledger.get: %+v", got.Error)
		}
		if unmarshalErr := json.Unmarshal(got.Result, &entry); unmarshalErr != nil {
			t.Fatalf("decode ledger.get: %v (raw %s)", unmarshalErr, got.Result)
		}
		sealed = false
		for _, art := range entry.Artifacts {
			if art.MediaType == string(content.MediaBlockRows) && art.State == "sealed" {
				sealed = true
			}
		}
		if sealed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the block is settled (%q) but its rows artifact did not seal before the deadline: %+v", itemStatus, entry.Artifacts)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
