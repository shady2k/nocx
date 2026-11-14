package session

// The liveness notification's wire value (nocx-oalo1.4.4).
//
// This file is package session because the assertion is about the value
// notifyLiveness BOUNDS onto proto.SessionLiveness, and no shipped caller can
// produce a sub-millisecond round trip on demand: the number comes from a real
// probe's real answer.

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// notificationSink answers the other planes with nothing: this test asks only
// what a notification carried.
type notificationSink struct {
	notifications []proto.Notification
}

func (s *notificationSink) SendSessionData(proto.SessionFrame) error         { return nil }
func (s *notificationSink) SendLifecycleData(proto.SessionFrame) error       { return nil }
func (s *notificationSink) SendScreenFrame(proto.ScreenDataFrame) error      { return nil }
func (s *notificationSink) SendOutputRows(proto.OutputRowsFrame) error       { return nil }
func (s *notificationSink) SendIntervalEnd(proto.IntervalEndFrame) error     { return nil }
func (s *notificationSink) SendClearBoundary(proto.ClearBoundaryFrame) error { return nil }
func (s *notificationSink) SendEffectFrame(proto.EffectFrame) error          { return nil }

func (s *notificationSink) SendNotification(n proto.Notification) error {
	s.notifications = append(s.notifications, n)
	return nil
}

// TestAnAnsweredProbeShorterThanAMillisecondIsStillAMeasurement is the defect
// e2e/ssh-reconnect.spec.ts:312 met on 2026-11-14: "a host that is merely
// answering late is marked slow and offered nothing" watched a host be marked
// slow and then stay marked slow after the link improved, for a minute.
//
// The chain is entirely in the units. A loopback probe answers in a few
// hundred microseconds; proto.SessionLiveness.RoundTripMS is whole
// milliseconds and its own note reads zero as "unresponsive, or a first round
// with nothing yet to measure"; the coordinator's session.applyObservation
// keeps the PREVIOUS round trip when it reads zero, because an unanswered
// probe says nothing about how fast the host answers. So a truncated
// measurement is indistinguishable from no measurement, the stored 800 ms
// stands, and the grade — which only leaves slow below 300 ms — can never
// clear.
func TestAnAnsweredProbeShorterThanAMillisecondIsStillAMeasurement(t *testing.T) {
	svc := New(Options{
		Generation: "gen-under-test",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(svc.Close)
	sink := &notificationSink{}
	t.Cleanup(svc.Bind(sink))

	id := proto.HostSessionID{Generation: "g", Session: proto.SessionHex([16]byte{1})}
	svc.notifyLiveness(id, true, 400*time.Microsecond)

	if len(sink.notifications) != 1 {
		t.Fatalf("one answered probe produced %d notifications, want 1", len(sink.notifications))
	}
	live, ok := sink.notifications[0].Params.(proto.SessionLiveness)
	if !ok {
		t.Fatalf("the notification carried %T, want proto.SessionLiveness", sink.notifications[0].Params)
	}
	if !live.Responsive {
		t.Fatal("an answered probe was reported as unanswered")
	}
	if live.RoundTripMS <= 0 {
		t.Fatalf("an answered probe carries %d ms, which this wire reads as \"nothing measured one\": the grade it feeds can then never leave slow", live.RoundTripMS)
	}
}

// TestAnUnansweredProbeCarriesNoRoundTrip is the paired fact the zero sentinel
// exists for: silence has no duration, and the coordinator must keep the last
// real measurement rather than read "instant".
func TestAnUnansweredProbeCarriesNoRoundTrip(t *testing.T) {
	svc := New(Options{
		Generation: "gen-under-test",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(svc.Close)
	sink := &notificationSink{}
	t.Cleanup(svc.Bind(sink))

	svc.notifyLiveness(proto.HostSessionID{Generation: "g", Session: proto.SessionHex([16]byte{1})}, false, 0)

	if len(sink.notifications) != 1 {
		t.Fatalf("one unanswered probe produced %d notifications, want 1", len(sink.notifications))
	}
	live, ok := sink.notifications[0].Params.(proto.SessionLiveness)
	if !ok {
		t.Fatalf("the notification carried %T, want proto.SessionLiveness", sink.notifications[0].Params)
	}
	if live.Responsive {
		t.Fatal("an unanswered probe was reported as answered")
	}
	if live.RoundTripMS != 0 {
		t.Fatalf("silence carried %d ms, want the zero that means no measurement", live.RoundTripMS)
	}
}
