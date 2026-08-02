package transport

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testIdentity(seq int) ProbeResultIdentity {
	return ProbeResultIdentity{
		Endpoint:           "host.example.com:22",
		HostKeyFingerprint: "SHA256:testfingerprint",
		Username:           "testuser",
		AuthPolicy:         "auto",
		Timestamp:          time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
	}
}

func altIdentity(seq int) ProbeResultIdentity {
	return ProbeResultIdentity{
		Endpoint:           "other.example.com:2222",
		HostKeyFingerprint: "SHA256:otherfp",
		Username:           "admin",
		AuthPolicy:         "password",
		Timestamp:          time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// Store and Lookup
// ---------------------------------------------------------------------------

func TestProbeResultStore_StoreAndLookup_ExactMatch(t *testing.T) {
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity:     testIdentity(0),
		Outcome:      OutcomeAccepted,
		Detail:       "ok",
		ProfileID:    "profile:1",
		CredentialID: "cred:1",
		ConfigMtime:  time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		GroupChain:   []string{"group:a", "group:b"},
	}
	s.Store(rec)

	got := s.Lookup(testIdentity(0), rec.ConfigMtime, rec.GroupChain)
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got.Outcome != OutcomeAccepted {
		t.Errorf("expected accepted, got %s", got.Outcome)
	}
	if got.Detail != "ok" {
		t.Errorf("expected detail ok, got %s", got.Detail)
	}
	if got.Identity.HostKeyFingerprint != "SHA256:testfingerprint" {
		t.Errorf("fingerprint mismatch: %s", got.Identity.HostKeyFingerprint)
	}
}

func TestProbeResultStore_Lookup_WrongEndpoint(t *testing.T) {
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity:    testIdentity(0),
		Outcome:     OutcomeAccepted,
		ConfigMtime: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		GroupChain:  []string{"g:1"},
	}
	s.Store(rec)

	// Same identity but different endpoint.
	id := testIdentity(0)
	id.Endpoint = "other.com:22"
	got := s.Lookup(id, rec.ConfigMtime, rec.GroupChain)
	if got != nil {
		t.Fatal("expected nil for different endpoint")
	}
}

func TestProbeResultStore_Lookup_WrongFingerprint(t *testing.T) {
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity:    testIdentity(0),
		Outcome:     OutcomeAccepted,
		ConfigMtime: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		GroupChain:  []string{"g:1"},
	}
	s.Store(rec)

	id := testIdentity(0)
	id.HostKeyFingerprint = "SHA256:different"
	got := s.Lookup(id, rec.ConfigMtime, rec.GroupChain)
	if got != nil {
		t.Fatal("expected nil for different host key fingerprint")
	}
}

func TestProbeResultStore_Lookup_WrongUsername(t *testing.T) {
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity:    testIdentity(0),
		Outcome:     OutcomeAccepted,
		ConfigMtime: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		GroupChain:  []string{"g:1"},
	}
	s.Store(rec)

	id := testIdentity(0)
	id.Username = "other"
	got := s.Lookup(id, rec.ConfigMtime, rec.GroupChain)
	if got != nil {
		t.Fatal("expected nil for different username")
	}
}

func TestProbeResultStore_Lookup_WrongAuthPolicy(t *testing.T) {
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity:    testIdentity(0),
		Outcome:     OutcomeAccepted,
		ConfigMtime: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		GroupChain:  []string{"g:1"},
	}
	s.Store(rec)

	id := testIdentity(0)
	id.AuthPolicy = "publicKey"
	got := s.Lookup(id, rec.ConfigMtime, rec.GroupChain)
	if got != nil {
		t.Fatal("expected nil for different auth policy")
	}
}

func TestProbeResultStore_Lookup_WrongTimestamp(t *testing.T) {
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity:    testIdentity(0),
		Outcome:     OutcomeAccepted,
		ConfigMtime: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		GroupChain:  []string{"g:1"},
	}
	s.Store(rec)

	id := testIdentity(0)
	id.Timestamp = id.Timestamp.Add(1 * time.Hour)
	got := s.Lookup(id, rec.ConfigMtime, rec.GroupChain)
	if got != nil {
		t.Fatal("expected nil for different timestamp")
	}
}

// ---------------------------------------------------------------------------
// Invalidation: config mtime
// ---------------------------------------------------------------------------

func TestProbeResultStore_Lookup_ConfigMtimeChanged(t *testing.T) {
	s := NewProbeResultStore()
	oldMtime := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	rec := ProbeResultRecord{
		Identity:    testIdentity(0),
		Outcome:     OutcomeAccepted,
		ConfigMtime: oldMtime,
		GroupChain:  []string{"g:1"},
	}
	s.Store(rec)

	// Same identity, same group chain, but config mtime has advanced.
	newMtime := time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC)
	got := s.Lookup(testIdentity(0), newMtime, []string{"g:1"})
	if got != nil {
		t.Fatal("expected nil when config mtime changed")
	}
}

