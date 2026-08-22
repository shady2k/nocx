package session

import (
	"context"
	"crypto/rand"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/sandbox"
)

func TestRealRegistry_ImplementsRegistry(t *testing.T) {
	var _ Registry = New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})
}

func TestRealSession_SandboxInfoReturnsDeepCopy(t *testing.T) {
	s := &realSession{
		sandboxInfo: &sandbox.SessionInfo{
			Backend:         sandbox.BackendLandlock,
			Workspace:       "/workspace",
			WritableRoots:   []string{"/workspace"},
			ReadOnlyRoots:   []string{"/usr"},
			HomeProjections: []sandbox.HomeProjection{{HostPath: "/workspace", RelativePath: "workspace"}},
		},
	}

	first := s.SandboxInfo()
	first.WritableRoots[0] = "/mutated"
	first.ReadOnlyRoots[0] = "/mutated-ro"
	first.HomeProjections[0].RelativePath = "mutated"
	second := s.SandboxInfo()
	if got := second.WritableRoots[0]; got != "/workspace" {
		t.Fatalf("second SandboxInfo root = %q, want immutable session metadata", got)
	}
	if got := second.ReadOnlyRoots[0]; got != "/usr" {
		t.Fatalf("second SandboxInfo read-only root = %q, want immutable session metadata", got)
	}
	if got := second.HomeProjections[0].RelativePath; got != "workspace" {
		t.Fatalf("second SandboxInfo projection = %q, want immutable session metadata", got)
	}
}

type sandboxPTYStub struct {
	*pty.Stub
	info *sandbox.SessionInfo
}

func (s *sandboxPTYStub) SandboxInfo() *sandbox.SessionInfo {
	return s.info.Clone()
}

func TestRealRegistry_SandboxCwdRemainsCanonicalUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(home, "workspace")
	logger := log.NewSlogAdapter(nil)
	pt := &sandboxPTYStub{
		Stub: pty.NewStub(logger),
		info: &sandbox.SessionInfo{
			Backend:         sandbox.BackendLandlock,
			Workspace:       workspace,
			WritableRoots:   []string{workspace},
			HomeProjections: []sandbox.HomeProjection{},
		},
	}
	reg := New(logger, &stubPTYFactory{stub: pt})
	sess, err := reg.Open(context.Background(), Config{
		Kind:    KindLocal,
		Cwd:     workspace,
		Cols:    80,
		Rows:    24,
		Sandbox: &sandbox.Request{Workspace: workspace},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if got := sess.Cwd(); got != workspace {
		t.Fatalf("Cwd = %q, want canonical workspace %q", got, workspace)
	}
}

func TestNewID_Is32HexChars(t *testing.T) {
	id := NewID()
	if len(string(id)) != 32 {
		t.Fatalf("expected 32 hex chars, got %d: %s", len(string(id)), id)
	}
	for _, c := range string(id) {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex character in id: %c in %s", c, id)
		}
	}
}

func TestIDConversionRoundTrip(t *testing.T) {
	var raw [16]byte
	_, _ = rand.Read(raw[:])

	id := IDFromBytes(raw)
	if len(string(id)) != 32 {
		t.Fatalf("IDFromBytes: expected 32 chars, got %d", len(string(id)))
	}

	back, err := IDToBytes(id)
	if err != nil {
		t.Fatalf("IDToBytes: %v", err)
	}
	if back != raw {
		t.Errorf("round-trip mismatch: %x != %x", back, raw)
	}
}

func TestIDToBytes_RejectsMalformed(t *testing.T) {
	tests := []string{
		"",
		"abc",                              // too short
		"gggggggggggggggggggggggggggggggg", // non-hex chars
		"abc123",                           // too short
	}
	for _, tc := range tests {
		_, err := IDToBytes(ID(tc))
		if err == nil {
			t.Errorf("expected error for %q", tc)
		}
	}
}

func TestNewID_GeneratesUnique(t *testing.T) {
	ids := make(map[ID]bool)
	for i := 0; i < 100; i++ {
		id := NewID()
		if ids[id] {
			t.Fatalf("duplicate id after %d iterations: %s", i, id)
		}
		ids[id] = true
	}
}

