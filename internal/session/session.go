package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type ID string

// InstanceID names one backend instance: minted once per registry (one per
// backend start), so two instances are never equal and a record from a
// previous instance can never be mistaken for one from the current. Same
// shape as ID — 32 hex chars from crypto/rand — and deliberately a
// different type: a session id and an instance id are not interchangeable,
// and assigning one where the other is expected should not compile.
type InstanceID string

// Identity names one session incarnation: the backend instance that minted
// it and the session's epoch within that instance. This is the pair that
// tells a restored record from a current incarnation — the whole reason
// the fields exist (nocx-3oupk).
//
// Within one instance two LIVE sessions can never share an id: the
// registry keys its map on ID, so a second insert would collide. The
// epoch's work is therefore not between two live sessions; it is across
// time. A restore path that reuses an id — re-opening the tab a record
// names — creates a different incarnation at a fresh epoch, and a record
// from a previous backend instance is a different incarnation at a
// different instance. (instance, epoch) says so even where the id does
// not, which is what makes the refusal total rather than probabilistic.
//
// The epoch follows the rule internal/lifecycle states for its own epochs
// (decision 8): monotonic per registry, fresh per session, never reused,
// never resumed. It is NOT the lifecycle kernel's epoch: that one is
// per-domain and minted by the kernel for envelope authentication, while
// this one is per-session-record and minted by the session registry, so a
// conventional session that never integrated still has one, and a session
// that spawns several lifecycle domains (nested ssh) does not change its
// own. internal/content/ledger.go's Session is the ledger's restore key
// (id + workspace, keyed by the bare id — exactly the ambiguity this
// identity resolves) and has no instance/epoch vocabulary of its own; the
// ledger will record this identity when the restore path lands, rather
// than spelling a second one now.
type Identity struct {
	InstanceID InstanceID
	Epoch      uint64
}

// SameIncarnation reports whether a record naming session id with this
// identity refers to the same session incarnation as sess: the id, the
// instance and the epoch must all agree. Each field carries part of the
// refusal — the id says which session the record names, the instance says
// which backend minted it (a record out of a previous instance never
// resolves to a current session of the same id), and the epoch says which
// incarnation of that id within the instance (a later session reusing the
// id is a different incarnation even when the id matches).
func (i Identity) SameIncarnation(id ID, sess Session) bool {
	return sess.ID() == id && sess.Identity() == i
}

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

// ExitCause discriminates how a session ended (nocx-ictcq). It is a closed
// set — the wire enum — never a free string.
type ExitCause string

