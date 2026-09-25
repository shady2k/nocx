package app

// The receipt-ordering half of nocx-2v80t.3.22: a command carrying a
// credential froze and painted correctly, but .ui-block-receipt never
// appeared and the header was never rewritten to the masked form
// (e2e/prompt-vault.spec.ts:103). The renderer's own guard
// (attachRecordedAck, terminal-content.ts) drops history.recorded on the
// floor, with no retry, when the block it names still reads `running` in the
// renderer's own kernel — and that is exactly the state a renderer is in
// when the receipt for a just-completed attempt reaches it AHEAD of the
// lifecycle.changed fact that would have told it the attempt is done. This
// test drives the real backend over the real socket and asserts the ORDER
// history.recorded and its completing fact reach the wire in, which is what
// the acceptance tests elsewhere assert the CONTENT of but never the order.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"

	"github.com/gorilla/websocket"
)

func TestHistoryRecorded_NeverPrecedesTheFactReportingItsAttemptDone(t *testing.T) {
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
	defer a.Shutdown(ctx)

	lifecycle := openLifecycleAppSession(t, a)
	conn := lifecycle.conn
	defer func() { _ = conn.Close() }()

	const command = "echo sk-proj-abcdefghijklmnop"
	submit := callAppWS(t, conn, "lifecycle.submitAttempt", map[string]any{
		"domain":  lifecycle.domain,
		"command": command,
		"cwd":     "/srv",
		"source":  "user",
	}, 2)
	if submit.Error != nil {
		t.Fatalf("lifecycle.submitAttempt: %+v", submit.Error)
	}
	var attempt struct {
		ID string `json:"id"`
	}
	if decodeErr := json.Unmarshal(submit.Result, &attempt); decodeErr != nil {
		t.Fatalf("decode submit: %v", decodeErr)
	}

	// Written to the pty exactly like the renderer does: immediately after
	// the submit response, with no artificial delay — the timing that lets a
	// fast command like `echo` complete before a slower backend would have
	// sent the receipt.
	sidBytes, err := session.IDToBytes(session.ID(lifecycle.id))
	if err != nil {
		t.Fatalf("session id: %v", err)
	}
	frame := transport.Frame{
		Version:   transport.FrameVersion,
		MsgType:   transport.MsgTypeData,
		SessionID: sidBytes,
		Payload:   []byte(command + "\r"),
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, frame.Encode()); err != nil {
		t.Fatalf("write command: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var sawCompletionFact bool
	var sawReceipt bool
	var receiptMaskedCount int
	var receiptCaptures int
	for {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("read: %v", readErr)
		}
		var notification struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &notification) != nil || notification.Method == "" {
			continue
		}
		switch notification.Method {
		case "history.recorded":
			var p struct {
				AttemptID   string `json:"attemptId"`
				MaskedCount int    `json:"maskedCount"`
				Captures    []any  `json:"captures"`
			}
			_ = json.Unmarshal(notification.Params, &p)
			if p.AttemptID != attempt.ID {
				continue
			}
			sawReceipt = true
			receiptMaskedCount = p.MaskedCount
			receiptCaptures = len(p.Captures)
			if !sawCompletionFact {
				t.Fatalf("history.recorded for attempt %s reached the wire before the "+
					"lifecycle.changed fact reporting it done — a renderer applies facts in "+
					"wire order and still holds the attempt open when this arrives, so the "+
					"receipt attaches to no finished block and is dropped for good "+
					"(e2e/prompt-vault.spec.ts:103, nocx-2v80t.3.22)", attempt.ID)
			}
		case "lifecycle.changed":
			var f struct {
				Lifecycle string `json:"lifecycle"`
				Attempt   *struct {
					ID    string `json:"id"`
					State string `json:"state"`
				} `json:"attempt"`
			}
			_ = json.Unmarshal(notification.Params, &f)
			if f.Attempt != nil && f.Attempt.ID == attempt.ID &&
				(f.Attempt.State == "completed" || f.Attempt.State == "unknown") {
				sawCompletionFact = true
			}
			if f.Lifecycle == "prompt_ready" && sawReceipt {
				goto done
			}
		}
	}
done:
	if !sawReceipt {
		t.Fatal("history.recorded for this attempt never arrived")
	}
	// The content is asserted elsewhere (TestCapture_SaveNowAndSaveLaterOverTheRealSocket);
	// here only as a sanity check that the receipt this test raced against
	// the fact for was the real one, not an empty stand-in.
	if receiptMaskedCount != 1 || receiptCaptures != 1 {
		t.Fatalf("receipt maskedCount=%d captures=%d, want one masked credential and one capture offer",
			receiptMaskedCount, receiptCaptures)
	}
}
