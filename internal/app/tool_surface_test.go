package app

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/toolendpoint"
)

func waitToolSurfaceFact(t *testing.T, facts <-chan toolSurfaceFact) toolSurfaceFact {
	t.Helper()
	select {
	case fact := <-facts:
		return fact
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool-surface fact")
		return toolSurfaceFact{}
	}
}

func TestToolSurfaceMonitorPublishesHealthyOnce(t *testing.T) {
	facts := make(chan toolSurfaceFact, 3)
	monitor := newToolSurfaceMonitor(20*time.Millisecond, func(fact toolSurfaceFact) { facts <- fact })
	defer monitor.Close()
	monitor.Observe(toolendpoint.Observation{SessionID: "session-1", Kind: toolendpoint.ObservationAdmitted})
	monitor.Observe(toolendpoint.Observation{SessionID: "session-1", Kind: toolendpoint.ObservationCatalogue})
	monitor.Observe(toolendpoint.Observation{SessionID: "session-1", Kind: toolendpoint.ObservationCatalogue})

	fact := waitToolSurfaceFact(t, facts)
	if !fact.Available || fact.SessionID != "session-1" {
		t.Fatalf("healthy fact = %+v", fact)
	}
	select {
	case extra := <-facts:
		t.Fatalf("unexpected second healthy fact = %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestToolSurfaceMonitorPublishesDeadlineFailure(t *testing.T) {
	facts := make(chan toolSurfaceFact, 1)
	monitor := newToolSurfaceMonitor(20*time.Millisecond, func(fact toolSurfaceFact) { facts <- fact })
	defer monitor.Close()
	monitor.Observe(toolendpoint.Observation{SessionID: "session-2", Kind: toolendpoint.ObservationAdmitted})

	fact := waitToolSurfaceFact(t, facts)
	if fact.Available || fact.SessionID != "session-2" {
		t.Fatalf("deadline fact = %+v", fact)
	}
	if fact.Reason != "tools.catalogue did not arrive before the launch deadline" {
		t.Fatalf("deadline reason = %q", fact.Reason)
	}
}

func TestToolSurfaceMonitorPreservesRefusalReason(t *testing.T) {
	facts := make(chan toolSurfaceFact, 1)
	monitor := newToolSurfaceMonitor(time.Hour, func(fact toolSurfaceFact) { facts <- fact })
	defer monitor.Close()
	monitor.Observe(toolendpoint.Observation{
		SessionID: "session-3",
		Kind:      toolendpoint.ObservationRefusal,
		Reason:    "session already has a worker caller",
	})

	fact := waitToolSurfaceFact(t, facts)
	if fact.Available || fact.Reason != "session already has a worker caller" {
		t.Fatalf("refusal fact = %+v", fact)
	}
}