func TestProbeResultStore_Lookup_ConfigMtimeUnchanged(t *testing.T) {
	s := NewProbeResultStore()
	mtime := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	rec := ProbeResultRecord{
		Identity:    testIdentity(0),
		Outcome:     OutcomeAccepted,
		ConfigMtime: mtime,
		GroupChain:  []string{"g:1"},
	}
	s.Store(rec)

	got := s.Lookup(testIdentity(0), mtime, []string{"g:1"})
	if got == nil {
		t.Fatal("expected match when config mtime unchanged")
	}
}

func TestProbeResultStore_Lookup_StoredWithoutMtime(t *testing.T) {
	// Zero ConfigMtime means "no invalidation tracking for config" —
	// lookup with any mtime still matches.
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity:   testIdentity(0),
		Outcome:    OutcomeAccepted,
		GroupChain: []string{"g:1"},
		// ConfigMtime is zero
	}
	s.Store(rec)

	got := s.Lookup(testIdentity(0), time.Date(2026, 7, 29, 99, 0, 0, 0, time.UTC), []string{"g:1"})
	if got == nil {
		t.Fatal("expected match when stored result has zero config mtime (no invalidation)")
	}
}

// ---------------------------------------------------------------------------
// Invalidation: group chain
// ---------------------------------------------------------------------------

func TestProbeResultStore_Lookup_GroupChainChanged(t *testing.T) {
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity:    testIdentity(0),
		Outcome:     OutcomeAccepted,
		ConfigMtime: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		GroupChain:  []string{"group:a", "group:b"},
	}
	s.Store(rec)

	got := s.Lookup(testIdentity(0), rec.ConfigMtime, []string{"group:a", "group:c"})
	if got != nil {
		t.Fatal("expected nil when group chain changed")
	}
}

func TestProbeResultStore_Lookup_GroupChainUnchanged(t *testing.T) {
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity:    testIdentity(0),
		Outcome:     OutcomeAccepted,
		ConfigMtime: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		GroupChain:  []string{"group:a", "group:b"},
	}
	s.Store(rec)

	got := s.Lookup(testIdentity(0), rec.ConfigMtime, []string{"group:a", "group:b"})
	if got == nil {
		t.Fatal("expected match when group chain unchanged")
	}
}

func TestProbeResultStore_Lookup_StoredWithoutGroupChain(t *testing.T) {
	// Nil GroupChain means "no invalidation tracking for group chain".
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity:    testIdentity(0),
		Outcome:     OutcomeAccepted,
		ConfigMtime: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
	}
	s.Store(rec)

	got := s.Lookup(testIdentity(0), rec.ConfigMtime, []string{"any", "chain"})
	if got == nil {
		t.Fatal("expected match when stored result has nil group chain (no invalidation)")
	}
}

// ---------------------------------------------------------------------------
// Store replaces existing result with matching identity
// ---------------------------------------------------------------------------

func TestProbeResultStore_Store_ReplacesExisting(t *testing.T) {
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity: testIdentity(0),
		Outcome:  OutcomeAccepted,
		Detail:   "first",
	}
	s.Store(rec)

	rec2 := ProbeResultRecord{
		Identity: testIdentity(0),
		Outcome:  OutcomeRejected,
		Detail:   "replacement",
	}
	s.Store(rec2)

	// Only one record stored (replaced).
	if s.Len() != 1 {
		t.Errorf("expected 1 record after replacement, got %d", s.Len())
	}

	got := s.Lookup(testIdentity(0), time.Time{}, nil)
	if got == nil {
		t.Fatal("expected match after replacement")
	}
	if got.Outcome != OutcomeRejected {
		t.Errorf("expected replaced outcome 'rejected', got %s", got.Outcome)
	}
	if got.Detail != "replacement" {
		t.Errorf("expected replaced detail, got %s", got.Detail)
	}
}

func TestProbeResultStore_Store_MultipleIdentities(t *testing.T) {
	s := NewProbeResultStore()
	s.Store(ProbeResultRecord{Identity: testIdentity(0), Outcome: OutcomeAccepted})
	s.Store(ProbeResultRecord{Identity: altIdentity(0), Outcome: OutcomeRejected})

	if s.Len() != 2 {
		t.Errorf("expected 2 records, got %d", s.Len())
	}

	got := s.Lookup(testIdentity(0), time.Time{}, nil)
	if got == nil || got.Outcome != OutcomeAccepted {
		t.Error("first identity should still be stored")
	}

	got2 := s.Lookup(altIdentity(0), time.Time{}, nil)
	if got2 == nil || got2.Outcome != OutcomeRejected {
		t.Error("second identity should still be stored")
	}
}

