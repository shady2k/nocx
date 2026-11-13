package catalog_test

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/operation/catalog"
)

func TestAnswerPhasePreservesRemainingMax(t *testing.T) {
	c := notify.NewManualClock()
	s := catalog.BeginWithClock(t.Context(), catalog.OpInternalAgentRun, catalog.Invocation{}, c)
	max := s.Declaration().Max.Duration()
	c.Advance(max - time.Minute)
	if !s.BeginAnswer() {
		t.Fatal("answer did not begin")
	}
	c.Advance(10 * time.Minute)
	if _, done := s.Outcome(); done {
		t.Fatal("max expired during answer")
	}
	s.EndAnswer()
	c.Advance(30 * time.Second)
	if !s.Complete() {
		t.Fatal("work inside remaining max failed")
	}
}

func TestAnswerPhaseResumesOnlyRemainingMax(t *testing.T) {
	c := notify.NewManualClock()
	s := catalog.BeginWithClock(t.Context(), catalog.OpInternalAgentRun, catalog.Invocation{}, c)
	s.StartWork()
	c.Advance(s.Declaration().Max.Duration() - time.Minute)
	if !s.BeginAnswer() {
		t.Fatal("answer refused")
	}
	c.Advance(10 * time.Minute)
	s.EndAnswer()
	if remaining := s.StartWork(); remaining != time.Minute {
		t.Fatalf("StartWork renewed max: %v", remaining)
	}
	c.Advance(time.Minute)
	if outcome, done := s.Outcome(); !done || outcome != catalog.OutcomeExpired {
		t.Fatalf("remaining max did not expire: %v %v", outcome, done)
	}
}
