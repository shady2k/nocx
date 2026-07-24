package session

import (
	"context"
	"crypto/rand"
	"io"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
)

func TestRealRegistry_ImplementsRegistry(t *testing.T) {
	var _ Registry = New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})
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

	if err := reg.Close(sess.ID()); err != nil {
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

	time.Sleep(100 * time.Millisecond) // let shell initialise

	// Write exit to the shell.
	_, _ = sess.Write([]byte("exit\n"))

	select {
	case <-sess.Done():
		// PTY exited — success.
	case <-time.After(5 * time.Second):
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

type stubPTYFactory struct{ stub *pty.Stub }

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