const (
	// ExitExited is an authoritative terminal event: the shell process
	// itself exited, and ExitOutcome's status carries its exit status.
	// "exited" is a new word because no existing vocabulary names a shell's
	// own exit: the content ledger's terminal states are about agent runs,
	// and the lifecycle's lost/closed words describe the transport, not the
	// process. The plain verb is the honest one.
	ExitExited ExitCause = "exited"
	// ExitInterrupted is a loss: the channel is gone, the host is
	// unreachable, a handshake expired, a reattach failed, or the session
	// was torn down without an authoritative status. The word is the
	// content ledger's (internal/content/ledger.go) — a state chosen after
	// a restart rather than an assertion of liveness — reused here because
	// it is the same statement: the backend cannot assert how the session
	// ended, so it does not invent an exit.
	//
	// The granular loss detail is deliberately NOT part of this vocabulary.
	// internal/lifecyclechannel.LossCause (hello-timeout, end-of-stream,
	// read-error, closed) names losses of the AUTHENTICATED LIFECYCLE
	// CHANNEL — the shell-integration descriptor — and nocx-viil.1 makes it
	// the single owner of that set. This cause rides the exit notification,
	// which fires for every session, including plain non-integrated ssh and
	// local shells where no lifecycle channel ever existed; their losses
	// cannot be spelled in LossCause's words, and extending that set here
	// would be a second one (the exact thing viil.1 forbids). The
	// integration axis (session.integrationChanged) already carries the
	// reason vocabulary (ssh.RefusalReason, including handshake-timeout and
	// channel-lost) for the sessions that have one, so the detail the
	// backend knows is reported on the axis that knows it. The exit cause
	// stays the two-value discriminator: authoritative exit versus loss.
	ExitInterrupted ExitCause = "interrupted"
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
	// Parent claims the session that opened this one (nocx-9hu9d). The zero
	// Ref means "no parent" — a root session, which is every session the
	// product opens today. A non-zero Ref is a CLAIM, checked by
	// validateParent before anything is spawned and recorded immutably on
	// the session afterwards; see lineage.go for what is refused and why.
	// It carries provenance and nothing else: no part of Open reads it to
	// decide what the session may do.
	Parent Ref
	// PaneID names the pane this session is the pipe of (design §6.1 and
	// §7). Frontend-minted UUIDv7, validated and resolved by the transport
	// BEFORE anything is spawned, and empty when this session is attached to
	// no recorded pane.
	//
	// It is recorded rather than derived, unlike the workspace beside it —
	// see the note on Session.PaneID.
	PaneID string
	// Sandbox is the opt-in filesystem policy request for a local pane. Nil
	// for ordinary local and every SSH session.
	Sandbox *sandbox.Request
	// SandboxPrepared runs after policy realization and before the helper is
	// started. A failure aborts launch, preserving the grant-before-enforcement
	// boundary.
	SandboxPrepared func(*sandbox.PreparedCommand) error
	// SandboxStartFailed rolls back the recorded grant if enforcement cannot
	// reach readiness after SandboxPrepared succeeds.
	SandboxStartFailed func() error
}

type PTYFactory interface {
	NewPTY(ctx context.Context, cfg pty.Config) (pty.Pty, error)
}

type OutputHandler func(data []byte) error

