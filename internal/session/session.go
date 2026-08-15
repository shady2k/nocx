package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/ssh"
)

type ID string

// ErrSessionClosed is returned by Write once Close has been called: the
// write loop has stopped and nothing further will reach the channel.
var ErrSessionClosed = errors.New("session is closed")

// writeQueueDepth is how many data frames a session may have in flight
// before the transport starts dropping them. It buys the write loop room
// to fall behind a burst — a paste, a held key — without letting a channel
// that has stopped accepting bytes grow the queue without bound. A session
// that reaches this depth is not slow, it is stuck, and the transport says
// so rather than buffering input the user will never see arrive.
const writeQueueDepth = 64

type Kind int

const (
	KindLocal Kind = iota
	KindRemote
)

type Config struct {
	Kind   Kind
	Cwd    string
	Host   string
	Local  *pty.Config
	Remote *ssh.ConnectConfig
	Cols   uint16
	Rows   uint16
	XPixel uint16
	YPixel uint16
	// Enhanced requests the marker-only prompt env (ADR-0006) for this session.
	Enhanced bool
	// ProfileID records the profile this session was opened from, enabling
	// the connection list to report which rows are live and when they were
	// last used (nocx-uxs5.4). Empty for ad-hoc/local sessions.
	ProfileID string
	// CredentialID records the credential this session was opened with.
	// Used by revocation to find sessions running that credential.
	// Empty for sessions with no linked credential (inline auth).
	CredentialID string
}

type PTYFactory interface {
	NewPTY(ctx context.Context, cfg pty.Config) (pty.Pty, error)
}

type OutputHandler func(data []byte) error

type Session interface {
	ID() ID
	Kind() Kind
	// Host returns the session's remote hostname. Empty for a local session.
	Host() string
	// Cwd is where the session's shell was started. It is the tab's name
	// until a program sets a title; it does NOT follow `cd`, which needs the
	// OSC 7 events in nocx-5mn.2.
	Cwd() string
	// ProfileID returns the profile ID this session was opened from.
	// Empty for ad-hoc/local sessions (nocx-uxs5.4).
	ProfileID() string

	// CredentialID returns the credential ID this session was opened with.
	// Empty for sessions with no linked credential (inline auth) and for
	// local/ad-hoc sessions.
	CredentialID() string
	// Write sends p to the session's channel and returns the number of
	// bytes written and any error. It blocks until the write completes
	// (or the session dies); callers that must not block — the
	// transport readLoop — use EnqueueWrite instead.
	Write(p []byte) (int, error)
	// EnqueueWrite submits p to the session's write queue without
	// waiting for the channel write to complete. It preserves FIFO
	// order relative to other EnqueueWrite calls from the same caller
	// (the transport readLoop), and never blocks: if the queue is
	// full the frame is dropped. Returns false if the session is
	// closed or the queue is full.
	EnqueueWrite(p []byte) bool
	Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error
	Close() error
	Done() <-chan struct{}
	StartOutput(ctx context.Context, onOutput OutputHandler) error
	// ShellIntegrationReason reports why shell integration did not happen
	// for this session (nocx-r52q). Remote sessions surface the refusal
	// reason decided when the shell started; local sessions always return
	// ReasonNone. The transport carries this value to the UI.
	ShellIntegrationReason() ssh.RefusalReason
	// SSHOptions returns the connect options this session's SSH connection
	// was opened with: exactly what Reg.Open handed to the SSH factory, in
	// order. Nil for local sessions. The file manager's SFTP lease
	// (ssh.FSConn) is acquired with these same options so it resolves to
	// the same destination the shell did (fm-w8, spec D3) — the lease
	// shares the tab's pooled connection (AD-4) only when the pool keys
	// agree, and the options are what make them agree.
	SSHOptions() []ssh.ConnectOption
	// HostKeyFingerprint returns the SHA256 fingerprint of the remote
	// host's public key, as observed and verified when the connection was
	// dialed (the consent design §3.2 keys consent by it — the same
	// machine reached any way is one answer). Empty for local sessions and
	// for channels that did not capture one; an empty fingerprint never
	// keys a consent answer.
	HostKeyFingerprint() string
}

// ProfileUsageTracker records profile session activity (nocx-uxs5.4).
// Implementations may persist last-used timestamps; a nil tracker is a no-op.
type ProfileUsageTracker interface {
	// SessionOpened is called when a session is created for a profile.
	SessionOpened(profileID string)
	// SessionClosed is called when a session for a profile ends.
	SessionClosed(profileID string)
	// LastUsedForProfiles returns the last-used time for each requested
	// profile ID. Profiles with no recorded usage are absent from the map.
	LastUsedForProfiles(profileIDs []string) (map[string]time.Time, error)
}