// ---------------------------------------------------------------------------
// InvalidateForProfile
// ---------------------------------------------------------------------------

func TestProbeResultStore_InvalidateForProfile(t *testing.T) {
	s := NewProbeResultStore()
	s.Store(ProbeResultRecord{
		Identity:  testIdentity(0),
		Outcome:   OutcomeAccepted,
		ProfileID: "profile:1",
	})
	s.Store(ProbeResultRecord{
		Identity:  altIdentity(0),
		Outcome:   OutcomeRejected,
		ProfileID: "profile:2",
	})

	s.InvalidateForProfile("profile:1")

	if s.Len() != 1 {
		t.Errorf("expected 1 record after invalidating profile:1, got %d", s.Len())
	}

	got := s.Lookup(testIdentity(0), time.Time{}, nil)
	if got != nil {
		t.Error("result for profile:1 should have been removed")
	}

	got2 := s.Lookup(altIdentity(0), time.Time{}, nil)
	if got2 == nil {
		t.Error("result for profile:2 should still exist")
	}
}

// ---------------------------------------------------------------------------
// InvalidateForCredential
// ---------------------------------------------------------------------------

func TestProbeResultStore_InvalidateForCredential(t *testing.T) {
	s := NewProbeResultStore()
	s.Store(ProbeResultRecord{
		Identity:     testIdentity(0),
		Outcome:      OutcomeAccepted,
		CredentialID: "cred:1",
	})
	s.Store(ProbeResultRecord{
		Identity:     altIdentity(0),
		Outcome:      OutcomeRejected,
		CredentialID: "cred:2",
	})

	s.InvalidateForCredential("cred:1")

	if s.Len() != 1 {
		t.Errorf("expected 1 record after invalidating cred:1, got %d", s.Len())
	}

	got := s.Lookup(testIdentity(0), time.Time{}, nil)
	if got != nil {
		t.Error("result for cred:1 should have been removed")
	}
}

// ---------------------------------------------------------------------------
// Clear
// ---------------------------------------------------------------------------

func TestProbeResultStore_Clear(t *testing.T) {
	s := NewProbeResultStore()
	s.Store(ProbeResultRecord{Identity: testIdentity(0), Outcome: OutcomeAccepted})
	s.Store(ProbeResultRecord{Identity: altIdentity(0), Outcome: OutcomeRejected})

	if s.Len() != 2 {
		t.Fatalf("expected 2 records before clear, got %d", s.Len())
	}

	s.Clear()

	if s.Len() != 0 {
		t.Errorf("expected 0 records after clear, got %d", s.Len())
	}

	if got := s.Lookup(testIdentity(0), time.Time{}, nil); got != nil {
		t.Error("lookup after clear should return nil")
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestProbeResultStore_List(t *testing.T) {
	s := NewProbeResultStore()
	s.Store(ProbeResultRecord{Identity: testIdentity(0), Outcome: OutcomeAccepted})
	s.Store(ProbeResultRecord{Identity: altIdentity(0), Outcome: OutcomeRejected})

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 records in list, got %d", len(list))
	}
}

// ---------------------------------------------------------------------------
// Export and Import
// ---------------------------------------------------------------------------

func TestProbeResultStore_ExportImport_RoundTrip(t *testing.T) {
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity: testIdentity(0),
		Outcome:  OutcomeAccepted,
		Detail:   "ok",
	}
	s.Store(rec)

	data, err := s.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	s2 := NewProbeResultStore()
	if err := s2.Import(data); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Records are present after import but NOT matchable (import sets
	// sentinel mtime/chain guards). Len shows they exist.
	if s2.Len() != 1 {
		t.Fatalf("expected 1 record after import, got %d", s2.Len())
	}

	// Lookup with real values must not match — imported records have
	// sentinel invalidation guards.
	got := s2.Lookup(testIdentity(0), time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC), []string{"g:1"})
	if got != nil {
		t.Fatal("expected nil — imported records must not be matchable")
	}
}

func TestProbeResultStore_ExportImport_Multiple(t *testing.T) {
	s := NewProbeResultStore()
	s.Store(ProbeResultRecord{Identity: testIdentity(0), Outcome: OutcomeAccepted})
	s.Store(ProbeResultRecord{Identity: altIdentity(0), Outcome: OutcomeRejected})

	data, err := s.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	s2 := NewProbeResultStore()
	if err := s2.Import(data); err != nil {
		t.Fatalf("Import: %v", err)
	}

	if s2.Len() != 2 {
		t.Fatalf("expected 2 records after import, got %d", s2.Len())
	}

	// Verify both records exist in the list, even though Lookup fails.
	list := s2.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 records in list, got %d", len(list))
	}

	// Lookup with any real values must fail — sentinel mtime/chain prevent it.
	got1 := s2.Lookup(testIdentity(0), time.Time{}, nil)
	if got1 != nil {
		t.Error("imported record must not be matchable")
	}
}

