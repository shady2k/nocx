package transport

// Round 10 (nocx-zg3k3.5.3): the helper's own end fact can arrive while the
// lane is still unregistered — the shell exits while the coordinator is
// away, the re-adopting process adopts the channel, and the domain_closed
// is ingested before anything routes it. The projection replay derives
// nothing for an already-closed domain, so the one-shot settle was lost and
// the entry ran on. The kernel still holds the domain's recorded terminal
// state; the session's open entry settles from it at the re-adopt boundary.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

func TestAnEndFactBeforeTheLaneRegistersSettlesAtTheBoundary(t *testing.T) {
	ctx := context.Background()
	logger := log.NewSlogAdapter(nil)
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	port := &lifecycleRecordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	const lane = lifecycle.LaneID("lane-settle-1")
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	// The shell establishes, then says goodbye — both BEFORE the lane
	// registers anywhere: the helper's own end fact, ingested on the
	// publisher with no listener.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecycle.Event{
		Kind: lifecycle.KindDomainClosed, DomainClosed: &lifecycle.DomainClosedEvent{},
	}))

	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()
	if envErr := led.EnsureEnvironment(ctx, content.Environment{ID: "local", Kind: content.EnvLocal}); envErr != nil {
		t.Fatalf("EnsureEnvironment: %v", envErr)
	}
	if _, obsErr := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "local", Confidence: "{}",
		Criticality: content.CriticalityRoutine, Payload: "{}",
	}); obsErr != nil {
		t.Fatalf("RecordObservation: %v", obsErr)
	}
	sid := "sess-settle-1"
	colour := "#000000"
	if _, wsErr := db.Layout().CreateWorkspace(ctx, content.Workspace{ID: "ws-settle", Name: "settle", Colour: &colour, Position: 0},
		content.Tab{ID: "tab-settle", WorkspaceID: "ws-settle", Position: 0, Layout: "column"},
		content.Pane{ID: "pane-settle", TabID: "tab-settle", Kind: "local", SizeShare: 1, Cwd: "/repo"}); wsErr != nil {
		t.Fatalf("CreateWorkspace: %v", wsErr)
	}
	if sessErr := led.CreateSession(ctx, content.Session{ID: sid, WorkspaceID: "ws-settle"}); sessErr != nil {
		t.Fatalf("CreateSession: %v", sessErr)
	}
	const attempt = "att-settle-1"
	if _, submitErr := led.Submit(ctx, content.SubmitEntry{
		ID: attempt, Client: "c1", EnvironmentID: "local", Kind: content.EntryShell,
		SessionID: &sid, Cwd: "/repo", Intent: "shell exits while the coordinator is away",
	}); submitErr != nil {
		t.Fatalf("Submit: %v", submitErr)
	}
	if _, startErr := led.StartExecution(ctx, content.StartExecution{EntryID: attempt}); startErr != nil {
		t.Fatalf("StartExecution: %v", startErr)
	}
	artifact := "00000000-0000-7000-8000-0000000000cc"
	if _, openErr := led.OpenBlockOutput(ctx, content.OpenBlockOutput{
		EntryID: attempt, ArtifactID: artifact,
	}); openErr != nil {
		t.Fatalf("OpenBlockOutput: %v", openErr)
	}
	if appendErr := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: attempt, ArtifactID: artifact, FromRow: 0,
		Rows: []emulator.Row{aStreamRow("R1"), aStreamRow("R2")},
	}); appendErr != nil {
		t.Fatalf("the first delivery: %v", appendErr)
	}

	ws := NewWSServer(logger, newRegWithStub(logger), WithContentDB(db), WithLifecyclePublisher(pub))
	// The lane registers — after the end fact was already ingested.
	ws.RegisterLifecycleLane(lane, session.ID(sid))

	// THE BOUNDARY: the re-adopt consults the adopted domain's recorded
	// terminal state and settles the session's open entry from it.
	ws.settleAdoptedTerminalDomains(session.ID(sid))

	// The entry is terminal and the artifact sealed: the helper's own end
	// report settled it, replayed from the kernel's recorded state.
	row, rowErr := led.Entry(ctx, attempt)
	if rowErr != nil {
		t.Fatalf("Entry: %v", rowErr)
	}
	if row == nil {
		t.Fatal("the entry vanished")
	}
	if row.Status != content.EntryUnknown {
		t.Fatalf("the entry settled %q, want unknown — the helper's end report replayed", row.Status)
	}
	art, artErr := led.Artifact(ctx, artifact)
	if artErr != nil {
		t.Fatalf("Artifact: %v", artErr)
	}
	var body []byte
	for _, c := range art.Chunks {
		body = append(body, c...)
	}
	if art.State != "sealed" {
		t.Fatalf("the rows artifact is %q, want sealed by the settle", art.State)
	}
	if got := len(strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")); got != 2 {
		t.Fatalf("the artifact holds %d rows, want the stored 2 sealed as they stood", got)
	}
}