type Registry interface {
	Open(ctx context.Context, cfg Config) (Session, error)
	Get(id ID) (Session, error)
	Close(id ID) error
	List() []Session
}

func NewID() ID {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return IDFromBytes(b)
}

func IDFromBytes(b [16]byte) ID {
	buf := make([]byte, 32)
	hex.Encode(buf, b[:])
	return ID(buf)
}

func IDToBytes(id ID) ([16]byte, error) {
	if len(id) != 32 {
		return [16]byte{}, fmt.Errorf("session id must be 32 hex chars, got %d", len(id))
	}
	var b [16]byte
	_, err := hex.Decode(b[:], []byte(id))
	if err != nil {
		return [16]byte{}, fmt.Errorf("invalid session id hex: %w", err)
	}
	return b, nil
}

type Reg struct {
	log          log.Logger
	ptf          PTYFactory
	ssh          SSHFactory
	mu           sync.Mutex
	sessions     map[ID]*realSession
	usageTracker ProfileUsageTracker
}

// SSHFactory creates SSH connections (AD-4). Injected at the composition
// root so tests can stub it.
type SSHFactory interface {
	Connect(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.Channel, error)
}

func New(logger log.Logger, ptf PTYFactory) *Reg {
	return &Reg{
		log:      logger,
		ptf:      ptf,
		sessions: make(map[ID]*realSession),
	}
}

// WithSSHFactory injects an SSH factory, enabling KindRemote sessions.
func (r *Reg) WithSSHFactory(f SSHFactory) *Reg {
	r.ssh = f
	return r
}

// WithProfileUsageTracker injects a ProfileUsageTracker, enabling last-used
// persistence and the sessions.status RPC (nocx-uxs5.4). A nil tracker is a
// no-op — Open and Close work without it.
func (r *Reg) WithProfileUsageTracker(t ProfileUsageTracker) *Reg {
	r.usageTracker = t
	return r
}

func (r *Reg) Open(ctx context.Context, cfg Config) (Session, error) {
	// Mint the session ID BEFORE any connect: the remote launcher embeds it
	// as NOCX_SESSION_ID in the start command (nocx-r52q), so the ID the
	// session is later registered under must already exist while Connect
	// runs. A failed connect registers nothing — the ID is simply unused.
	id := NewID()

	var ch Channel
	var err error
	var opts []ssh.ConnectOption

	if cfg.Kind == KindRemote {
		if r.ssh == nil {
			return nil, fmt.Errorf("SSH sessions not available (no SSH factory wired)")
		}
		if cfg.Remote == nil {
			return nil, fmt.Errorf("remote session requires ConnectConfig")
		}
		opts = sshOptionsFromConfig(cfg.Remote)
		opts = append(opts, ssh.WithSessionID(string(id)))
		if cfg.Enhanced {
			opts = append(opts, ssh.WithEnhanced())
		}
		ch, err = r.ssh.Connect(ctx, cfg.Host, opts...)
		if err != nil {
			return nil, fmt.Errorf("ssh connect: %w", err)
		}
	} else {
		pt, perr := r.ptf.NewPTY(ctx, pty.Config{
			Cwd:       cfg.Cwd,
			Cols:      cfg.Cols,
			Rows:      cfg.Rows,
			XPixel:    cfg.XPixel,
			YPixel:    cfg.YPixel,
			Enhanced:  cfg.Enhanced,
			SessionID: string(id),
		})
		if perr != nil {
			return nil, fmt.Errorf("open session: %w", perr)
		}
		ch = pt
	}

	s := &realSession{
		id:           id,
		kind:         cfg.Kind,
		host:         cfg.Host,
		cwd:          resolveSessionCwd(cfg.Cwd),
		profileID:    cfg.ProfileID,
		credentialID: cfg.CredentialID,
		sshOpts:      opts,
		ch:           ch,
		log:          r.log.With("session_id", string(id)),
		writeCh:      make(chan writeJob, writeQueueDepth),
		writeDone:    make(chan struct{}),
	}
	s.startWriteLoop()

	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()

	r.log.Info("session opened", "id", string(id), "kind", kindName(cfg.Kind), "profile_id", cfg.ProfileID)
	if r.usageTracker != nil && cfg.ProfileID != "" {
		r.usageTracker.SessionOpened(cfg.ProfileID)
	}
	return s, nil
}