func TestRealRegistry_OpenAndClose(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := New(logger, &stubPTYFactory{stub: pty.NewStub(logger)})

	ctx := context.Background()
	sess, err := reg.Open(ctx, Config{
		Kind: KindLocal,
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if sess.ID() == "" {
		t.Fatal("session ID is empty")
	}
	if sess.Kind() != KindLocal {
		t.Fatalf("expected KindLocal, got %d", sess.Kind())
	}

	if len(reg.List()) != 1 {
		t.Fatalf("expected 1 session, got %d", len(reg.List()))
	}

	if closeErr := reg.Close(sess.ID()); closeErr != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Fatalf("expected 0 sessions after close, got %d", len(reg.List()))
	}
}

func TestRealRegistry_Get_NotFound(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})
	_, err := reg.Get("nonexistent1234567890123456")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestRealRegistry_CloseTwice_NoPanic(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindLocal,
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = reg.Close(sess.ID())
	if err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second close should not panic.
	_ = reg.Close(sess.ID())
}

func TestRealRegistry_RemoteKind_ReturnsError(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})
	_, err := reg.Open(context.Background(), Config{
		Kind: KindRemote,
		Cols: 80,
		Rows: 24,
	})
	if err == nil {
		t.Fatal("expected error for remote kind")
	}
}

type realPTYFactory struct{ log log.Logger }

func (f *realPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	return pty.NewLocal(f.log, cfg)
}

// capturePTYFactory records the pty.Config each Open passes to NewPTY — the
// seam that carries the session id to the lifecycle lane registration.
type capturePTYFactory struct {
	last pty.Config
}

func (f *capturePTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	f.last = cfg
	return pty.NewStub(log.NewSlogAdapter(nil)), nil
}

func TestOpenCarriesSessionIDToPTYFactory(t *testing.T) {
	f := &capturePTYFactory{}
	reg := New(log.NewSlogAdapter(nil), f)

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindLocal, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if f.last.SessionID != string(sess.ID()) {
		t.Fatalf("NewPTY received SessionID %q, want the session's own id %q", f.last.SessionID, sess.ID())
	}
}

func TestOpenBindsSandboxRequestToSessionIncarnation(t *testing.T) {
	f := &capturePTYFactory{}
	reg := New(log.NewSlogAdapter(nil), f)
	request := &sandbox.Request{Workspace: t.TempDir()}

	sess, err := reg.Open(t.Context(), Config{Kind: KindLocal, Cols: 80, Rows: 24, Sandbox: request})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()
	identity := sess.Identity()
	if f.last.Sandbox == request {
		t.Fatal("session mutated and forwarded the caller's sandbox request")
	}
	if got := f.last.Sandbox.Identity; got.SessionID != string(sess.ID()) || got.InstanceID != string(identity.InstanceID) || got.Epoch != identity.Epoch {
		t.Fatalf("sandbox identity = %#v, want session %q instance %q epoch %d", got, sess.ID(), identity.InstanceID, identity.Epoch)
	}
	if request.Identity != (sandbox.SessionIdentity{}) {
		t.Fatalf("caller request was mutated: %#v", request.Identity)
	}
}

func TestRealRegistry_DoneChannel(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &realPTYFactory{log: log.NewSlogAdapter(nil)})

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindLocal,
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Start the output pump so the shell's stdout does not block.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = sess.StartOutput(ctx, func(data []byte) error { return nil })
	}()
	// Write exit to the shell. No sleep needed: the PTY buffers stdin,
	// and StartOutput's goroutine drains stdout before the buffer fills.
	_, _ = sess.Write([]byte("exit\n"))

	select {
	case <-sess.Done():
		// PTY exited — success.
	case <-time.After(30 * time.Second):
		t.Fatal("Done channel never closed")
	}

	_ = reg.Close(sess.ID())
}

func TestRealRegistry_TwoOpensDifferentIDs(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})

	a, err := reg.Open(context.Background(), Config{Kind: KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer func() { _ = reg.Close(a.ID()) }()

	b, err := reg.Open(context.Background(), Config{Kind: KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = reg.Close(b.ID()) }()

	if a.ID() == b.ID() {
		t.Fatal("two open calls returned the same session id")
	}
	if len(reg.List()) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(reg.List()))
	}
}

