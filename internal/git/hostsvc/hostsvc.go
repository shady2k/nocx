// Package hostsvc is the remote helper's git service: a thin mapping of
// named operations onto internal/git/local (D16). It must not re-derive
// anything local already answers, and the bounds it applies are the
// factory's and the repo's (D9). Status, EnvState and OpenOutcome cross the
// wire as the domain types, JSON-encoded, not as new shapes.
//
// The service holds one Repo per binding id, keyed by the id open returns
// (plan Task 7): open resolves the repository and mints the id; status and
// envState reach their repo through it. A second open of the same directory
// replaces the held repository, closing the old one.
package hostsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/helper/host"
)

// Service is the helper's "git" service.
type Service struct {
	factory git.RepoFactory

	mu    sync.Mutex
	repos map[string]git.Repo
}

// New builds the git service over the given factory. The factory is the
// whole of the service's execution surface; the composition root chooses
// local.NewFactory, and tests may inject a stub.
func New(factory git.RepoFactory) *Service {
	return &Service{factory: factory, repos: make(map[string]git.Repo)}
}

// Name is the reserved service name the host registers it under.
func (s *Service) Name() string { return "git" }

// Ops is the closed set of operations this service serves.
func (s *Service) Ops() []string {
	return []string{
		"open", "status", "envState", "diff", "log",
		"stage", "unstage", "stageAll", "unstageAll", "commit", "headMessage", "remoteURL",
	}
}

// ParamsSchema declares each op's params type; the host decodes a request's
// params through it and audits it for argv at registration (D3).
func (s *Service) ParamsSchema(op string) *host.Schema {
	switch op {
	case "open":
		return host.SchemaFor(OpenParams{})
	case "status", "envState", "stageAll", "unstageAll", "headMessage", "remoteURL":
		return host.SchemaFor(BindingParams{})
	case "diff":
		return host.SchemaFor(DiffParams{})
	case "log":
		return host.SchemaFor(LogParams{})
	case "stage", "unstage":
		return host.SchemaFor(StageParams{})
	case "commit":
		return host.SchemaFor(CommitParams{})
	}
	return nil
}

// Call dispatches one named operation.
func (s *Service) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	switch op {
	case "open":
		return s.open(ctx, params)
	case "status":
		return s.status(ctx, params)
	case "envState":
		return s.envState(ctx, params)
	case "diff":
		return s.diff(ctx, params)
	case "log":
		return s.log(ctx, params)
	case "stage":
		return s.stage(ctx, params)
	case "unstage":
		return s.unstage(ctx, params)
	case "stageAll":
		return s.stageAll(ctx, params)
	case "unstageAll":
		return s.unstageAll(ctx, params)
	case "commit":
		return s.commit(ctx, params)
	case "headMessage":
		return s.headMessage(ctx, params)
	case "remoteURL":
		return s.remoteURL(ctx, params)
	}
	return nil, fmt.Errorf("hostsvc: no op %q", op)
}

// bindingID derives the service-issued id from the verified directory, so a
// second open of the same directory replaces the first binding rather than
// multiplying it.
func bindingID(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:])
}

func (s *Service) open(ctx context.Context, raw json.RawMessage) (any, error) {
	var p OpenParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	repo, outcome, err := s.factory.Open(ctx, p.Cwd)
	if err != nil {
		return nil, err
	}
	if outcome.State != git.OpenOK {
		if repo != nil {
			_ = repo.Close() // a non-ok outcome carries no binding
		}
		return OpenResult{OpenOutcome: outcome}, nil
	}
	if repo == nil {
		return nil, errors.New("hostsvc: ok outcome with a nil repo")
	}
	id := bindingID(p.Cwd)
	s.mu.Lock()
	if old, ok := s.repos[id]; ok {
		_ = old.Close() // replacement: the directory is bound once
	}
	s.repos[id] = repo
	s.mu.Unlock()
	return OpenResult{BindingID: id, OpenOutcome: outcome}, nil
}

// status and envState hold the service lock across their repo call: a
// concurrent open that replaces a binding closes the old repo, and the seam
// says Close may release real resources, so a repo must never be used after
// its replacement closes it. The helper's control channel serializes git ops
// per service, which is the price of that guarantee.
func (s *Service) status(ctx context.Context, raw json.RawMessage) (any, error) {
	var p BindingParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	st, err := repo.Status(ctx)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return normalizeStatus(st), nil
}

func (s *Service) envState(ctx context.Context, raw json.RawMessage) (any, error) {
	var p BindingParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	state, reason := repo.EnvState()
	s.mu.Unlock()
	return EnvStateResult{State: state, Reason: reason}, nil
}

// diff and log hold the service lock across their repo call, exactly as
// status does: a concurrent open that replaces a binding closes the old
// repo, and the seam says Close may release real resources. The bound the
// caller names is applied by the repo, where the work happens (D9) — the
// bytes beyond it never reach this side of the wire.
func (s *Service) diff(ctx context.Context, raw json.RawMessage) (any, error) {
	var p DiffParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	d, err := repo.Diff(ctx, p.Path, p.Side, p.MaxBytes)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) log(ctx context.Context, raw json.RawMessage) (any, error) {
	var p LogParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	lg, err := repo.Log(ctx, p.Max)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return normalizeLog(lg), nil
}