func (r *Reg) Get(id ID) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	return s, nil
}

func (r *Reg) Close(id ID) error {
	r.mu.Lock()
	s, ok := r.sessions[id]
	if ok {
		delete(r.sessions, id)
	}
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	r.log.Info("session closed", "id", string(id))
	err := s.Close()
	if r.usageTracker != nil && s.profileID != "" {
		r.usageTracker.SessionClosed(s.profileID)
	}
	return err
}

func (r *Reg) List() []Session {
	r.mu.Lock()
	defer r.mu.Unlock()

	sessions := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	return sessions
}

// resolveSessionCwd mirrors what the PTY layer will actually do, so the value
// the client is told matches the directory the shell really starts in, and
// renders it the way a terminal user expects to read it. Only this side knows
// the home directory, so the ~ abbreviation happens here rather than in the UI.
func resolveSessionCwd(cwd string) string {
	dir := cwd
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = home
	}
	return abbreviateHome(dir)
}

func abbreviateHome(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dir
	}
	if dir == home {
		return "~"
	}
	if strings.HasPrefix(dir, home+string(os.PathSeparator)) {
		return "~" + dir[len(home):]
	}
	return dir
}

func kindName(k Kind) string {
	switch k {
	case KindLocal:
		return "local"
	case KindRemote:
		return "ssh"
	default:
		return "unknown"
	}
}

// sshOptionsFromConfig converts a ssh.ConnectConfig into ConnectOptions.
func sshOptionsFromConfig(cfg *ssh.ConnectConfig) []ssh.ConnectOption {
	var opts []ssh.ConnectOption
	if cfg.User != "" {
		opts = append(opts, ssh.WithUser(cfg.User))
	}
	if cfg.Port > 0 {
		opts = append(opts, ssh.WithPort(cfg.Port))
	}
	if cfg.KeyFile != "" {
		opts = append(opts, ssh.WithKeyFile(cfg.KeyFile))
	}
	if cfg.UseAgent {
		opts = append(opts, ssh.WithAgent())
	}
	if cfg.Cols > 0 || cfg.Rows > 0 {
		opts = append(opts, ssh.WithPTYSize(cfg.Cols, cfg.Rows, cfg.XPixel, cfg.YPixel))
	}
	if cfg.AuthMode != "" {
		opts = append(opts, ssh.WithAuthMode(cfg.AuthMode))
	}
	if cfg.JumpHost != "" {
		opts = append(opts, ssh.WithJumpHost(cfg.JumpHost, cfg.JumpPort, cfg.JumpUser, cfg.JumpAuthMode))
	}
	// JumpConfig carries the full recursive jump configuration (Secrets,
	// SecretID, KeyFile, AuthMode, nested JumpConfig) — without it the
	// session→ssh seam drops the bastion's auth material and the dial
	// offers no methods (nocx-8b1v).
	if cfg.JumpConfig != nil {
		opts = append(opts, ssh.WithJumpConfig(cfg.JumpConfig))
	}
	if cfg.JumpSecrets != nil {
		opts = append(opts, ssh.WithJumpCredentials(cfg.JumpSecrets, cfg.JumpSecretID))
	}
	if cfg.JumpPassphraseSecretID != "" {
		opts = append(opts, ssh.WithJumpPassphraseSecretID(cfg.JumpPassphraseSecretID))
	}
	if cfg.Secrets != nil {
		opts = append(opts, ssh.WithCredentials(cfg.Secrets, cfg.SecretID))
	}
	// The vault-stored key (and its passphrase) ride the same store: without
	// them the dial sees publicKey auth with nothing to offer — the probe
	// works because it uses the resolved config directly, the session loses
	// the binding here. ADR-0017: a connection references a secret.
	if cfg.KeySecretID != "" {
		opts = append(opts, ssh.WithKeySecretID(cfg.KeySecretID))
	}
	if cfg.PassphraseSecretID != "" {
		opts = append(opts, ssh.WithPassphraseSecretID(cfg.PassphraseSecretID))
	}
	if cfg.ConnectionName != "" {
		opts = append(opts, ssh.WithConnectionName(cfg.ConnectionName))
	}
	if cfg.PasswordRequester != nil {
		opts = append(opts, ssh.WithPasswordRequester(cfg.PasswordRequester))
	}
	if cfg.AuthorizedEndpoint != "" {
		opts = append(opts, ssh.WithAuthorizedEndpoint(cfg.AuthorizedEndpoint))
	}
	if cfg.JumpAuthorizedEndpoint != "" {
		opts = append(opts, ssh.WithJumpAuthorizedEndpoint(cfg.JumpAuthorizedEndpoint))
	}
	if cfg.RemoteLauncher != nil {
		opts = append(opts, ssh.WithRemoteLauncher(cfg.RemoteLauncher))
	}
	if cfg.RemoteLifecycle != nil {
		opts = append(opts, ssh.WithRemoteLifecycle(cfg.RemoteLifecycle))
	}
	// The resolved destination mode rides the same path (nocx-mlm7):
	// without this the profile's effective desiredMode dies here and every
	// profile — raw or relay included — would integrate at open. A field
	// that is carried and discarded is worse than one that is missing.
	if cfg.DesiredMode != "" {
		opts = append(opts, ssh.WithDesiredMode(cfg.DesiredMode))
	}
	// The shell pin rides the same path as the launcher: without this the
	// pin dies here and the launcher always receives ShellAuto — a field
	// that is carried and discarded is worse than one that is missing,
	// because it looks configured (nocx-pu4.1). Empty means detect: the
	// launcher maps "" to ShellAuto at the far end (nocx-6rj0).
	if cfg.Shell != "" {
		opts = append(opts, ssh.WithShell(cfg.Shell))
	}
	if cfg.RemoteInstaller != nil {
		opts = append(opts, ssh.WithRemoteInstaller(cfg.RemoteInstaller))
	}
	return opts
}