func TestRealRegistry_StartOutput(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &realPTYFactory{log: log.NewSlogAdapter(nil)})

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindLocal,
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	outputCh := make(chan []byte, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = sess.StartOutput(ctx, func(data []byte) error {
			outputCh <- data
			return nil
		})
	}()

	// Write a command that produces output.
	_, _ = sess.Write([]byte("echo hello\n"))

	select {
	case data := <-outputCh:
		if len(data) == 0 {
			t.Fatal("expected non-empty output")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no output received")
	}
}

func TestRealRegistry_Resize(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &realPTYFactory{log: log.NewSlogAdapter(nil)})

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindLocal,
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	// Resize to new dimensions — should not error.
	err = sess.Resize(context.Background(), 100, 40, 0, 0)
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}
}

func TestRealRegistry_WriteToClosedSession(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindLocal,
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = reg.Close(sess.ID())

	// Write after close should not panic.
	_, _ = sess.Write([]byte("echo test\n"))
}

type stubPTYFactory struct{ stub pty.Pty }

func (f *stubPTYFactory) NewPTY(_ context.Context, _ pty.Config) (pty.Pty, error) {
	return f.stub, nil
}

// capturingPTYFactory records the pty.Config passed to NewPTY so tests can
// assert the session's Config threaded through correctly.
type capturingPTYFactory struct {
	stub *pty.Stub
	got  pty.Config
}

func (f *capturingPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	f.got = cfg
	return f.stub, nil
}

// TestOpenThreadsEnhancedIntoPTYConfig verifies that session.Config.Enhanced
// is threaded through to pty.Config.Enhanced (nocx-4ff.10).
func TestOpenThreadsEnhancedIntoPTYConfig(t *testing.T) {
	stub := pty.NewStub(log.NewSlogAdapter(nil))
	factory := &capturingPTYFactory{stub: stub}
	reg := New(log.NewSlogAdapter(nil), factory)

	_, err := reg.Open(context.Background(), Config{
		Kind:     KindLocal,
		Cols:     80,
		Rows:     24,
		Enhanced: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !factory.got.Enhanced {
		t.Fatalf("pty.Config.Enhanced = false, want true")
	}
}

// TestRegistry_OpenWithFakePTY proves a session can be opened against a stub
// PTY — no real process is spawned, and the registry is independently testable
// (DEFECT 10 / AD-8).
func TestRegistry_OpenWithFakePTY(t *testing.T) {
	stub := pty.NewStub(log.NewSlogAdapter(nil))
	factory := &stubPTYFactory{stub: stub}
	reg := New(log.NewSlogAdapter(nil), factory)

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindLocal,
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open with fake PTY: %v", err)
	}

	if sess.ID() == "" {
		t.Fatal("session ID is empty")
	}
	if len(reg.List()) != 1 {
		t.Fatalf("expected 1 session, got %d", len(reg.List()))
	}

	// Write should hit the stub (no real process).
	n, err := sess.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5 bytes written, got %d", n)
	}

	// Done should be open (stub not closed yet).
	select {
	case <-sess.Done():
		t.Fatal("Done should be open for a live stub")
	default:
	}

	// Close the session and verify the stub's Done is closed.
	_ = reg.Close(sess.ID())
	select {
	case <-sess.Done():
	default:
		t.Fatal("Done should be closed after Close()")
	}
	if len(reg.List()) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(reg.List()))
	}

	// StartOutput on a stub: Read returns EOF immediately, so the pump exits quickly.
	out := make(chan []byte, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess2, err := reg.Open(context.Background(), Config{Kind: KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("second Open with fake: %v", err)
	}
	_ = sess2.StartOutput(ctx, func(data []byte) error {
		out <- data
		return nil
	})

	select {
	case <-out:
		t.Fatal("stub Read returns EOF, should not produce output")
	case <-time.After(100 * time.Millisecond):
	}

	// Read on stub returns EOF.
	n, err = stub.Read(make([]byte, 100))
	if err != io.EOF || n != 0 {
		t.Errorf("expected EOF from stub Read, got err=%v n=%d", err, n)
	}
}

// stubUsageTracker records the profile IDs it sees, for testing
// ProfileUsageTracker integration.
type stubUsageTracker struct {
	opened []string
	closed []string
}

func (s *stubUsageTracker) SessionOpened(profileID string) {
	s.opened = append(s.opened, profileID)
}

func (s *stubUsageTracker) SessionClosed(profileID string) {
	s.closed = append(s.closed, profileID)
}

func (s *stubUsageTracker) LastUsedForProfiles(profileIDs []string) (map[string]time.Time, error) {
	return nil, nil
}

// TestSessionProfileID proves a session remembers the profile ID it was
// opened from (nocx-uxs5.4).
func TestSessionProfileID(t *testing.T) {
	stub := pty.NewStub(log.NewSlogAdapter(nil))
	reg := New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: stub})

	sess, err := reg.Open(context.Background(), Config{
		ProfileID: "ssh:my-host:a1",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if got := sess.ProfileID(); got != "ssh:my-host:a1" {
		t.Errorf("ProfileID() = %q, want %q", got, "ssh:my-host:a1")
	}
}

// TestProfileUsageTracker_OpenClosed verifies that a wired
// ProfileUsageTracker receives SessionOpened on Open and SessionClosed
// on Close.
func TestProfileUsageTracker_OpenClosed(t *testing.T) {
	stub := pty.NewStub(log.NewSlogAdapter(nil))
	tracker := &stubUsageTracker{}
	reg := New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: stub})
	reg = reg.WithProfileUsageTracker(tracker)

	sess, err := reg.Open(context.Background(), Config{
		ProfileID: "ssh:p1:1",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if len(tracker.opened) != 1 {
		t.Fatalf("expected 1 open call, got %d", len(tracker.opened))
	}
	if tracker.opened[0] != "ssh:p1:1" {
		t.Errorf("opened[0] = %q, want %q", tracker.opened[0], "ssh:p1:1")
	}

	if closeErr := reg.Close(sess.ID()); closeErr != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(tracker.closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(tracker.closed))
	}
	if tracker.closed[0] != "ssh:p1:1" {
		t.Errorf("closed[0] = %q, want %q", tracker.closed[0], "ssh:p1:1")
	}
}

