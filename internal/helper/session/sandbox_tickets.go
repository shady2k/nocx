package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sandbox"
)

const (
	sandboxPendingLimit   = 32
	sandboxConsumedLimit  = 1024
	sandboxPreparationTTL = 60 * time.Second
	sandboxConsumedTTL    = 10 * time.Minute
)

var (
	errSandboxUnsupported   = errors.New("sandbox: native preparation unavailable")
	errSandboxTicketExpired = errors.New("sandbox: preparation expired or unknown")
	errSandboxMismatch      = errors.New("sandbox: preparation payload mismatch")
	errSandboxCapacity      = errors.New("sandbox: preparation capacity exhausted")
	errSandboxParams        = errors.New("sandbox: invalid request")
)

// SandboxEnvironment contains helper composition facts, never wire authority.
type SandboxEnvironment struct {
	HostHome      string
	RunnerPath    string
	RuntimeBase   string
	ReservedRoots []string
}

type sandboxPreparer interface {
	PrepareSandbox(sandbox.BuildRequest) (*sandbox.Prepared, error)
	NativeCleanupPending() bool
}

type sandboxTicket struct {
	token       string
	intent      proto.SandboxPrepareParams
	prepareHash [32]byte
	launchHash  [32]byte
	prepared    *sandbox.Prepared
	created     time.Time
	consumed    time.Time
	preparing   chan struct{}
	done        chan struct{}
	expired     bool
	result      proto.SpawnResult
	err         error
}

type sandboxTicketState struct {
	mu       sync.Mutex
	pending  map[string]*sandboxTicket
	consumed map[string]*sandboxTicket
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	closing  bool
}

func newSandboxTicketState() *sandboxTicketState {
	state := &sandboxTicketState{pending: make(map[string]*sandboxTicket), consumed: make(map[string]*sandboxTicket), stop: make(chan struct{}), done: make(chan struct{})}
	return state
}

func sandboxRequestHash(value any) [32]byte {
	encoded, _ := json.Marshal(value)
	return sha256.Sum256(encoded)
}

