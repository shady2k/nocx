package diagnostics

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// testCtx is one logger and one context on the same buffer: the ctx carries
// the logger every log.From call reaches, and sl reads the same records back.
func attrOf(r logtest.Record, key string) (string, bool) {
	for _, a := range r.Attrs {
		if a.Key == key {
			return a.Value.String(), true
		}
	}
	return "", false
}

func testCtx(t *testing.T) (context.Context, *slog.Logger) {
	t.Helper()
	sl := logtest.Slog(t)
	ctx := log.WithLogger(context.Background(), log.NewSlogAdapter(sl))
	return ctx, sl
}

func TestAnOpenRecordsEveryStageWithItsDurationAndTheSameTrace(t *testing.T) {
	ctx, sl := testCtx(t)
	_ = sl
	rec := NewRecorder()
	ctx, open := rec.Begin(ctx, OpenInfo{Kind: "ssh", Key: "ssh:profile:one"})
	if open == nil {
		t.Fatal("Begin returned no trace")
	}
	end := Stage(ctx, StageDial)
	time.Sleep(5 * time.Millisecond)
	end(nil)
	end = Stage(ctx, StageSpawn)
	time.Sleep(5 * time.Millisecond)
	end(nil)
	open.Finish(nil)

	snap := rec.Snapshot()
	if len(snap.Recent) != 1 {
		t.Fatalf("recent opens = %d, want 1", len(snap.Recent))
	}
	got := snap.Recent[0]
	if got.Outcome != "opened" {
		t.Fatalf("outcome = %q, want opened", got.Outcome)
	}
	if len(got.Stages) != 2 || got.Stages[0].Name != StageDial || got.Stages[1].Name != StageSpawn {
		t.Fatalf("stages = %+v, want dial then spawn", got.Stages)
	}
	if got.Stages[0].Took <= 0 || got.Stages[1].Took <= 0 {
		t.Fatalf("stage durations = %v/%v, want both positive", got.Stages[0].Took, got.Stages[1].Took)
	}
	if got.Trace == "" || got.Trace == "00000000000000000000000000000000" {
		t.Fatalf("trace = %q, want a real trace id", got.Trace)
	}
	// Both stage lines and the outcome carry the same trace id.
	for _, wanted := range []string{"open stage", "open finished"} {
		if !logtest.WaitFor(ctx, time.Second, func(r logtest.Record) bool { return r.Message == wanted }) {
			t.Fatalf("no %q line", wanted)
		}
	}
	for _, r := range logtest.RecordsSlog(sl) {
		if r.Message == "open stage" || r.Message == "open finished" {
			if got, _ := attrOf(r, "trace_id"); got != snap.Recent[0].Trace {
				t.Fatalf("%q carried trace_id %q, want %q", r.Message, got, snap.Recent[0].Trace)
			}
		}
	}
}

func TestAPendingOpenSaysWhichStageItIsInAtABoundedCadence(t *testing.T) {
	ctx, sl := testCtx(t)
	_ = sl
	rec := NewRecorder()
	ctx, open := rec.Begin(ctx, OpenInfo{Kind: "ssh", Key: "ssh:profile:pending"})
	defer open.Finish(context.Canceled)
	_ = Stage(ctx, StageSpawn) // never left: the call is parked here
	if !logtest.WaitFor(ctx, 7*time.Second, func(r logtest.Record) bool {
		if r.Message != "open pending" {
			return false
		}
		stage, _ := attrOf(r, "stage")
		return stage == StageSpawn
	}) {
		t.Fatal("no pending line naming the stage the open is parked in")
	}
	snap := rec.Snapshot()
	if len(snap.Live) != 1 {
		t.Fatalf("live opens = %d, want 1", len(snap.Live))
	}
	if snap.Live[0].Current != StageSpawn {
		t.Fatalf("current stage = %q, want %q", snap.Live[0].Current, StageSpawn)
	}
	if !strings.Contains(Render(snap, Facts{ServerStarted: time.Now()}), "stage=spawn") {
		t.Fatalf("the text does not name the stage:\n%s", Render(snap, Facts{ServerStarted: time.Now()}))
	}
}

