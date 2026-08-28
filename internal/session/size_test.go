package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/ssh"
)

// recordingPty is a pty.Pty that remembers the size it was created at and
// every Resize it is asked for. It exists for the AD-1 assertion: "created
// at its final size, never spawned-then-resized" is a claim about the
// creation size AND about the absence of a resize, and only something
// counting both can report it.
type recordingPty struct {
	created Size

	mu      sync.Mutex
	resizes []Size
	// resizeErr, when set, is what Resize answers — the failure path for
	// the one external call this channel makes.
	resizeErr error

	done chan struct{}
}

func (p *recordingPty) Read([]byte) (int, error)    { <-p.done; return 0, errors.New("closed") }
func (p *recordingPty) Write(b []byte) (int, error) { return len(b), nil }

func (p *recordingPty) Close() error {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}

func (p *recordingPty) Resize(_ context.Context, cols, rows, xpixel, ypixel uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resizeErr != nil {
		return p.resizeErr
	}
	p.resizes = append(p.resizes, Size{Cols: cols, Rows: rows, XPixel: xpixel, YPixel: ypixel})
	return nil
}

func (p *recordingPty) Done() <-chan struct{} { return p.done }

func (p *recordingPty) resizeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.resizes)
}

func (p *recordingPty) lastResize() (Size, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.resizes) == 0 {
		return Size{}, false
	}
	return p.resizes[len(p.resizes)-1], true
}

// recordingPtyFactory hands out recordingPtys and keeps the last one, plus
// the pty.Config the registry built for it.
type recordingPtyFactory struct {
	last    *recordingPty
	lastCfg pty.Config
	// err, when set, is what NewPTY answers: the failure of the one
	// external call the local open makes.
	err       error
	resizeErr error
}

func (f *recordingPtyFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastCfg = cfg
	f.last = &recordingPty{
		created:   Size{Cols: cfg.Cols, Rows: cfg.Rows, XPixel: cfg.XPixel, YPixel: cfg.YPixel},
		resizeErr: f.resizeErr,
		done:      make(chan struct{}),
	}
	return f.last, nil
}

// recordingSSHFactory answers Connect with a stub channel and records the
// ConnectConfig the options build up to — the remote half of "the channel
// is created at its final size".
type recordingSSHFactory struct {
	cfg ssh.ConnectConfig
	err error
}

func (f *recordingSSHFactory) Connect(_ context.Context, _ string, opts ...ssh.ConnectOption) (ssh.Channel, error) {
	if f.err != nil {
		return nil, f.err
	}
	var cfg ssh.ConnectConfig
	for _, o := range opts {
		o(&cfg)
	}
	f.cfg = cfg
	return ssh.NewStubChannel(log.NewSlogAdapter(nil)), nil
}

// ── the no-client default ──────────────────────────────────────────────

// A session opened with no client attached has a size — the named default —
// and a channel that WORKS at it. The shell is asked what geometry it is
// running in, so what is asserted is the kernel's own winsize on the far
// side of a real PTY, not a number the registry kept in a field.
func TestOpen_NoClientAttached_RunsAtTheDefaultSizeWithAWorkingChannel(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &realPTYFactory{log: log.NewSlogAdapter(nil)})

	sess, err := reg.Open(context.Background(), Config{Kind: KindLocal})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if got := sess.EffectiveSize(); got != DefaultSize() {
		t.Fatalf("EffectiveSize = %+v, want the default %+v", got, DefaultSize())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan []byte, 64)
	go func() {
		_ = sess.StartOutput(ctx, func(data []byte) error {
			buf := make([]byte, len(data))
			copy(buf, data)
			select {
			case out <- buf:
			default:
			}
			return nil
		})
	}()

	// `stty size` prints "<rows> <cols>" for the terminal it is attached to.
	// A marker either side keeps the assertion off the echoed command line
	// and off the prompt.
	if _, werr := sess.Write([]byte("echo NOCX-$(stty size | tr ' ' 'x')-END\n")); werr != nil {
		t.Fatalf("Write: %v", werr)
	}

	want := "NOCX-24x80-END"
	deadline := time.After(30 * time.Second)
	var seen strings.Builder
	for {
		select {
		case chunk := <-out:
			seen.Write(chunk)
			// The echoed command line contains the substitution, not its
			// result, so only the shell's own answer can match.
			if strings.Contains(seen.String(), want) {
				return
			}
		case <-deadline:
			t.Fatalf("the shell never reported %q; saw:\n%s", want, seen.String())
		}
	}
}

