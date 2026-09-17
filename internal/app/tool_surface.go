package app

import (
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/toolendpoint"
)

// tools/list reaches tools.catalogue at about 1.6s on a healthy launch;
// five seconds leaves over three seconds for startup variance while still
// deciding the absence during the launch-owned staging interval.
const toolSurfaceDeadline = 5 * time.Second

type toolSurfaceFact struct {
	SessionID string
	Available bool
	Reason    string
}

type toolSurfaceMonitor struct {
	mu       sync.Mutex
	deadline time.Duration
	sink     func(toolSurfaceFact)
	pending  map[string]*time.Timer
	settled  map[string]struct{}
}

func newToolSurfaceMonitor(deadline time.Duration, sink func(toolSurfaceFact)) *toolSurfaceMonitor {
	return &toolSurfaceMonitor{
		deadline: deadline,
		sink:     sink,
		pending:  make(map[string]*time.Timer),
		settled:  make(map[string]struct{}),
	}
}

func (m *toolSurfaceMonitor) Observe(observation toolendpoint.Observation) {
	if m == nil || observation.SessionID == "" {
		return
	}
	switch observation.Kind {
	case toolendpoint.ObservationAdmitted:
		m.arm(observation.SessionID)
	case toolendpoint.ObservationCatalogue:
		m.finish(observation.SessionID, toolSurfaceFact{SessionID: observation.SessionID, Available: true})
	case toolendpoint.ObservationRefusal:
		reason := observation.Reason
		if reason == "" {
			reason = "the tool endpoint refused the launch"
		}
		m.finish(observation.SessionID, toolSurfaceFact{SessionID: observation.SessionID, Reason: reason})
	}
}

func (m *toolSurfaceMonitor) arm(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.settled[sessionID]; ok {
		return
	}
	if _, ok := m.pending[sessionID]; ok {
		return
	}
	m.pending[sessionID] = time.AfterFunc(m.deadline, func() {
		m.finish(sessionID, toolSurfaceFact{
			SessionID: sessionID,
			Reason:    "tools.catalogue did not arrive before the launch deadline",
		})
	})
}

func (m *toolSurfaceMonitor) finish(sessionID string, fact toolSurfaceFact) {
	m.mu.Lock()
	if _, ok := m.settled[sessionID]; ok {
		m.mu.Unlock()
		return
	}
	if timer := m.pending[sessionID]; timer != nil {
		timer.Stop()
		delete(m.pending, sessionID)
	}
	m.settled[sessionID] = struct{}{}
	sink := m.sink
	m.mu.Unlock()
	if sink != nil {
		sink(fact)
	}
}

func (m *toolSurfaceMonitor) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	for sessionID, timer := range m.pending {
		timer.Stop()
		delete(m.pending, sessionID)
	}
	m.mu.Unlock()
}