func TestAClosedLifecycleChannelNamesWhoClosedIt(t *testing.T) {
	ctx, sl := testCtx(t)
	_ = sl
	rec := NewRecorder()
	ctx, open := rec.Begin(ctx, OpenInfo{Kind: "ssh", Key: "ssh:profile:closed"})
	_ = Stage(ctx, StageLifecycle)
	NoteLifecycleClosed(ctx, "open-rollback:spawn")
	NotePublish(ctx, "failed:connection-failed")
	open.Finish(context.Canceled)

	text := Render(rec.Snapshot(), Facts{ServerStarted: time.Now()})
	for _, want := range []string{
		"outcome=failed", "lifecycle_closed_by=open-rollback:spawn", "publish=failed:connection-failed",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q:\n%s", want, text)
		}
	}
}

func TestTheTextNeverCarriesAHostUserAddressOrPath(t *testing.T) {
	ctx, sl := testCtx(t)
	_ = sl
	rec := NewRecorder()
	const (
		host = "db-7.internal.example"
		user = "alice"
		path = "/home/alice/.ssh/id_ed25519"
	)
	_, open := rec.Begin(ctx, OpenInfo{Kind: "ssh", Key: user + "@" + host + ":" + path})
	open.Finish(nil)
	rec.RecordRefusal(time.Now(), "open", "control", []Holder{
		{Method: "open", Request: "1/open#4", Since: time.Now().Add(-time.Second)},
	})
	text := Render(rec.Snapshot(), Facts{
		ServerStarted: time.Now().Add(-time.Hour),
		Helper:        HelperStatus{Known: true, Connected: true, ConnectedFor: time.Minute},
		LogBytes:      42,
		LogKnown:      true,
	})
	for _, forbidden := range []string{host, user, path, "id_ed25519", "@"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the text carries %q:\n%s", forbidden, text)
		}
	}
	if !strings.Contains(text, "connection 1") {
		t.Fatalf("the text does not carry the stable per-run label:\n%s", text)
	}
}

func TestTheRecordIsBounded(t *testing.T) {
	ctx, sl := testCtx(t)
	_ = sl
	rec := NewRecorder()
	for i := 0; i < liveOpenCap+10; i++ {
		_, open := rec.Begin(ctx, OpenInfo{Kind: "ssh", Key: "ssh:profile:bounded"})
		open.Finish(nil)
	}
	snap := rec.Snapshot()
	if len(snap.Live) > liveOpenCap {
		t.Fatalf("live opens = %d, want <= %d", len(snap.Live), liveOpenCap)
	}
	for i := 0; i < recentOpenCap+10; i++ {
		rec.RecordRefusal(time.Now(), "open", "control", nil)
	}
	if got := len(rec.Snapshot().Refusals); got > refusalCap {
		t.Fatalf("refusals = %d, want <= %d", got, refusalCap)
	}
}

func TestARefusalNamesTheScopeItsHoldersAndHowLongTheyHeldIt(t *testing.T) {
	rec := NewRecorder()
	since := time.Now().Add(-3 * time.Second)
	rec.RecordRefusal(time.Now(), "sessions.live", "control", []Holder{
		{Method: "open", Request: "1/open#4", Trace: "abc", Since: since},
	})
	text := Render(rec.Snapshot(), Facts{ServerStarted: time.Now()})
	for _, want := range []string{
		"method=sessions.live", "scope=control", "holders=open(1/open#4)", "held=3",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q:\n%s", want, text)
		}
	}
}

// The overrun is a protocol violation, not a refusal: the text must say the
// method, the class and the two counts, and must be bounded like the rest of
// the record.
func TestACreditOverrunNamesTheMethodTheClassAndBothCounts(t *testing.T) {
	rec := NewRecorder()
	rec.RecordCreditOverrun(time.Now(), CreditOverrun{
		Conn: 7, Method: "profiles.list", Class: "ordinary", InFlight: 3, Window: 2,
	})
	text := Render(rec.Snapshot(), Facts{ServerStarted: time.Now()})
	for _, want := range []string{
		"credit overruns: 1", "conn=7", "method=profiles.list",
		"class=ordinary", "in_flight=3", "window=2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q:\n%s", want, text)
		}
	}
	for i := 0; i < creditOverrunCap+10; i++ {
		rec.RecordCreditOverrun(time.Now(), CreditOverrun{Method: "files.list", Class: "ordinary"})
	}
	if got := len(rec.Snapshot().CreditOverruns); got > creditOverrunCap {
		t.Fatalf("credit overruns = %d, want <= %d", got, creditOverrunCap)
	}
}