// The remote half of the same question: with no client attached, the SSH
// channel is opened at the default too, rather than at whatever
// internal/ssh would have fallen back to on its own.
func TestOpen_NoClientAttached_RemoteChannelOpensAtTheDefault(t *testing.T) {
	f := &recordingSSHFactory{}
	reg := New(log.NewSlogAdapter(nil), &recordingPtyFactory{}).WithSSHFactory(f)

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindRemote, Host: "example", Remote: &ssh.ConnectConfig{User: "u"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	got := Size{Cols: f.cfg.Cols, Rows: f.cfg.Rows, XPixel: f.cfg.XPixel, YPixel: f.cfg.YPixel}
	if got != DefaultSize() {
		t.Fatalf("ssh channel opened at %+v, want the default %+v", got, DefaultSize())
	}
	if sess.EffectiveSize() != DefaultSize() {
		t.Fatalf("EffectiveSize = %+v, want %+v", sess.EffectiveSize(), DefaultSize())
	}
}

// ── the client reports, the backend sets ───────────────────────────────

// A client attaching reports its geometry, and the size the channel is
// created at is the one the BACKEND concluded — read off the session, not
// off the caller's config.
func TestOpen_ClientReports_TheBackendSetsTheChannelSize(t *testing.T) {
	f := &recordingPtyFactory{}
	reg := New(log.NewSlogAdapter(nil), f)

	reported := Size{Cols: 132, Rows: 43, XPixel: 1320, YPixel: 860}
	sess, err := reg.Open(context.Background(), Config{
		Kind: KindLocal,
		Cols: reported.Cols, Rows: reported.Rows,
		XPixel: reported.XPixel, YPixel: reported.YPixel,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if got := sess.EffectiveSize(); got != reported {
		t.Fatalf("EffectiveSize = %+v, want the reported %+v", got, reported)
	}
	if got := f.last.created; got != sess.EffectiveSize() {
		t.Fatalf("channel created at %+v, want the session's effective size %+v", got, sess.EffectiveSize())
	}
}

func TestOpen_ClientReports_RemoteChannelOpensAtTheEffectiveSize(t *testing.T) {
	sshf := &recordingSSHFactory{}
	reg := New(log.NewSlogAdapter(nil), &recordingPtyFactory{}).WithSSHFactory(sshf)

	reported := Size{Cols: 100, Rows: 30, XPixel: 800, YPixel: 600}
	sess, err := reg.Open(context.Background(), Config{
		Kind: KindRemote, Host: "example", Remote: &ssh.ConnectConfig{User: "u"},
		Cols: reported.Cols, Rows: reported.Rows,
		XPixel: reported.XPixel, YPixel: reported.YPixel,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	got := Size{Cols: sshf.cfg.Cols, Rows: sshf.cfg.Rows, XPixel: sshf.cfg.XPixel, YPixel: sshf.cfg.YPixel}
	if got != sess.EffectiveSize() {
		t.Fatalf("ssh channel opened at %+v, want the session's effective size %+v", got, sess.EffectiveSize())
	}
}

// A report the backend cannot use is not an error and does not reach the
// channel: the session runs at the default instead. This is the same arm as
// "no client attached", asked through a client that sent nonsense.
func TestOpen_UnusableClientReport_FallsBackToTheDefault(t *testing.T) {
	f := &recordingPtyFactory{}
	reg := New(log.NewSlogAdapter(nil), f)

	sess, err := reg.Open(context.Background(), Config{Kind: KindLocal, Cols: 120, Rows: 0})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if got := sess.EffectiveSize(); got != DefaultSize() {
		t.Fatalf("EffectiveSize = %+v, want the default %+v", got, DefaultSize())
	}
	if f.last.created != DefaultSize() {
		t.Fatalf("channel created at %+v, want the default %+v", f.last.created, DefaultSize())
	}
}

// ── AD-1: created at its final size, never spawned-then-resized ────────

func TestOpen_ChannelIsCreatedAtItsFinalSize_AndNeverResized(t *testing.T) {
	f := &recordingPtyFactory{}
	reg := New(log.NewSlogAdapter(nil), f)

	sess, err := reg.Open(context.Background(), Config{Kind: KindLocal, Cols: 90, Rows: 25})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if f.last.created != (Size{Cols: 90, Rows: 25}) {
		t.Fatalf("channel created at %+v, want 90x25", f.last.created)
	}
	if n := f.last.resizeCount(); n != 0 {
		t.Fatalf("the channel was resized %d times during open; AD-1 says it is created at its final size", n)
	}
}

// The same assertion for the session that has no client: the default is a
// creation size, not a resize applied after a 0x0 spawn.
func TestOpen_NoClientAttached_ChannelIsNotSpawnedThenResized(t *testing.T) {
	f := &recordingPtyFactory{}
	reg := New(log.NewSlogAdapter(nil), f)

	sess, err := reg.Open(context.Background(), Config{Kind: KindLocal})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if f.last.created != DefaultSize() {
		t.Fatalf("channel created at %+v, want the default %+v", f.last.created, DefaultSize())
	}
	if n := f.last.resizeCount(); n != 0 {
		t.Fatalf("the channel was resized %d times during open; AD-1 says it is created at its final size", n)
	}
}

// ── resize is the same decision, asked again ───────────────────────────

func TestResize_TheBackendDecides(t *testing.T) {
	f := &recordingPtyFactory{}
	reg := New(log.NewSlogAdapter(nil), f)

	sess, err := reg.Open(context.Background(), Config{Kind: KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	reported := Size{Cols: 100, Rows: 40, XPixel: 1000, YPixel: 800}
	if rerr := sess.Resize(context.Background(), reported); rerr != nil {
		t.Fatalf("Resize: %v", rerr)
	}
	if got := sess.EffectiveSize(); got != reported {
		t.Fatalf("EffectiveSize = %+v after a resize to %+v", got, reported)
	}
	applied, ok := f.last.lastResize()
	if !ok || applied != reported {
		t.Fatalf("channel resized to %+v (seen=%v), want %+v", applied, ok, reported)
	}
}

// A resize carrying no usable geometry — the shape a session whose last
// client has gone away is left in — puts the session back on the default
// rather than on 0x0.
func TestResize_UnusableReport_FallsBackToTheDefault(t *testing.T) {
	f := &recordingPtyFactory{}
	reg := New(log.NewSlogAdapter(nil), f)

	sess, err := reg.Open(context.Background(), Config{Kind: KindLocal, Cols: 100, Rows: 40})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if rerr := sess.Resize(context.Background(), Size{}); rerr != nil {
		t.Fatalf("Resize: %v", rerr)
	}
	if got := sess.EffectiveSize(); got != DefaultSize() {
		t.Fatalf("EffectiveSize = %+v, want the default %+v", got, DefaultSize())
	}
	applied, ok := f.last.lastResize()
	if !ok || applied != DefaultSize() {
		t.Fatalf("channel resized to %+v (seen=%v), want the default %+v", applied, ok, DefaultSize())
	}
}

// ── failure paths, each paired with its success ────────────────────────

// The one external call a local open makes. It fails: the open fails, and
// the registry holds nothing — no session, and so no size.
func TestOpen_PTYFactoryFails_NothingIsRegistered(t *testing.T) {
	boom := errors.New("no pty available")
	reg := New(log.NewSlogAdapter(nil), &recordingPtyFactory{err: boom})

	_, err := reg.Open(context.Background(), Config{Kind: KindLocal})
	if err == nil {
		t.Fatal("Open succeeded with a failing pty factory")
	}
	if !strings.Contains(err.Error(), boom.Error()) {
		t.Fatalf("Open error = %v, want it to carry %v", err, boom)
	}
	if n := len(reg.List()); n != 0 {
		t.Fatalf("registry holds %d sessions after a failed open", n)
	}
}

// And on an ordinary machine it succeeds: the same call, the same registry,
// one session holding a size.
func TestOpen_PTYFactorySucceeds_SessionIsRegisteredWithASize(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &recordingPtyFactory{})

	sess, err := reg.Open(context.Background(), Config{Kind: KindLocal})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if n := len(reg.List()); n != 1 {
		t.Fatalf("registry holds %d sessions, want 1", n)
	}
	if !sess.EffectiveSize().Valid() {
		t.Fatalf("EffectiveSize = %+v, want a usable grid", sess.EffectiveSize())
	}
}

// The remote open's external call, both ways.
func TestOpen_SSHConnectFails_NothingIsRegistered(t *testing.T) {
	boom := errors.New("dial refused")
	reg := New(log.NewSlogAdapter(nil), &recordingPtyFactory{}).
		WithSSHFactory(&recordingSSHFactory{err: boom})

	_, err := reg.Open(context.Background(), Config{
		Kind: KindRemote, Host: "example", Remote: &ssh.ConnectConfig{User: "u"},
	})
	if err == nil {
		t.Fatal("Open succeeded with a failing SSH factory")
	}
	if !strings.Contains(err.Error(), boom.Error()) {
		t.Fatalf("Open error = %v, want it to carry %v", err, boom)
	}
	if n := len(reg.List()); n != 0 {
		t.Fatalf("registry holds %d sessions after a failed remote open", n)
	}
}

func TestOpen_SSHConnectSucceeds_SessionIsRegisteredWithASize(t *testing.T) {
	reg := New(log.NewSlogAdapter(nil), &recordingPtyFactory{}).
		WithSSHFactory(&recordingSSHFactory{})

	sess, err := reg.Open(context.Background(), Config{
		Kind: KindRemote, Host: "example", Remote: &ssh.ConnectConfig{User: "u"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	if n := len(reg.List()); n != 1 {
		t.Fatalf("registry holds %d sessions, want 1", n)
	}
	if !sess.EffectiveSize().Valid() {
		t.Fatalf("EffectiveSize = %+v, want a usable grid", sess.EffectiveSize())
	}
}

// The external call a resize makes. It fails: the error reaches the caller
// AND the session keeps reporting the size it is actually running at — a
// refused resize must not leave EffectiveSize describing a grid the channel
// never took.
func TestResize_ChannelRefuses_TheEffectiveSizeIsUnchanged(t *testing.T) {
	boom := errors.New("resize unsupported")
	f := &recordingPtyFactory{resizeErr: boom}
	reg := New(log.NewSlogAdapter(nil), f)

	sess, err := reg.Open(context.Background(), Config{Kind: KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reg.Close(sess.ID()) }()

	before := sess.EffectiveSize()
	if rerr := sess.Resize(context.Background(), Size{Cols: 200, Rows: 60}); !errors.Is(rerr, boom) {
		t.Fatalf("Resize error = %v, want %v", rerr, boom)
	}
	if got := sess.EffectiveSize(); got != before {
		t.Fatalf("EffectiveSize = %+v after a refused resize, want the unchanged %+v", got, before)
	}
}