type Session interface {
	ID() ID
	Kind() Kind
	// Identity returns the session's incarnation identity: the backend
	// instance that minted it and the session's epoch within that instance
	// (nocx-3oupk). Immutable for the session's lifetime. This is what a
	// restored record carries and what a late observation is compared
	// against — a record or message naming this sessionId from a previous
	// instance, or an earlier epoch of this one, is a different
	// incarnation and must not resolve to this session.
	Identity() Identity
	// Parent returns the session that opened this one, and whether there is
	// one at all (nocx-9hu9d). The edge is immutable: it is written once, at
	// creation, and exists from then until the registry drops this session's
	// record — the parent's own death is not an end of that interval, because
	// provenance does not stop being true when its subject dies (D6). The Ref
	// is returned BY VALUE for the same reason: a caller cannot write back
	// through what it is handed, and there is no other door — nothing on this
	// interface takes a Ref.
	//
	// It answers "who created this", never "who may act on this". A caller
	// deciding an authority question from this value is the defect ADR-0020 §5
	// names; the revocable delegation that does answer it is a later epic.
	Parent() (Ref, bool)
	// Liveness returns what the backend currently believes about this
	// session: {liveness, livenessEpoch, observedAt} over alive | dead |
	// unknown | interrupted (nocx-iarf9). See liveness.go — in particular
	// that a terminal value is derived from this session's own end and can
	// be asserted by nobody, and that `unknown` is what a session on an
	// unreachable host reads as, because both other renderings would lie.
	Liveness() LivenessState
	// PaneID returns the pane this session is the pipe of, or empty when it
	// is attached to none (nocx-rtg0.28). Immutable: a session is the pipe
	// of one pane for its whole life, and the pane outlives it (D5) rather
	// than the other way round.
	//
	// This is NOT the second owner the workspace below would have been, and
	// the difference is where the fact comes from. The workspace is DERIVED
	// — the chain pane → tab → workspace answers it, and a copy here would
	// start lying the first time a pane was dragged. The pane is not derived
	// from anything the session holds; it is what the opener named, and the
	// transport already refused it if it named nothing. It is here because
	// the ledger's write path needs the anchor for every block this session
	// records (design §6.1), and reading it off the session is what keeps
	// the renderer from restating it per event on the envelope — which would
	// be two surfaces owning one input.
	PaneID() string
	// OpenedAt is the moment this session's pipe was opened, on the
	// backend's wall clock. Immutable, and it exists for one question the
	// ledger cannot answer on its own: WHICH of a pane's recorded blocks
	// belong to THIS session. A block is anchored to the pane, not to the
	// session — entries.session_id is deliberately NULL for a command
	// (ws_ledger.go) because the pane is what outlives the pipe — so a pane
	// that has been through a restart, or a second session, carries the
	// blocks of both. The agent's block tools are granted one session
	// (ADR-0020 decision 5), and this is the floor that makes "this
	// session's blocks" expressible: entries recorded before the pipe
	// existed are somebody else's.
	//
	// It is here rather than beside the block tools because nothing else in
	// the process knows it — a copy kept by whoever asked first would be a
	// second owner of a fact the session is born with.
	OpenedAt() time.Time
	// There is deliberately NO WorkspaceID here (nocx-isoph.2). The field
	// nocx-fraus put on the session was the intermediate step, not the
	// destination: since tabs-panes-and-blocks §4.5 the workspace is a
	// column on the TAB, and the backend owns the whole chain, so it
	// RESOLVES pane → tab → workspace itself (content.LayoutRepository's
	// WorkspaceForPane). A copy on the session would be the second owner of
	// one fact, and the two would part company the first time a pane was
	// dragged into another tab — with the session's copy still answering
	// confidently. The invariant is unchanged: never null, one owner of the
	// default (internal/workspace.Default).
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
	// SandboxInfo returns a deep copy of the immutable realized policy for a
	// sandboxed local session, or nil otherwise.
	SandboxInfo() *sandbox.SessionInfo
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
	// OpenBootstrapWindow opens the one interval in which the backend reads
	// this session's output and writes to its input (design §5.5). It is
	// how a typed `ssh` inside this session's shell reaches the far-side
	// loader: the frames travel through this pty and the loader's tokens
	// come back through it. The window takes no byte away from the
	// renderer, and while it is open the user's own input is refused.
	//
	// One window at a time; a second open is an error rather than a second
	// owner of one stream.
	OpenBootstrapWindow() (BootstrapWindow, error)
	// ShellIntegrationReason reports why shell integration did not happen
	// for this session (nocx-r52q). Remote sessions surface the refusal
	// reason decided when the shell started; local sessions always return
	// ReasonNone. The transport carries this value to the UI.
	ShellIntegrationReason() ssh.RefusalReason
	// ExitOutcome reports how the session ended, once Done is closed
	// (nocx-ictcq): an authoritative shell exit with its status, or a loss.
	// The status is meaningful only for ExitExited. Before Done closes the
	// answer is undefined; after a forced teardown (an explicit Close that
	// beat the watcher) it is ExitInterrupted — a teardown is never dressed
	// up as a shell's own exit.
	ExitOutcome() (ExitCause, int)
	// SSHOptions returns the connect options this session's SSH connection
	// was opened with: exactly what Reg.Open handed to the SSH factory, in
	// order. Nil for local sessions. The file manager's SFTP lease
	// (ssh.FSConn) is acquired with these same options so it resolves to
	// the same destination the shell did (fm-w8, spec D3) — the lease
	// shares the tab's pooled connection (AD-4) only when the pool keys
	// agree, and the options are what make them agree.
	SSHOptions() []ssh.ConnectOption
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

// mintInstanceID is the reader-taking mint the failure-path test drives:
// a reader that fails yields a refused identity rather than one that could
// equal another instance's. Reg.New's entropy source is crypto/rand; the
// reader seam exists so the error is observable where it happens.
func mintInstanceID(r io.Reader) (InstanceID, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("mint instance id: %w", err)
	}
	buf := make([]byte, 32)
	hex.Encode(buf, b[:])
	return InstanceID(buf), nil
}