func TestNoCreditOverrunSaysSo(t *testing.T) {
	text := Render(NewRecorder().Snapshot(), Facts{ServerStarted: time.Now()})
	if !strings.Contains(text, "credit overruns: none") {
		t.Fatalf("text lacks the empty overrun line:\n%s", text)
	}
}

// nocx-q9od5: Copy diagnostics said a 1.6 s open took 22.8 s a minute later.
// The recent arm read the clock instead of the finish, so every finished
// record grew for as long as the process lived — and the record is the
// instrument the owner's report is read with.
func TestAFinishedOpenKeepsTheDurationItTook(t *testing.T) {
	ctx, _ := testCtx(t)
	rec := NewRecorder()
	clock := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	rec.now = func() time.Time { return clock }

	ctx, open := rec.Begin(ctx, OpenInfo{Kind: "ssh", Key: "ssh:profile:one"})
	clock = clock.Add(1500 * time.Millisecond)
	end := Stage(ctx, StageInstall)
	clock = clock.Add(100 * time.Millisecond)
	end(nil)
	open.Finish(nil)

	// THE PROCESS LIVES ON, which is what used to inflate the record.
	clock = clock.Add(20 * time.Second)
	snap := rec.Snapshot()
	if len(snap.Recent) != 1 {
		t.Fatalf("recent opens = %d, want 1", len(snap.Recent))
	}
	if got := snap.Recent[0].Elapsed; got != 1600*time.Millisecond {
		t.Fatalf("a finished open's duration = %v, want the 1.6s it took (the clock moved 20s on)", got)
	}
	text := Render(snap, Facts{})
	if !strings.Contains(text, "duration=1600ms") {
		t.Fatalf("the text does not state the open's own duration:\n%s", text)
	}
	if strings.Contains(text, "duration=21.") {
		t.Fatalf("the text states a duration the open did not take:\n%s", text)
	}
}

// The last step of an open is the session's first prompt, and it happens
// after the open returns. The record carries it for exactly that reason: the
// owner's 20 to 30 silent seconds were that interval (nocx-q9od5).
func TestAnOpensFirstPromptIsRecordedAfterItFinished(t *testing.T) {
	ctx, _ := testCtx(t)
	rec := NewRecorder()
	clock := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	rec.now = func() time.Time { return clock }

	ctx, open := rec.Begin(ctx, OpenInfo{Kind: "ssh", Key: "ssh:profile:one"})
	clock = clock.Add(2 * time.Second)
	open.Finish(nil)
	// The far shell's prompt arrives two seconds after the open was adopted.
	clock = clock.Add(2 * time.Second)
	NoteFirstPrompt(ctx)
	// And a later prompt_ready on the same session does not move it.
	clock = clock.Add(30 * time.Second)
	NoteFirstPrompt(ctx)

	snap := rec.Snapshot()
	if len(snap.Recent) != 1 {
		t.Fatalf("recent opens = %d, want 1", len(snap.Recent))
	}
	if got := snap.Recent[0].FirstPrompt; got != 4*time.Second {
		t.Fatalf("first prompt = %v, want 4s from the open's start", got)
	}
	if got := snap.Recent[0].Elapsed; got != 2*time.Second {
		t.Fatalf("elapsed = %v, want the open's own 2s", got)
	}
	text := Render(snap, Facts{})
	if !strings.Contains(text, "first_prompt=4000ms") {
		t.Fatalf("the text does not state when the prompt came:\n%s", text)
	}

	// An open that never saw a prompt says nothing about one.
	clock = clock.Add(time.Second)
	ctx2, open2 := rec.Begin(ctx, OpenInfo{Kind: "local", Key: "local"})
	open2.Finish(nil)
	snap = rec.Snapshot()
	for _, view := range snap.Recent {
		if view.Kind == "local" && view.FirstPrompt != 0 {
			t.Fatalf("a local open claims a first prompt: %+v", view)
		}
	}
	_ = ctx2
}
