package transport

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// stubUsageTracker implements session.ProfileUsageTracker in memory for tests.
type stubUsageTracker struct {
	mu     sync.Mutex
	opened map[string]time.Time
	closed map[string]time.Time
	data   map[string]time.Time
}

func newStubUsageTracker() *stubUsageTracker {
	return &stubUsageTracker{
		opened: make(map[string]time.Time),
		closed: make(map[string]time.Time),
		data:   make(map[string]time.Time),
	}
}

func (s *stubUsageTracker) SessionOpened(profileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.opened[profileID] = now
	s.data[profileID] = now
}

func (s *stubUsageTracker) SessionClosed(profileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.closed[profileID] = now
	s.data[profileID] = now
}

func (s *stubUsageTracker) LastUsedForProfiles(profileIDs []string) (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]time.Time, len(profileIDs))
	for _, pid := range profileIDs {
		if t, ok := s.data[pid]; ok {
			result[pid] = t
		}
	}
	return result, nil
}

// TestSessionsStatus_ReportsLiveAndLastUsed verifies that sessions.status
// returns the correct live/not-live state and last-used timestamps for a
// set of profile IDs.
func TestSessionsStatus_ReportsLiveAndLastUsed(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	tracker := newStubUsageTracker()
	reg = reg.WithProfileUsageTracker(tracker)

	ws := NewWSServer(logger, reg, WithProfileUsageStore(tracker))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	// Open a session with a profile ID directly on the registry.
	sess, err := reg.Open(ctx, session.Config{
		ProfileID: "ssh:live-profile:1",
	})
	if err != nil {
		t.Fatalf("registry.Open: %v", err)
	}
	defer reg.Close(sess.ID()) //nolint:errcheck

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "sessions.status", map[string]any{
		"profileIds": []string{"ssh:live-profile:1", "ssh:not-live:1"},
	})

	var result struct {
		Result sessionsStatusResult `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Live profile should report live=true.
	liveStatus, ok := result.Result.Statuses["ssh:live-profile:1"]
	if !ok {
		t.Fatal("expected status for ssh:live-profile:1")
	}
	if !liveStatus.Live {
		t.Error("expected live=true for open session")
	}
	if liveStatus.LastUsed == "" {
		t.Error("expected lastUsed to be set for open session")
	}

	// Not-live profile should report live=false.
	notLive, ok := result.Result.Statuses["ssh:not-live:1"]
	if !ok {
		t.Fatal("expected status for ssh:not-live:1")
	}
	if notLive.Live {
		t.Error("expected live=false for unopened session")
	}
}

// TestSessionsStatus_NoTracker verifies sessions.status works without
// a wired tracker — live state is still reported, lastUsed is absent.
func TestSessionsStatus_NoTracker(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)

	ws := NewWSServer(logger, reg) // no WithProfileUsageStore
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	sess, err := reg.Open(ctx, session.Config{
		ProfileID: "ssh:live-no-tracker:1",
	})
	if err != nil {
		t.Fatalf("registry.Open: %v", err)
	}
	defer reg.Close(sess.ID()) //nolint:errcheck

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "sessions.status", map[string]any{
		"profileIds": []string{"ssh:live-no-tracker:1"},
	})

	var result struct {
		Result sessionsStatusResult `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	st, ok := result.Result.Statuses["ssh:live-no-tracker:1"]
	if !ok {
		t.Fatal("expected status entry")
	}
	if !st.Live {
		t.Error("expected live=true")
	}
	if st.LastUsed != "" {
		t.Error("expected lastUsed empty when no tracker wired")
	}
}