// TestDeletedProfileSessionContinues proves that closing a session whose
// profile was "deleted" (tracker missing the profile) still works cleanly.
// The session must not strand: its registry entry is cleaned up and
// Close succeeds even when the tracker receives an unknown profile ID.
func TestDeletedProfileSessionContinues(t *testing.T) {
	stub := pty.NewStub(log.NewSlogAdapter(nil))
	tracker := &stubUsageTracker{}
	reg := New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: stub})
	reg = reg.WithProfileUsageTracker(tracker)

	// Open with a profile ID.
	sess, err := reg.Open(context.Background(), Config{
		ProfileID: "ssh:deleted-profile:1",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Simulate profile deletion: the tracker's behavior doesn't affect
	// session lifecycle — Open and Close must work regardless.
	// Close the session as if its profile was deleted.
	if closeErr := reg.Close(sess.ID()); closeErr != nil {
		t.Fatalf("Close after profile deletion: %v", err)
	}

	// Verify the session is removed from the registry.
	_, err = reg.Get(sess.ID())
	if err == nil {
		t.Error("expected error after close, got nil")
	}

	// The tracker still received Close for the deleted-profile ID.
	if len(tracker.closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(tracker.closed))
	}
	if tracker.closed[0] != "ssh:deleted-profile:1" {
		t.Errorf("closed[0] = %q, want %q", tracker.closed[0], "ssh:deleted-profile:1")
	}
}