type Reg struct {
	log          log.Logger
	ptf          PTYFactory
	ssh          SSHFactory
	mu           sync.Mutex
	sessions     map[ID]*realSession
	usageTracker ProfileUsageTracker
	// instanceID is this registry's backend-instance identity: minted once
	// at construction, never equal to any other registry's, and stamped on
	// every session it opens. A record from a previous backend instance
	// carries a different one, which is what the refusal compares.
	instanceID InstanceID
	// livenessObserver is told (by Ref) when a session's liveness value
	// changes, so the transport can publish it (nocx-iarf9). Set at the
	// composition root after the transport exists; nil until then, and nil
	// forever in any build that does not publish it. Guarded by mu.
	livenessObserver func(Ref)
	// epochCounter mints the per-session epoch: atomic because Open is
	// called concurrently (one per tab) and the counter has no other lock
	// of its own — the sessions-map insert takes r.mu, but the mint must be
	// safe whether or not the open later fails.
	epochCounter atomic.Uint64
}

// SSHFactory creates SSH connections (AD-4). Injected at the composition
// root so tests can stub it.
type SSHFactory interface {
	Connect(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.Channel, error)
}

// New builds a registry with crypto/rand as its entropy source. It cannot
// fail for the same reason NewID cannot: a rand failure at construction is
// a process-level catastrophe, and the panic exists so a registry never
// starts with an instance identity that could equal another's. The
// failure itself is real and proven through newReg, which is what this
// wraps.
func New(logger log.Logger, ptf PTYFactory) *Reg {
	r, err := newReg(logger, ptf, rand.Reader)
	if err != nil {
		panic("session: " + err.Error())
	}
	return r
}

