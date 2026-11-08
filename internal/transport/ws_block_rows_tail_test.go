package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
)

func TestZeroRetentionBurstKeepsEveryStoredMarker(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputCapBytes(4 << 20)
	db := newLedgerStoreWithPolicy(t, policy)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	conn := newBoundsHelperConn(t)
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	var zero uint64
	var spawned proto.SpawnResult
	if err = c.Call(context.Background(), proto.ServiceSession, proto.OpSpawn, proto.SpawnParams{
		Cwd: "/", Cols: 120, Rows: 40, ScrollbackLines: &zero,
	}, &spawned); err != nil {
		t.Fatal(err)
	}
	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Session: spawned.Entry.Session, Subscriber: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &boundsRecorder{sid: session.ID(sid), ws: e.ws}
	rec.startConfirmations(attached.ConfirmWritten)
	t.Cleanup(func() {
		_ = c.Close()
		rec.stopConfirmations()
	})
	attached.OnOutputRows(rec.onRows)
	attached.OnIntervalEnd(rec.onEnd)
	if err := e.ws.AttachBlockRows(t.Context(), session.ID(sid), rec.offerConfirmation); err != nil {
		t.Fatalf("attach block rows: %v", err)
	}
	nonce, nonceHex := fenceNonce(1)
	command := "stty -echo; seq -f 'transcript-zero-retention-%03g' 1 5000; printf '\\033]1337;NOCX_FENCE;" + nonceHex + "\\007'"
	attempt := startsACommand(t, e, pub, lane, h, 2, command)
	typeCommand(t, c, spawned.Entry.Session, command+"\r")
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, nonce)))
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpLifecycleComplete, proto.LifecycleCompleteParams{
		Session: spawned.Entry.Session, Incarnation: proto.Incarnation{Session: spawned.Entry.Session.Session, Generation: 1}, Nonce: nonceHex,
	}, nil); err != nil {
		t.Fatal(err)
	}
	art := waitForSealed(t, db, attempt)
	var got []string
	for _, chunk := range art.Chunks {
		for _, line := range strings.Split(string(chunk), "\n") {
			if line == "" {
				continue
			}
			var row struct {
				Row struct {
					Text string `json:"text"`
				} `json:"row"`
			}
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(row.Row.Text, "transcript-zero-retention-") {
				got = append(got, strings.TrimRight(row.Row.Text, " "))
			}
		}
	}
	rec.mu.Lock()
	t.Logf("rows=%d incomplete=%v lost=%d confirmations=%d ends=%d", len(got), rec.sawIncomplete, rec.sawLoss, rec.confirmMark, len(rec.ends))
	incomplete, lost := rec.sawIncomplete, rec.sawLoss
	rec.mu.Unlock()
	if incomplete || lost != 0 || art.Truncated != nil {
		t.Fatalf("stored marker burst is incomplete: incomplete=%v lost=%d truncation=%v", incomplete, lost, art.Truncated)
	}
	if len(got) != 5000 {
		t.Fatalf("stored %d markers, want 5000; truncation=%v", len(got), art.Truncated)
	}
	for i, text := range got {
		if want := fmt.Sprintf("transcript-zero-retention-%03d", i+1); text != want {
			t.Fatalf("marker %d = %q, want %q", i, text, want)
		}
	}
}