// ── mutations (plan Task 8) ────────────────────────────────────────────

// The mutation ops hold the service lock across their repo call, exactly
// as the reads do: a concurrent open that replaces a binding closes the
// old repo, and the seam says Close may release real resources. The
// helper's control channel serializes git ops per service, which is the
// price of that guarantee.

func (s *Service) stage(ctx context.Context, raw json.RawMessage) (any, error) {
	var p StageParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	st, err := repo.Stage(ctx, p.Paths)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return normalizeStatus(st), nil
}

func (s *Service) unstage(ctx context.Context, raw json.RawMessage) (any, error) {
	var p StageParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	st, err := repo.Unstage(ctx, p.Paths)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return normalizeStatus(st), nil
}

func (s *Service) stageAll(ctx context.Context, raw json.RawMessage) (any, error) {
	var p BindingParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	st, err := repo.StageAll(ctx)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return normalizeStatus(st), nil
}

func (s *Service) unstageAll(ctx context.Context, raw json.RawMessage) (any, error) {
	var p BindingParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	st, err := repo.UnstageAll(ctx)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return normalizeStatus(st), nil
}

func (s *Service) commit(ctx context.Context, raw json.RawMessage) (any, error) {
	var p CommitParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	out, err := repo.Commit(ctx, p.Message, p.Amend)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return normalizeOutcome(out), nil
}

// headMessage is the Amend prefill: the full HEAD message, fetched once
// when the box is ticked. An unborn branch has no message to prefill —
// that is the "none" state inside the domain result, not an error.
func (s *Service) headMessage(ctx context.Context, raw json.RawMessage) (any, error) {
	var p BindingParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	hm, err := repo.HeadMessage(ctx)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return hm, nil
}

// remoteURL is the "open on its hosting" fact: the URL of the remote the
// current branch tracks, derived by git — never guessed. A detached HEAD,
// a branch with no upstream or a deleted remote answer ErrNoRemote, which
// the service codes as itself so the backend renders the ordinary
// "no link to draw" state.
func (s *Service) remoteURL(ctx context.Context, raw json.RawMessage) (any, error) {
	var p BindingParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	repo, ok := s.repos[p.BindingID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("hostsvc: no binding %q", p.BindingID)
	}
	url, err := repo.RemoteURL(ctx)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return url, nil
}

// normalizeOutcome applies the wire guard to the post-commit status: the
// outcome's lists are never nil, the same contract the status result
// carries.
func normalizeOutcome(out git.CommitOutcome) git.CommitOutcome {
	out.Status = normalizeStatus(out.Status)
	return out
}

// ── D11 and the domain refusals ────────────────────────────────────────

// mutationOps are the operations that refuse cancellation (D11): the
// panel's mutations run to completion once started, because half-applying
// a commit is worse than waiting for it. headMessage and remoteURL are
// reads and stay cancellable (D10).
var mutationOps = map[string]bool{
	"stage": true, "unstage": true, "stageAll": true, "unstageAll": true,
	"commit": true,
}

// RefusesCancel reports whether a cancel naming op must be refused (D11).
func (s *Service) RefusesCancel(op string) bool { return mutationOps[op] }

// The git service's refusal codes — the machine-readable half of a
// refusal that must reach the backend AS the typed error (D11/D12): the
// transport switches on *git.ErrNothingToCommit, *git.ErrAmendUnborn and
// *git.ErrConflicted, so the wire carries the code and the backend

const (
	ErrCodeNothingToCommit = "git.nothing_to_commit"
	ErrCodeAmendUnborn     = "git.amend_unborn"
	ErrCodeConflicted      = "git.conflicted"
	ErrCodeNoRemote        = "git.no_remote"
)

func (s *Service) Refusal(err error) (string, json.RawMessage) {
	var conflicted *git.ErrConflicted
	if errors.As(err, &conflicted) {
		return ErrCodeConflicted, mustJSON(conflicted)
	}
	var nothing *git.ErrNothingToCommit
	if errors.As(err, &nothing) {
		return ErrCodeNothingToCommit, nil
	}
	var amend *git.ErrAmendUnborn
	if errors.As(err, &amend) {
		return ErrCodeAmendUnborn, nil
	}
	var noRemote *git.ErrNoRemote
	if errors.As(err, &noRemote) {
		return ErrCodeNoRemote, nil
	}
	return "", nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// normalizeLog is the log half of the same wire guard: Entries is never nil
// — an empty history marshals as [], never null.
func normalizeLog(lg git.Log) git.Log {
	if lg.Entries == nil {
		lg.Entries = []git.LogEntry{}
	}
	return lg
}

// normalizeStatus is the service boundary's wire guard: the domain contract
// says the three lists are never nil — an empty set marshals as [], never
// null, the defect the first contract schema in this repository caught —
// and the boundary is where that guarantee is earned, whatever the
// implementation hands back.
func normalizeStatus(st git.Status) git.Status {
	if st.Staged == nil {
		st.Staged = []git.Entry{}
	}
	if st.Unstaged == nil {
		st.Unstaged = []git.Entry{}
	}
	if st.Conflicted == nil {
		st.Conflicted = []git.Entry{}
	}
	return st
}