func validSandboxID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func (s *Service) sandboxPrepare(ctx context.Context, p proto.SandboxPrepareParams) (proto.SandboxPrepareResult, error) {
	if !validSandboxID(p.OperationID) || !validSandboxID(p.LaunchID) || len(p.Cwd) > sandbox.MaxPathBytes || len(p.Workspace) > 128 || p.Cwd == "" || strings.ContainsAny(p.Cwd, "\x00\r\n") {
		return proto.SandboxPrepareResult{}, errSandboxParams
	}
	if p.Mode != proto.SandboxOff && p.Mode != proto.SandboxEnforce || (p.Mode == proto.SandboxEnforce) != (p.Enforce != nil) {
		return proto.SandboxPrepareResult{}, errSandboxParams
	}
	if p.Enforce != nil && p.Enforce.ProfileProvenance != sandbox.StandardRoot && p.Enforce.ProfileProvenance != sandbox.WorkspaceProfileRoot {
		return proto.SandboxPrepareResult{}, errSandboxParams
	}
	state := s.sandboxTickets
	hash := sandboxRequestHash(p)
	state.mu.Lock()
	if state.closing {
		state.mu.Unlock()
		return proto.SandboxPrepareResult{}, errSandboxTicketExpired
	}
	if preparer, ok := s.spawner.(sandboxPreparer); ok && preparer.NativeCleanupPending() {
		state.mu.Unlock()
		return proto.SandboxPrepareResult{}, errNativeClosePending
	}
	for _, previous := range state.consumed {
		if previous.intent.OperationID == p.OperationID || previous.intent.LaunchID == p.LaunchID {
			state.mu.Unlock()
			return proto.SandboxPrepareResult{}, errSandboxTicketExpired
		}
	}
	for _, previous := range state.pending {
		if previous.intent.OperationID == p.OperationID || previous.intent.LaunchID == p.LaunchID {
			if previous.prepareHash != hash {
				state.mu.Unlock()
				return proto.SandboxPrepareResult{}, errSandboxMismatch
			}
			state.mu.Unlock()
			select {
			case <-previous.preparing:
			case <-ctx.Done():
				return proto.SandboxPrepareResult{}, ctx.Err()
			}
			state.mu.Lock()
			result, err := sandboxPreparationResult(previous), previous.err
			if previous.expired {
				err = errSandboxTicketExpired
			}
			state.mu.Unlock()
			return result, err
		}
	}
	if len(state.pending) >= sandboxPendingLimit {
		state.mu.Unlock()
		return proto.SandboxPrepareResult{}, errSandboxCapacity
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		state.mu.Unlock()
		return proto.SandboxPrepareResult{}, errSandboxCapacity
	}
	token := hex.EncodeToString(nonce[:])
	if state.pending[token] != nil || state.consumed[token] != nil {
		state.mu.Unlock()
		return proto.SandboxPrepareResult{}, errSandboxCapacity
	}
	ticket := &sandboxTicket{token: token, intent: p, prepareHash: hash, created: s.now(), preparing: make(chan struct{}), done: make(chan struct{})}
	state.pending[token] = ticket
	state.mu.Unlock()

	var prepared *sandbox.Prepared
	var err error
	if p.Enforce != nil {
		preparer, available := s.spawner.(sandboxPreparer)
		if !available || s.sandboxEnvironment.RunnerPath == "" {
			err = errSandboxUnsupported
		} else {
			prepared, err = preparer.PrepareSandbox(sandbox.BuildRequest{WorkspaceID: string(p.Workspace), Cwd: p.Cwd, HostHome: s.sandboxEnvironment.HostHome, Runner: s.sandboxEnvironment.RunnerPath, RuntimeBase: s.sandboxEnvironment.RuntimeBase, ReservedRoots: s.sandboxEnvironment.ReservedRoots, StandardRevision: p.Enforce.StandardRevision, WorkspaceRevision: p.Enforce.WorkspaceRevision, Profile: p.Enforce.Profile, Delta: p.Enforce.Delta, ProfileProvenance: p.Enforce.ProfileProvenance})
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	state.mu.Lock()
	if !s.now().Before(ticket.created.Add(sandboxPreparationTTL)) || ticket.expired {
		ticket.expired = true
		err = errSandboxTicketExpired
	}
	ticket.prepared, ticket.err = prepared, err
	if err != nil {
		delete(state.pending, token)
	}
	close(ticket.preparing)
	result := sandboxPreparationResult(ticket)
	state.mu.Unlock()
	if err != nil && prepared != nil {
		_ = prepared.Cleanup()
	}
	return result, err
}

func sandboxPreparationResult(ticket *sandboxTicket) proto.SandboxPrepareResult {
	result := proto.SandboxPrepareResult{Ticket: ticket.token, OperationID: ticket.intent.OperationID, LaunchID: ticket.intent.LaunchID, Mode: ticket.intent.Mode, ExpiresAt: ticket.created.Add(sandboxPreparationTTL).UTC().Format(time.RFC3339Nano)}
	if ticket.prepared != nil {
		result.Enforce = &proto.SandboxPreparedPolicy{Policy: ticket.prepared.Policy, Digest: ticket.prepared.Digest}
	}
	return result
}

func (s *Service) sandboxLaunch(ctx context.Context, p proto.SandboxLaunchParams) (proto.SpawnResult, error) {
	state := s.sandboxTickets
	hash := sandboxRequestHash(p)
	state.mu.Lock()
	if previous := state.consumed[p.Ticket]; previous != nil {
		if !s.now().Before(previous.consumed.Add(sandboxConsumedTTL)) {
			state.mu.Unlock()
			return proto.SpawnResult{}, errSandboxTicketExpired
		}
		if previous.launchHash != hash {
			state.mu.Unlock()
			return proto.SpawnResult{}, errSandboxMismatch
		}
		state.mu.Unlock()
		select {
		case <-previous.done:
		case <-ctx.Done():
			return proto.SpawnResult{}, ctx.Err()
		}
		return previous.result, previous.err
	}
	if state.closing {
		state.mu.Unlock()
		return proto.SpawnResult{}, errSandboxTicketExpired
	}
	if preparer, ok := s.spawner.(sandboxPreparer); ok && preparer.NativeCleanupPending() {
		state.mu.Unlock()
		return proto.SpawnResult{}, errNativeClosePending
	}
	ticket := state.pending[p.Ticket]
	if ticket == nil || ticket.expired || !s.now().Before(ticket.created.Add(sandboxPreparationTTL)) {
		state.mu.Unlock()
		return proto.SpawnResult{}, errSandboxTicketExpired
	}
	select {
	case <-ticket.preparing:
	default:
		state.mu.Unlock()
		return proto.SpawnResult{}, errSandboxParams
	}
	if ticket.err != nil {
		state.mu.Unlock()
		return proto.SpawnResult{}, ticket.err
	}
	if p.OperationID != ticket.intent.OperationID || p.LaunchID != ticket.intent.LaunchID || p.Mode != ticket.intent.Mode || (p.Mode == proto.SandboxEnforce) != (p.Grant != nil) {
		state.mu.Unlock()
		return proto.SpawnResult{}, errSandboxMismatch
	}
	if p.Grant != nil && (ticket.prepared == nil || p.Grant.ID <= 0 || p.Grant.Version != sandbox.PolicyVersion || p.Grant.Digest != ticket.prepared.Digest) {
		state.mu.Unlock()
		return proto.SpawnResult{}, errSandboxMismatch
	}
	if len(state.consumed) >= sandboxConsumedLimit {
		state.mu.Unlock()
		return proto.SpawnResult{}, errSandboxCapacity
	}
	delete(state.pending, p.Ticket)
	ticket.launchHash, ticket.consumed = hash, s.now()
	state.consumed[p.Ticket] = ticket
	state.mu.Unlock()

	binding := &proto.SandboxGetResult{OperationID: p.OperationID, LaunchID: p.LaunchID, Mode: p.Mode, Grant: p.Grant, Enforcement: "off", Observer: "unavailable"}
	if p.Mode == proto.SandboxEnforce {
		binding.Enforcement = "enforced"
	}
	shape := p.Shape
	spawn := proto.SpawnParams{Workspace: ticket.intent.Workspace, Cwd: ticket.intent.Cwd, Env: shape.Env, Cols: shape.Cols, Rows: shape.Rows, XPixel: shape.XPixel, YPixel: shape.YPixel, WindowBytes: shape.WindowBytes, RowBufferBytes: shape.RowBufferBytes, ScrollbackLines: shape.ScrollbackLines, Lifecycle: shape.Lifecycle}
	result, err := s.spawnLocal(context.WithoutCancel(ctx), spawn, ticket.prepared, binding)
	if err != nil && ticket.prepared != nil && !errors.Is(err, errNativeClosePending) {
		_ = ticket.prepared.Cleanup()
	}
	state.mu.Lock()
	ticket.result, ticket.err = result, err
	close(ticket.done)
	state.mu.Unlock()
	return result, err
}

func (s *Service) sandboxDiscard(p proto.SandboxDiscardParams) proto.SandboxDiscardResult {
	state := s.sandboxTickets
	state.mu.Lock()
	ticket := state.pending[p.Ticket]
	var prepared *sandbox.Prepared
	if ticket != nil {
		ticket.expired = true
		delete(state.pending, p.Ticket)
		select {
		case <-ticket.preparing:
			prepared = ticket.prepared
		default:
		}
	}
	state.mu.Unlock()
	if prepared != nil {
		_ = prepared.Cleanup()
	}
	return proto.SandboxDiscardResult{}
}

func (s *Service) sandboxGet(p proto.SandboxGetParams) (proto.SandboxGetResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.SandboxGetResult{}, err
	}
	hs.mu.Lock()
	defer hs.mu.Unlock()
	if hs.sandbox == nil {
		return proto.SandboxGetResult{Session: hs.id, Mode: proto.SandboxOff, Enforcement: "ordinary", Observer: "unavailable"}, nil
	}
	result := *hs.sandbox
	result.Session = hs.id
	if hs.exit != nil {
		result.Enforcement = "ended"
	}
	return result, nil
}

func (s *Service) sandboxSweepLoop() {
	state := s.sandboxTickets
	defer close(state.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-state.stop:
			return
		case <-ticker.C:
			var cleanup []*sandbox.Prepared
			state.mu.Lock()
			now := s.now()
			for token, ticket := range state.pending {
				if !now.Before(ticket.created.Add(sandboxPreparationTTL)) {
					ticket.expired = true
					delete(state.pending, token)
					select {
					case <-ticket.preparing:
						if ticket.prepared != nil {
							cleanup = append(cleanup, ticket.prepared)
						}
					default:
					}
				}
			}
			for token, ticket := range state.consumed {
				if !now.Before(ticket.consumed.Add(sandboxConsumedTTL)) {
					select {
					case <-ticket.done:
						delete(state.consumed, token)
					default:
					}
				}
			}
			state.mu.Unlock()
			for _, prepared := range cleanup {
				_ = prepared.Cleanup()
			}
		}
	}
}

func (s *Service) closeSandboxPreparations() {
	state := s.sandboxTickets
	state.once.Do(func() { close(state.stop) })
	<-state.done
	var cleanup []*sandbox.Prepared
	state.mu.Lock()
	state.closing = true
	var inflight []<-chan struct{}
	for _, ticket := range state.consumed {
		select {
		case <-ticket.done:
		default:
			inflight = append(inflight, ticket.done)
		}
	}
	for _, ticket := range state.pending {
		ticket.expired = true
		select {
		case <-ticket.preparing:
			if ticket.prepared != nil {
				cleanup = append(cleanup, ticket.prepared)
			}
		default:
		}
	}
	state.pending = make(map[string]*sandboxTicket)
	state.mu.Unlock()
	for _, done := range inflight {
		<-done
	}
	for _, prepared := range cleanup {
		_ = prepared.Cleanup()
	}
}
