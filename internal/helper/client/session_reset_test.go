package client_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// fakeProcess is a helper-side process with no OS behind it: the test writes
// what the "shell" prints and reads what was typed into it. A real /bin/sh
// cannot produce half a megabyte of output at a moment the test can name, and
// the window's reclamation is the thing under test — so the producer is the
// test's own.
type fakeProcess struct {
	out    *io.PipeReader
	writes *io.PipeWriter
	done   chan struct{}

	mu    sync.Mutex
	typed []byte
}

func newFakeProcess() *fakeProcess {
	r, w := io.Pipe()
	return &fakeProcess{out: r, writes: w, done: make(chan struct{})}
}

func (p *fakeProcess) Read(b []byte) (int, error) { return p.out.Read(b) }

func (p *fakeProcess) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.typed = append(p.typed, b...)
	p.mu.Unlock()
	return len(b), nil
}

func (p *fakeProcess) Close() error {
	_ = p.writes.Close()
	return p.out.Close()
}

func (p *fakeProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error { return nil }
func (p *fakeProcess) Done() <-chan struct{}                                        { return p.done }

func (p *fakeProcess) WaitErr() (error, bool) { return nil, false }
func (p *fakeProcess) Pid() int               { return 4242 }
func (p *fakeProcess) Shell() string          { return "/bin/fake" }

func (p *fakeProcess) ForegroundProcessGroup() (int, error) { return 4242, nil }

// print is the shell printing. It blocks until the helper's pump has taken the
// bytes, which is what makes "the window has now seen this much" assertable
// without a sleep.
func (p *fakeProcess) print(b []byte) error {
	_, err := p.writes.Write(b)
	return err
}

type fakeSpawner struct{ proc *fakeProcess }

func (s *fakeSpawner) Spawn(session.SpawnRequest) (session.Process, error) { return s.proc, nil }

// hostedFakeShell is hostedSessions with the test owning the shell: the REAL
// session service, the REAL host, the REAL socket, and a process the test can
// make produce exactly as much output as the window's bound needs.
func hostedFakeShell(t *testing.T) (*client.Client, *fakeProcess) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	proc := newFakeProcess()
	svc := session.New(session.Options{
		Generation: "testhash",
		Spawner:    &fakeSpawner{proc: proc},
		Log:        log,
		Limits:     session.DefaultLimits(),
	})
	t.Cleanup(svc.Close)
	t.Cleanup(func() { _ = proc.Close() })

	conn := newFakeConn(func(in io.Reader, out io.Writer) int {
		h := hostFor(in, out, log)
		h.Register(svc)
		release := svc.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	})
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, proc
}

// AD-9's reset has two halves and only one of them was built. The helper emits
// EventSessionReset when a reader's cursor falls behind its bounded window and
// moves that subscriber's own cursors to the base; the coordinator dropped the
// notification on the floor, so its cursor stayed below the base, the next ack
// was refused as behind, and the client turned that refusal into EOF — a tab
// reporting a session ENDED while the helper still holds a live shell.
//
// What this asserts is the whole span, in the order the wire delivered it: the
// bytes that were in flight before the hole are still handed to the reader,
// the hole is STATED where it happened rather than only logged, the stream
// continues afterwards — which is only possible if the ack that follows was
// accepted — and the session is never reported as over.
func TestALiveResetIsAppliedAndTheSessionSurvivesIt(t *testing.T) {
	c, proc := hostedFakeShell(t)

	entry, err := c.Spawn(context.Background(), proto.SpawnParams{
		Cwd: "/", Cols: 80, Rows: 24, WindowBytes: 1, // clamped up to the helper's floor
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber: "0123456789abcdef0123456789abcdef",
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		},
		Offset: proto.StreamOffset(entry.Window.Base), Fresh: true, RequestWrite: true,
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = attached.Close() }()

	// Nobody is reading, so the subscriber's cursor stands still while the
	// window reclaims out from under it: five times the window's floor is
	// past the bound by a margin no scheduling order can close.
	chunk := bytes.Repeat([]byte("a"), 32*1024)
	for range 20 {
		if printErr := proc.print(chunk); printErr != nil {
			t.Fatalf("shell output: %v", printErr)
		}
	}
	const marker = "OUTPUT-AFTER-THE-HOLE"
	if printErr := proc.print([]byte(marker)); printErr != nil {
		t.Fatalf("shell output: %v", printErr)
	}

	read := make(chan []byte, 1)
	go func() {
		var seen []byte
		buf := make([]byte, 8*1024)
		for {
			n, readErr := attached.Read(buf)
			if n > 0 {
				seen = append(seen, buf[:n]...)
				if bytes.Contains(seen, []byte(marker)) {
					read <- seen
					return
				}
			}
			if readErr != nil {
				read <- seen
				return
			}
		}
	}()

	var seen []byte
	select {
	case seen = <-read:
	case <-time.After(20 * time.Second):
		t.Fatal("the reader never reached the output produced after the reset")
	}

	if !bytes.Contains(seen, []byte(marker)) {
		t.Fatalf("the stream stopped at the reset: the output produced after it never arrived.\ngot %d bytes ending %q",
			len(seen), tailOf(seen))
	}
	notice := bytes.Index(seen, []byte("[nocx]"))
	if notice < 0 {
		t.Fatalf("the hole was not stated to the reader: no reset notice in %d bytes of output.\nended %q",
			len(seen), tailOf(seen))
	}
	if at := bytes.Index(seen, []byte(marker)); at < notice {
		t.Fatalf("the reset was applied out of order: the notice is at %d and the post-reset output at %d", notice, at)
	}
	select {
	case <-attached.Done():
		t.Fatal("the attachment reported the session over: the helper still holds a live process")
	default:
	}
}

func tailOf(b []byte) string {
	if len(b) > 120 {
		return string(b[len(b)-120:])
	}
	return string(b)
}