// newReg is the constructor with the entropy source made explicit — the
// seam the failure-path test drives, so the mint error is observable where
// it happens instead of being swallowed at the public boundary.
func newReg(logger log.Logger, ptf PTYFactory, entropy io.Reader) (*Reg, error) {
	instanceID, err := mintInstanceID(entropy)
	if err != nil {
		return nil, err
	}
	return &Reg{
		log:        logger,
		ptf:        ptf,
		sessions:   make(map[ID]*realSession),
		instanceID: instanceID,
	}, nil
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
	// Mint the session's epoch up front, like the id: the record the id
	// names is (instance, epoch), and the identity is immutable from the
	// moment the session exists.
	epoch := r.epochCounter.Add(1)
	// The parent edge is checked BEFORE anything is spawned or dialed
	// (nocx-9hu9d): a claim that cannot be true must not cost the user a shell
	// or an ssh handshake, and a refused open must leave nothing behind for a
	// later session to inherit. A zero Ref is a root session and skips it.
	if !cfg.Parent.Zero() {
		if err := r.validateParent(id, cfg.Parent); err != nil {
			return nil, err
		}
	}

	var pt pty.Pty
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
		// The keepalive prober's findings come back here (nocx-iarf9): it is
		// the one mechanism that already notices a host has stopped answering
		// while the session is still open, and without this the knowledge was
		// spent entirely on the decision to close the connection. Bound to
		// the host name THIS open used, because the connection underneath is
		// pooled and belongs to no single session.
		opts = append(opts, ssh.WithLivenessObserver(r.hostLivenessObserver(cfg.Host)))
		if cfg.Enhanced {
			opts = append(opts, ssh.WithEnhanced())
		}
		ch, err = r.ssh.Connect(ctx, cfg.Host, opts...)
		if err != nil {
			return nil, fmt.Errorf("ssh connect: %w", err)
		}
	} else {
		var sandboxReq *sandbox.Request
		if cfg.Sandbox != nil {
			copy := *cfg.Sandbox
			copy.Identity = sandbox.SessionIdentity{
				SessionID:  string(id),
				InstanceID: string(r.instanceID),
				Epoch:      epoch,
			}
			sandboxReq = &copy
		}
		var perr error
		pt, perr = r.ptf.NewPTY(ctx, pty.Config{
			Cwd:                cfg.Cwd,
			Cols:               cfg.Cols,
			Rows:               cfg.Rows,
			XPixel:             cfg.XPixel,
			YPixel:             cfg.YPixel,
			Enhanced:           cfg.Enhanced,
			SessionID:          string(id),
			Sandbox:            sandboxReq,
			SandboxPrepared:    cfg.SandboxPrepared,
			SandboxStartFailed: cfg.SandboxStartFailed,
		})
		if perr != nil {
			return nil, fmt.Errorf("open session: %w", perr)
		}
		ch = pt
	}

	s := &realSession{
		id:           id,
		openedAt:     time.Now(),
		identity:     Identity{InstanceID: r.instanceID, Epoch: epoch},
		parent:       cfg.Parent,
		kind:         cfg.Kind,
		host:         cfg.Host,
		cwd:          resolveSessionCwd(cfg.Cwd),
		paneID:       cfg.PaneID,
		profileID:    cfg.ProfileID,
		credentialID: cfg.CredentialID,
		sshOpts:      opts,
		ch:           ch,
		log:          r.log.With("session_id", string(id)),
		writeCh:      make(chan writeJob, writeQueueDepth),
		writeDone:    make(chan struct{}),
	}
	if pt != nil {
		if provider, ok := pt.(pty.SandboxInfoProvider); ok {
			s.sandboxInfo = provider.SandboxInfo()
		}
	}
	if s.sandboxInfo != nil {
		// The sandbox workspace is canonical policy metadata; do not
		// abbreviate it through the ordinary display-cwd resolver.
		s.cwd = s.sandboxInfo.Workspace
	}
	s.startWriteLoop()

	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()

	r.log.Info("session opened", "id", string(id), "instance_id", string(r.instanceID), "epoch", epoch, "kind", kindName(cfg.Kind), "profile_id", cfg.ProfileID)
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
	identity     Identity // the incarnation identity: instance + epoch, immutable
	parent       Ref      // who opened this session; zero for a root. Written once, at construction, and never again — there is no setter, and Parent hands out a copy
	kind         Kind
	host         string // empty for local sessions; the remote hostname for SSH
	cwd          string
	paneID       string    // the pane this session is the pipe of; empty for none. Written once, at construction
	openedAt     time.Time // when the pipe was opened. Written once, at construction; see Session.OpenedAt
	profileID    string
	credentialID string
	sshOpts      []ssh.ConnectOption // the options the SSH connection was opened with; nil for local

	ch        Channel
	log       log.Logger
	handler   OutputHandler
	handlerMu sync.Mutex
	closeOnce sync.Once

	// The liveness projection (nocx-iarf9). livenessMu guards the record;
	// livenessEpochs is this session's own monotonic source of observation
	// epochs, so any two observations of THIS session are ordered without a
	// shared clock (an observation of another incarnation is refused by
	// identity, never by number). The zero record means "not observed yet",
	// which livenessLocked resolves on the first read.
	livenessMu     sync.Mutex
	liveness       LivenessState
	livenessEpochs atomic.Uint64
	// Immutable realized sandbox metadata for this local session.
	sandboxInfo *sandbox.SessionInfo

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

	// window is the open bootstrap window's tap, or nil (design §5.5).
	// inputQuarantined is the same interval seen from the input side: while
	// it is true the USER's bytes are refused, never buffered. Both are
	// guarded by windowMu, which is separate from handlerMu because the
	// read pump takes it on every chunk and must never contend with a
	// handler swap.
	windowMu         sync.Mutex
	window           *outputTap
	inputQuarantined bool
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