func TestProbeResultStore_Import_SkipsEmptyEndpoint(t *testing.T) {
	data := `[
		{"identity": {"endpoint": "h:22", "hostKeyFingerprint": "fp", "username": "u", "authPolicy": "auto", "timestamp": "2026-07-29T12:00:00Z"}, "outcome": "accepted"},
		{"identity": {"endpoint": "", "hostKeyFingerprint": "", "username": "", "authPolicy": "", "timestamp": "0001-01-01T00:00:00Z"}, "outcome": "accepted"},
		{"identity": {"endpoint": "o:99", "hostKeyFingerprint": "fp2", "username": "a", "authPolicy": "pw", "timestamp": "2026-07-29T13:00:00Z"}, "outcome": "rejected"}
	]`

	s := NewProbeResultStore()
	if err := s.Import([]byte(data)); err != nil {
		t.Fatalf("Import: %v", err)
	}

	if s.Len() != 2 {
		t.Fatalf("expected 2 records (skipping empty endpoint), got %d", s.Len())
	}
}

func TestProbeResultStore_Import_InvalidJSON(t *testing.T) {
	s := NewProbeResultStore()
	err := s.Import([]byte("{not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestProbeResultStore_Export_Empty(t *testing.T) {
	s := NewProbeResultStore()
	data, err := s.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	// json.MarshalIndent(nil, "", "  ") produces "null".
	// Accept either "null" or "[]" — both are valid JSON for a store
	// that has no records.
	if string(data) != "null" && string(data) != "[]" {
		t.Errorf("expected null or empty array for empty store, got %s", string(data))
	}
}

// ---------------------------------------------------------------------------
// Empty store behaviour
// ---------------------------------------------------------------------------

func TestProbeResultStore_Empty_ListAndLookup(t *testing.T) {
	s := NewProbeResultStore()
	if s.Len() != 0 {
		t.Errorf("expected empty store length 0, got %d", s.Len())
	}

	list := s.List()
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d items", len(list))
	}

	got := s.Lookup(testIdentity(0), time.Time{}, nil)
	if got != nil {
		t.Error("lookup on empty store should return nil")
	}
}

// ---------------------------------------------------------------------------
// Concurrency safety smoke test
// ---------------------------------------------------------------------------

func TestProbeResultStore_ConcurrentStoreAndLookup(t *testing.T) {
	s := NewProbeResultStore()
	done := make(chan struct{})

	// Writer goroutine.
	go func() {
		for i := 0; i < 100; i++ {
			id := testIdentity(i)
			id.Endpoint = "concurrent.test:22"
			s.Store(ProbeResultRecord{
				Identity: id,
				Outcome:  OutcomeAccepted,
			})
		}
		close(done)
	}()

	// Reader goroutine.
	for i := 0; i < 50; i++ {
		id := testIdentity(0)
		id.Endpoint = "concurrent.test:22"
		_ = s.Lookup(id, time.Time{}, nil)
	}

	<-done
	// No deadlock or panic is the pass condition.
	if s.Len() == 0 {
		t.Error("expected at least some records after concurrent store")
	}
}

func TestProbeResultStore_Import_RecordsNotMatchable(t *testing.T) {
	// Imported records have sentinel mtime and empty group chain, so a
	// Lookup with real values must NOT find them.
	s := NewProbeResultStore()
	rec := ProbeResultRecord{
		Identity: testIdentity(0),
		Outcome:  OutcomeAccepted,
		Detail:   "imported",
	}
	s.Store(rec) // Store directly — this record has zero mtime and nil chain

	data, _ := s.Export()

	s2 := NewProbeResultStore()
	if err := s2.Import(data); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// A real caller passes the actual config mtime and group chain.
	realMtime := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	realChain := []string{"group:a", "group:b"}

	// Lookup with real caller values should fail — imported records have
	// sentinel guards that won't match.
	got := s2.Lookup(testIdentity(0), realMtime, realChain)
	if got != nil {
		t.Fatal("expected nil for imported record looked up with real mtime/chain — stale evidence must not be reusable")
	}

	// Even lookup with zero mtime (no invalidation) should fail because
	// the sentinel mtime (Unix epoch+1ns) is non-zero, so the guard triggers.
	got2 := s2.Lookup(testIdentity(0), time.Time{}, nil)
	if got2 != nil {
		t.Fatal("expected nil for imported record looked up with zero mtime — sentinel mtime prevents match")
	}
}
