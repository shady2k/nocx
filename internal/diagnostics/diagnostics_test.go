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