// realSession is the concrete Session implementation.
//
// Deleted profile with open session: the session holds its own Channel
// (SSH connection or PTY) and does not reference the profile store at
type realSession struct {
	id           ID
	kind         Kind
	host         string // empty for local sessions; the remote hostname for SSH
	cwd          string
	profileID    string
	credentialID string
	sshOpts      []ssh.ConnectOption // the options the SSH connection was opened with; nil for local

	ch        Channel
	log       log.Logger
	handler   OutputHandler
	handlerMu sync.Mutex
	closeOnce sync.Once

	// writeCh feeds a single write goroutine that serialises every write in
	// arrival order. The readLoop hands frames over without waiting, so
	// ordering has to be the queue's job: two frames for one session —
	// "pwd\n" then "hostname\n" — must reach the channel in that order or
	// the user sees "pwdhostname". It is bounded so a channel that has
	// stopped accepting bytes cannot grow it without limit.
	//
	// writeCh is NEVER closed. Closing it would race every sender: the
	// readLoop can be inside EnqueueWrite, past any closed check, at the
	// moment Close runs — and a send on a closed channel is a panic that
	// takes the whole backend down, not one tab. writeDone is the stop
	// signal instead; a send on an open channel is always safe.
	writeCh   chan writeJob
	writeDone chan struct{}
}

// writeJob carries a payload and its result channel. The result is
// best-effort: the transport logs errors and does not block on them,
// but returning the error keeps the Write signature honest.
type writeJob struct {
	p   []byte
	res chan writeResult
}

type writeResult struct {
	n   int
	err error
}

func (s *realSession) ID() ID                          { return s.id }
func (s *realSession) Kind() Kind                      { return s.kind }
func (s *realSession) Host() string                    { return s.host }
func (s *realSession) Cwd() string                     { return s.cwd }
func (s *realSession) ProfileID() string               { return s.profileID }
func (s *realSession) CredentialID() string            { return s.credentialID }
func (s *realSession) SSHOptions() []ssh.ConnectOption { return s.sshOpts }

func (s *realSession) Write(p []byte) (int, error) {
	res := make(chan writeResult, 1)
	select {
	case s.writeCh <- writeJob{p: p, res: res}:
	case <-s.writeDone:
		return 0, ErrSessionClosed
	case <-s.ch.Done():
		return 0, ErrSessionClosed
	}
	select {
	case r := <-res:
		return r.n, r.err
	case <-s.writeDone:
		return 0, ErrSessionClosed
	case <-s.ch.Done():
		return 0, ErrSessionClosed
	}
}

