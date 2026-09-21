package session

// The helper's capture send path, over the REAL dispatch (nocx-2v80t.2.2):
// a rendezvous that settles COMPLETE produces exactly one proto.OpCapture
// ask on the coordinator connection the spawn arrived on, carrying the
// record the runtime built, and the ack it answers is decoded and reported.
// The op is a reverse one — the ask travels up the spawn's own connection —
// so the fixture stamps a fake asker onto the spawn request's context the
// same way the host stamps the real connection.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// captureContract compiles the frozen session.capture.params contract. The
// package's other contract tests (and their schema loaders) live behind the
// nocx_local_ssh tag in an external test package, so the unfinished ask's
// wire proof carries its own copy here — the schema file on disk is the one
// authority either way.
func captureContract(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	entries, err := os.ReadDir("../../../contracts/helper")
	if err != nil {
		t.Fatalf("read the helper contracts: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, readErr := os.ReadFile("../../../contracts/helper/" + e.Name())
		if readErr != nil {
			t.Fatalf("read %s: %v", e.Name(), readErr)
		}
		doc, unmarshalErr := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if unmarshalErr != nil {
			t.Fatalf("unmarshal %s: %v", e.Name(), unmarshalErr)
		}
		id := "https://nocx.local/contracts/helper/" + e.Name()
		if addErr := c.AddResource(id, doc); addErr != nil {
			t.Fatalf("register %s: %v", e.Name(), addErr)
		}
	}
	s, err := c.Compile("https://nocx.local/contracts/helper/session.capture.params.schema.json")
	if err != nil {
		t.Fatalf("compile the capture contract: %v", err)
	}
	return s
}

// validateCaptureFrame checks one raw ask against the compiled contract.
func validateCaptureFrame(s *jsonschema.Schema, raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return s.Validate(doc)
}

// recordingAsker is the coordinator side of the reverse channel, scripted.
// It records every ask and answers with the ack the test names.
type recordingAsker struct {
	mu    sync.Mutex
	ops   []string
	raws  []json.RawMessage
	acked []*proto.CaptureResult
	kept  bool
	asks  chan struct{}
}

func newRecordingAsker(kept bool) *recordingAsker {
	return &recordingAsker{kept: kept, asks: make(chan struct{}, 8)}
}

func (a *recordingAsker) Ask(_ context.Context, service, op string, params, out any) error {
	a.mu.Lock()
	a.ops = append(a.ops, service+"."+op)
	raw, err := json.Marshal(params)
	if err != nil {
		a.mu.Unlock()
		return err
	}
	a.raws = append(a.raws, raw)
	var ack proto.CaptureResult
	if op == proto.OpCapture {
		ack.Kept = a.kept
		if !a.kept {
			ack.Reason = "outputOff"
		}
	}
	if out != nil {
		if b, err := json.Marshal(ack); err == nil {
			_ = json.Unmarshal(b, out)
		}
	}
	if cr, ok := out.(*proto.CaptureResult); ok {
		cp := *cr
		a.acked = append(a.acked, &cp)
	}
	a.mu.Unlock()
	a.asks <- struct{}{}
	return nil
}

func (a *recordingAsker) captureAsks() (int, []json.RawMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	var out []json.RawMessage
	for i, op := range a.ops {
		if op == proto.ServiceSession+"."+proto.OpCapture {
			n++
			out = append(out, a.raws[i])
		}
	}
	return n, out
}

func (a *recordingAsker) awaitAsk(t *testing.T) {
	t.Helper()
	select {
	case <-a.asks:
	case <-time.After(hangLimit):
		t.Fatal("the capture ask never reached the coordinator connection")
	}
}

// The spine: a settled rendezvous on a REAL session — driven through the real
// dispatch, exactly as TestLifecycleCompleteDeliversTheKernelFactToTheRuntime
// drives it — produces exactly one capture ask, addressed to this service,
// carrying the record keyed by the settled meeting's nonce, and the closing
// screen's content is what the emulator held at the boundary.
func TestASettledRendezvousAsksTheCoordinatorToCaptureOnce(t *testing.T) {
	asker := newRecordingAsker(true)
	svc := New(Options{
		Generation:            "gen-under-test",
		Spawner:               &lcSpawner{},
		Log:                   lcTestLog(),
		RendezvousExpireAfter: (&expiryTrigger{}).afterFunc,
	})
	t.Cleanup(svc.Close)

	ctx := host.WithConnection(context.Background(), asker)
	res, err := svc.spawn(ctx, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{
			Lane: "lane-under-test", Domain: "dom-under-test", Epoch: 7,
			Capability: strings.Repeat("ab", 32),
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	id := res.Entry.Session

	// The interval's output, then the fence that ends it. The runtime drains
	// the fence from the emulator's own effects, so the sighting half is
	// the stream's, and the completion arrives on the downlink op.
	hs, err := svc.find(id)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if ingestErr := hs.runtime.Ingest([]byte("hello from the interval\r\n")); ingestErr != nil {
		t.Fatalf("ingest: %v", ingestErr)
	}
	var fence sessionruntime.FenceNonce
	for i := range fence {
		fence[i] = byte(0x2b)
	}
	nonceHex := hex.EncodeToString(fence[:])

	completion, err := json.Marshal(proto.LifecycleCompleteParams{
		Session:     id,
		Incarnation: proto.Incarnation{Session: id.Session, Generation: 1},
		Nonce:       nonceHex,
	})
	if err != nil {
		t.Fatalf("marshal completion: %v", err)
	}
	if _, err := svc.Call(ctx, proto.OpLifecycleComplete, completion); err != nil {
		t.Fatalf("lifecycle-complete: %v", err)
	}
	if err := svc.TestSightFence(id, fence, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}

	asker.awaitAsk(t)
	n, raws := asker.captureAsks()
	if n != 1 {
		t.Fatalf("the coordinator connection saw %d capture asks, want exactly one", n)
	}
	var params proto.CaptureParams
	if err := json.Unmarshal(raws[0], &params); err != nil {
		t.Fatalf("decode the capture params: %v", err)
	}
	if params.Nonce != nonceHex {
		t.Fatalf("the ask names nonce %q, want the settled meeting's %q", params.Nonce, nonceHex)
	}
	if params.Completeness != proto.CompletenessComplete {
		t.Fatalf("completeness = %q, want the runtime's own complete", params.Completeness)
	}
	if got := captureScreenText(t, params.Closing); !strings.Contains(got, "hello from the interval") {
		t.Fatalf("the closing screen reads %q, want the boundary's content", got)
	}
	if len(params.Opening.Lines) == 0 || len(params.Opening.Lines) != params.Opening.Rows {
		t.Fatalf("the opening snapshot is not the rectangle the screen is: %+v", params.Opening)
	}
	if len(params.Departed) != 0 {
		t.Fatalf("an interval whose output fit the screen departed %d rows, want none", len(params.Departed))
	}

	// Duplicate halves settle nothing twice: the record was handed over
	// exactly once, whatever the channels do afterwards.
	if _, err := svc.Call(ctx, proto.OpLifecycleComplete, completion); err != nil {
		t.Fatalf("duplicate completion: %v", err)
	}
	if err := svc.TestSightFence(id, fence, []byte("$ ")); err != nil {
		t.Fatalf("re-sight the fence: %v", err)
	}
	if n, _ := asker.captureAsks(); n != 1 {
		t.Fatalf("duplicate halves produced %d capture asks, want the one", n)
	}
}

// The ack is decoded and its answer is what the send path reports: a legal
// nothing-was-kept answer is not an error, and a refusal by the store's own
// rules arrives here as data, exactly as the schema names it.
func TestTheCaptureAckCarriesWhyNothingWasKept(t *testing.T) {
	asker := newRecordingAsker(false)
	svc := New(Options{
		Generation:            "gen-under-test",
		Spawner:               &lcSpawner{},
		Log:                   lcTestLog(),
		RendezvousExpireAfter: (&expiryTrigger{}).afterFunc,
	})
	t.Cleanup(svc.Close)

	ctx := host.WithConnection(context.Background(), asker)
	res, err := svc.spawn(ctx, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{
			Lane: "lane-under-test", Domain: "dom-under-test", Epoch: 7,
			Capability: strings.Repeat("ab", 32),
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	id := res.Entry.Session

	var fence sessionruntime.FenceNonce
	for i := range fence {
		fence[i] = byte(0x2c)
	}
	nonceHex := hex.EncodeToString(fence[:])
	completion, _ := json.Marshal(proto.LifecycleCompleteParams{
		Session:     id,
		Incarnation: proto.Incarnation{Session: id.Session, Generation: 1},
		Nonce:       nonceHex,
	})
	if _, err := svc.Call(ctx, proto.OpLifecycleComplete, completion); err != nil {
		t.Fatalf("lifecycle-complete: %v", err)
	}
	if err := svc.TestSightFence(id, fence, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}

	asker.awaitAsk(t)
	asker.mu.Lock()
	defer asker.mu.Unlock()
	if len(asker.acked) == 0 || asker.acked[len(asker.acked)-1] == nil {
		t.Fatal("the asker was handed no result to fill — the send path would never see the ack")
	}
}

// captureScreenText is the test's own reading of a wire screen: graphemes
// joined per row, spacer cells skipped.
func captureScreenText(t *testing.T, s proto.CaptureScreen) string {
	t.Helper()
	var sb strings.Builder
	for _, row := range s.Lines {
		for _, c := range row.Cells {
			if c.Width == 0 {
				continue
			}
			sb.WriteString(c.Text)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// The unfinished ask (nocx-2v80t.2.4): an open interval's first departure
// produces exactly one ask that DECLARES itself unfinished — state names
// it, and the raw frame carries neither a nonce nor a closing screen, the
// two things a settled record has and an open interval must not. The frame
// validates against the frozen params contract, so the discriminated
// addition is the wire's, not only the struct's.
func TestAnOpenIntervalAsksTheCoordinatorToCaptureItsKnownSoFar(t *testing.T) {
	asker := newRecordingAsker(true)
	svc := New(Options{
		Generation:            "gen-under-test",
		Spawner:               &lcSpawner{},
		Log:                   lcTestLog(),
		RendezvousExpireAfter: (&expiryTrigger{}).afterFunc,
	})
	t.Cleanup(svc.Close)

	ctx := host.WithConnection(context.Background(), asker)
	res, err := svc.spawn(ctx, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{
			Lane: "lane-under-test", Domain: "dom-under-test", Epoch: 7,
			Capability: strings.Repeat("ab", 32),
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	id := res.Entry.Session
	hs, err := svc.find(id)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	// The command still runs; no completion and no fence exist. Its output
	// pushes rows off the screen, and that alone is the trigger.
	if ingestErr := hs.runtime.Ingest([]byte(strings.Repeat("still running\r\n", 30))); ingestErr != nil {
		t.Fatalf("ingest: %v", ingestErr)
	}

	asker.awaitAsk(t)
	n, raws := asker.captureAsks()
	if n != 1 {
		t.Fatalf("the coordinator connection saw %d capture asks, want exactly one", n)
	}
	// The raw frame's own keys, not the struct's defaults: an unfinished
	// ask may not CARRY a nonce or a closing screen at all.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(raws[0], &raw); err != nil {
		t.Fatalf("decode the capture frame: %v", err)
	}
	if _, ok := raw["nonce"]; ok {
		t.Fatalf("the unfinished ask carries a nonce: %s", raws[0])
	}
	if _, ok := raw["closing"]; ok {
		t.Fatalf("the unfinished ask carries a closing screen: %s", raws[0])
	}
	if string(raw["state"]) != `"unfinished"` {
		t.Fatalf("the ask's state is %s, want \"unfinished\" — a frame indistinguishable from a settled one is the defect", raw["state"])
	}

	var params proto.CaptureParams
	if err := json.Unmarshal(raws[0], &params); err != nil {
		t.Fatalf("decode the capture params: %v", err)
	}
	if len(params.Departed) == 0 {
		t.Fatalf("the unfinished ask carries %d departed rows, want what had left the screen so far", len(params.Departed))
	}
	if params.Completeness != proto.CompletenessNoFence {
		t.Fatalf("completeness = %q, want noFence: no authenticated boundary exists", params.Completeness)
	}
	if err := validateCaptureFrame(captureContract(t), raws[0]); err != nil {
		t.Fatalf("the unfinished ask does not satisfy the frozen contract: %v", err)
	}
}