func (s *realSession) ID() ID                            { return s.id }
func (s *realSession) Identity() Identity                { return s.identity }
func (s *realSession) Parent() (Ref, bool)               { return s.parent, !s.parent.Zero() }
func (s *realSession) Kind() Kind                        { return s.kind }
func (s *realSession) PaneID() string                    { return s.paneID }
func (s *realSession) OpenedAt() time.Time               { return s.openedAt }
func (s *realSession) Host() string                      { return s.host }
func (s *realSession) Cwd() string                       { return s.cwd }
func (s *realSession) ProfileID() string                 { return s.profileID }
func (s *realSession) CredentialID() string              { return s.credentialID }
func (s *realSession) SandboxInfo() *sandbox.SessionInfo { return s.sandboxInfo.Clone() }
func (s *realSession) SSHOptions() []ssh.ConnectOption   { return s.sshOpts }

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
	// The input quarantine (design §5.3). This is the USER's path — every
	// keystroke, paste and synthetic input arrives here — so this is where
	// the bootstrap window refuses them. REFUSED, not buffered: a buffered
	// keystroke is a command the user did not knowingly run, executed later
	// at a prompt they were not looking at. Resize and the other control
	// events do not travel this way and keep working.
	if s.inputRefused() {
		return false
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

// ShellIntegrationReason surfaces the connect-time refusal reason (nocx-r52q).
// The unified Channel has no such method — local PTYs have nothing to report —
// so this is an optional-method check: remote channels (ssh.Channel) carry it,
// everything else is ReasonNone.
func (s *realSession) ShellIntegrationReason() ssh.RefusalReason {
	if rc, ok := s.ch.(interface{ ShellIntegrationReason() ssh.RefusalReason }); ok {
		return rc.ShellIntegrationReason()
	}
	return ssh.ReasonNone
}

// ExitOutcome maps the channel's captured wait result to the wire cause
// (nocx-ictcq). The mapping is owned here, never by the transport, so no
// second owner can decide what an *exec.ExitError means. Only an exit the
// process itself reported — nil status 0, *exec.ExitError for a local shell,
// *gossh.ExitError for a remote one — is authoritative; everything else,
// including a channel that was closed before its watcher recorded, is a
// loss, and a loss never carries a fabricated status.
func (s *realSession) ExitOutcome() (ExitCause, int) {
	provider, ok := s.ch.(interface{ WaitErr() (error, bool) })
	if !ok {
		return ExitInterrupted, 0
	}
	waitErr, recorded := provider.WaitErr()
	if !recorded {
		return ExitInterrupted, 0
	}
	if waitErr == nil {
		return ExitExited, 0
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		return ExitExited, ee.ExitCode()
	}
	var se *gossh.ExitError
	if errors.As(waitErr, &se) {
		return ExitExited, se.ExitStatus()
	}
	return ExitInterrupted, 0
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

		// The bootstrap window's copy, taken BEFORE the renderer's and
		// taking nothing from it (design §5.5): every byte still reaches
		// the renderer, in order, unchanged.
		s.tapOutput(buf[:n])

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

// SignalForeground sends sig to the foreground process group of the
// session's terminal — the execution's own process group, created by the
// interactive shell's job control — so cancellation reaches the command's
// children rather than only the shell (ADR-0020 decision 2: cancellation
// escalates INT → TERM → KILL against the execution's group). The local
// pty channel implements it; a remote channel (the execution runs on the
// far host, unreachable from here) and the stub have no local process
// group and report pty.ErrNoForeground — the lease's escalation treats
// that as "nothing to cancel" and terminalizes the run without a kill,
// which is the honest answer for a host this process cannot signal.
func (s *realSession) SignalForeground(sig syscall.Signal) error {
	sg, ok := s.ch.(interface {
		SignalForeground(sig syscall.Signal) error
	})
	if !ok {
		return pty.ErrNoForeground
	}
	return sg.SignalForeground(sig)
}