// EnqueueWrite submits p to the write queue without blocking. The
// transport readLoop calls this for every data frame: it must never
// stall on one session (a dead SSH channel would freeze every other
// tab). If the bounded queue is full the frame is dropped — a slow
// channel should not be able to exhaust memory. Because the readLoop
// is the sole EnqueueWrite caller, the queue preserves FIFO order:
// frame N is enqueued before frame N+1. The result channel is nil —
// the transport does not need the write result, and writeLoop skips
// the send when res is nil.
func (s *realSession) EnqueueWrite(p []byte) bool {
	select {
	case <-s.writeDone:
		return false
	default:
	}
	select {
	case s.writeCh <- writeJob{p: p}:
		return true
	default:
		return false
	}
}

// startWriteLoop runs the single goroutine that drains writeCh in FIFO
// order. It exits on writeDone, which Close closes, so it never leaks and
// never observes a closed writeCh.
func (s *realSession) startWriteLoop() {
	go func() {
		for {
			select {
			case <-s.writeDone:
				return
			case job := <-s.writeCh:
				n, err := s.ch.Write(job.p)
				// res == nil is the TRANSPORT's path — every byte the user
				// types arrives here, and nobody is waiting for the result.
				// So an error here had exactly one reader and it was
				// discarded: a keystroke that never reached the pty looked
				// identical to one that did, from every log this product
				// keeps. That is the shape of "input is trapped", and it must
				// not be silent (nocx-xplc).
				//
				// Warn on the error, because a failed write on the input path
				// is the user typing into nothing. Debug on the short write —
				// same question, lower volume.
				switch {
				case job.res != nil:
					// A caller is waiting; the result is theirs to read, and
					// theirs to report.
					job.res <- writeResult{n: n, err: err}
				case err != nil:
					s.log.Warn("session input write failed; keystrokes did not reach the terminal",
						"error", err, "bytes", len(job.p), "written", n)
				case n != len(job.p):
					s.log.Debug("session input short write",
						"bytes", len(job.p), "written", n)
				}
			}
		}
	}()
}

func (s *realSession) Resize(ctx context.Context, cols, rows, xpixel, ypixel uint16) error {
	return s.ch.Resize(ctx, cols, rows, xpixel, ypixel)
}

func (s *realSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.log.Debug("closing session")
		// Signal the write loop and close the channel without waiting for
		// it. Waiting would hand a dead SSH channel the power to block
		// Close for as long as its write blocks — and `close` is handled
		// on the readLoop, so that would be the freeze this change exists
		// to remove, arriving through the other door. ch.Close is what
		// unblocks the in-flight write; the loop then sees writeDone.
		close(s.writeDone)
		err = s.ch.Close()
	})
	return err
}

func (s *realSession) Done() <-chan struct{} {
	return s.ch.Done()
}

// ShellIntegrationReason surfaces the connect-time refusal reason
// (nocx-r52q). The unified Channel has no such method — local PTYs have
// nothing to report — so this is an optional-method check: remote channels
// (ssh.Channel) carry it, everything else is ReasonNone.
func (s *realSession) ShellIntegrationReason() ssh.RefusalReason {
	if rc, ok := s.ch.(interface{ ShellIntegrationReason() ssh.RefusalReason }); ok {
		return rc.ShellIntegrationReason()
	}
	return ssh.ReasonNone
}

// HostKeyFingerprint surfaces the host public-key fingerprint observed at
// dial time (the consent design's consent key). Like
// ShellIntegrationReason, the unified Channel has no such method — local
// PTYs and stubs have nothing to report — so this is an optional-method
// check: remote channels that captured the key carry it, everything else
// answers "".
func (s *realSession) HostKeyFingerprint() string {
	if rc, ok := s.ch.(interface{ HostKeyFingerprint() string }); ok {
		return rc.HostKeyFingerprint()
	}
	return ""
}

func (s *realSession) StartOutput(ctx context.Context, onOutput OutputHandler) error {
	s.handlerMu.Lock()
	if s.handler != nil {
		s.handlerMu.Unlock()
		return fmt.Errorf("output already started for session %s", s.id)
	}
	s.handler = onOutput
	s.handlerMu.Unlock()

	go s.readPump(ctx)
	return nil
}

func (s *realSession) readPump(ctx context.Context) {
	buf := make([]byte, 32768)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := s.ch.Read(buf)
		if err != nil {
			if err != io.EOF {
				s.log.Debug("pty read error", "error", err)
			}
			return
		}

		s.handlerMu.Lock()
		h := s.handler
		s.handlerMu.Unlock()

		if h == nil {
			return
		}

		if err := h(buf[:n]); err != nil {
			s.log.Debug("output handler error", "error", err)
			return
		}
	}
}