// The acceptance test of the task (nocx-3oupk): a record naming a session
// from a previous backend instance does not resolve to a current session
// of the same id, and neither does a record naming an earlier epoch of
// this instance. The refusal compares id + instance + epoch together, so
// each field carries part of the rule.
func TestRecordFromPreviousInstanceDoesNotResolveToCurrentSession(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := New(logger, &stubPTYFactory{stub: pty.NewStub(logger)})

	ctx := context.Background()
	sess, err := reg.Open(ctx, Config{Kind: KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ident := sess.Identity()
	if ident.InstanceID != reg.instanceID {
		t.Fatalf("session instance = %q, want the registry's %q", ident.InstanceID, reg.instanceID)
	}

	// The positive: a record carrying this session's own identity resolves.
	if !ident.SameIncarnation(sess.ID(), sess) {
		t.Fatal("a record carrying the session's own identity must resolve to it")
	}

	// A record from a previous backend instance: same id, same epoch, a
	// different instance — the case the task names. Minted through the
	// same reader seam the registry uses, so it is a real instance id,
	// never the registry's own. Must NOT resolve.
	prevID, err := mintInstanceID(rand.Reader)
	if err != nil {
		t.Fatalf("mint previous instance id: %v", err)
	}
	previousInstance := Identity{InstanceID: prevID, Epoch: ident.Epoch}
	if previousInstance.SameIncarnation(sess.ID(), sess) {
		t.Fatal("a record from a previous backend instance resolved to a current session of the same id")
	}

	// An earlier epoch of THIS instance: same id, same instance, an earlier
	// incarnation — a restore reusing the id at a later epoch is a
	// different session. Must NOT resolve.
	earlierEpoch := Identity{InstanceID: ident.InstanceID, Epoch: ident.Epoch - 1}
	if earlierEpoch.SameIncarnation(sess.ID(), sess) {
		t.Fatal("a record from an earlier epoch of this instance resolved to the current session")
	}

	// A record that named the wrong session: same instance and epoch are
	// per-session facts, so this is a fabricated identity — the id is what
	// the record names, and it must agree too. Must NOT resolve.
	wrongID := Identity{InstanceID: ident.InstanceID, Epoch: ident.Epoch}
	if wrongID.SameIncarnation(ID("0123456789abcdef0123456789abcdef"), sess) {
		t.Fatal("a record naming a different session id resolved to this session")
	}
}

// Two registries are two backend instances: their identities are never
// equal, and each session carries its own registry's instance. This is the
// paired positive to the mint failure path — on an ordinary machine the
// mint succeeds and distinguishes.
func TestInstanceIdentity_DistinctAcrossRegistries(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	a := New(logger, &stubPTYFactory{stub: pty.NewStub(logger)})
	b := New(logger, &stubPTYFactory{stub: pty.NewStub(logger)})

	if a.instanceID == "" || len(string(a.instanceID)) != 32 {
		t.Fatalf("instance id = %q, want 32 hex chars", a.instanceID)
	}
	if a.instanceID == b.instanceID {
		t.Fatal("two backend instances must never be equal")
	}

	ctx := context.Background()
	sessA, err := a.Open(ctx, Config{Kind: KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if sessA.Identity().InstanceID != a.instanceID {
		t.Errorf("session instance = %q, want its registry's %q", sessA.Identity().InstanceID, a.instanceID)
	}
}

// The epoch is a per-session counter: fresh, monotonic, never reused — the
// rule internal/lifecycle states for its own epochs. A record's epoch is
// what distinguishes a later session that reuses an id from the
// incarnation the record names.
func TestEpochs_FreshMonotonicPerSession(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := New(logger, &stubPTYFactory{stub: pty.NewStub(logger)})

	ctx := context.Background()
	for want := uint64(1); want <= 3; want++ {
		sess, err := reg.Open(ctx, Config{Kind: KindLocal, Cols: 80, Rows: 24})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got := sess.Identity().Epoch; got != want {
			t.Fatalf("epoch = %d, want %d", got, want)
		}
	}
}

// errorReader fails every read — the representative of a crypto/rand
// failure at registry construction.
type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// Rule 3: the mint's external call (reading entropy) can fail, and a
// registry must not start with an instance identity that could equal
// another's. The failure is observable through the constructor New
// actually uses.
func TestNewReg_EntropyFailureRefusesRegistry(t *testing.T) {
	reg, err := newReg(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))}, errorReader{})
	if err == nil {
		t.Fatal("a registry with a failing entropy source must not be constructed")
	}
	if reg != nil {
		t.Fatalf("refused registry must be nil, got %+v", reg)
	}
}

// And on an ordinary machine it succeeds: the reader seam's positive half,
// driven with the same reader New uses.
func TestMintInstanceID_OrdinaryMachineSucceeds(t *testing.T) {
	id, err := mintInstanceID(rand.Reader)
	if err != nil {
		t.Fatalf("mint on crypto/rand must succeed: %v", err)
	}
	if len(string(id)) != 32 {
		t.Fatalf("instance id = %q, want 32 hex chars", id)
	}
	for _, c := range string(id) {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex character in instance id: %c in %s", c, id)
		}
	}
}
