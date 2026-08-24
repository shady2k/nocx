package transport

// The git.* control plane (spec §5.2): ten JSON-RPC methods backed by
// internal/git, plus the git.changed notification.
//
// Two guards, and they are the point of this file, exactly as in ws_files.go:
//
//  1. git.open is authorised by connState (D15) — a connection can open a
//     repository only for a session it has opened or reattached to. The
//     global session registry is never the answer: resolving a sessionId
//     through it would let any authenticated socket that learned another
//     connection's session id open that session's repository — and a
//     Commit button aimed at the wrong repository is a corrupted history,
//     not a nuisance (design §0).
//  2. Every later call re-checks, in exactly one place: Registry.Acquire
//     re-checks that the binding's session is in the REQUESTING
//     connection's connState, and takes the use-guard that keeps the
//     binding alive for the call. A handler cannot forget a check it
//     never performs.
//
// The change notification's addressing is the interesting half: the
// destination is resolved at emit time — the binding's session's current
// subscriber, never the connection that opened the binding, which is
// destroyed on a WebSocket drop. That is what survives an AD-9 reconnect:
// bindings are bounded by the session, not the WebSocket, so a reconnect
// changes nothing and the client keeps using its bindingId. The ONE
// exception is session teardown, where emit-time lookup finds nobody —
// both teardown paths remove the session's receiver before they clean up
// bindings — so the subscriber is captured BEFORE removal and handed to
// gitSessionClosed as a parameter (spec §5.2; this is the mechanism
// behind the open bug nocx-lzfb, and the fix its notification needs).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ── git.* ingress bounds and validators (the per-field sweep) ─────────────
//
// Every git.* method carries a binding id or a path, and every path is a
// REPOSITORY-RELATIVE pathspec (D8) — never an absolute filesystem path:
// git.diff rides the path in argv behind `--`, git.stage/git.unstage feed
// NUL-separated :(literal) pathspecs to stdin. So the pathspec rule here is
// git's, not the filesystem provider's: non-empty, NUL-free, bounded. The
// files.* side (ws_files.go) holds the absolute+clean rule its providers own.

// decodeParams decodes one method's params object, answering the -32602
// message for params that are absent or not a JSON object, and "" on
// success. The decode shape of the exemplar (validateProbeParamsRaw),
// shared by every validator in this sweep so none of them can forget the
// object gate.
func decodeParams(raw json.RawMessage, out any) string {
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return "params must be a JSON object"
	}
	return ""
}

// isLowerHex reports whether s is exactly n lowercase hexadecimal digits —
// the shape every backend-minted id in this surface has: session ids and
// binding ids are hex.EncodeToString of 16 random bytes (session.NewID, the
// git/filesystem binding mints), recovery generations of a 32-byte nonce.
func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := range n {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

const (
	// maxPathspecRunes bounds one git pathspec: a repo-relative path. A
	// pathspec must resolve inside the worktree, whose components sit under
	// the OS PATH_MAX (4096 bytes), so 4096 runes is the honest ceiling —
	// the same number the ask path bounds a renderer cwd by (maxCwdRunes).
	maxPathspecRunes = 4_096
	// maxGitDiffBytes bounds git.diff's maxBytes — the ONLY bound on the
	// diff byteSink (internal/git/local: the retained text is a prefix cut
	// at this value), so an unbounded value is an unbounded memory hazard.
	// The frontend's own surface names 1 MiB (git-diff-content.tsx
	// DIFF_MAX_BYTES) and the wire conformance test exercises 1 MiB; 4 MiB
	// is the backend's wire-cost ceiling, far above any real request.
	maxGitDiffBytes = 4 << 20
	// maxCommitMessageRunes bounds the git.commit message. The message rides
	// stdin (-F -, D8), so this is a wire-cost ceiling rather than a git
	// limit — but subject+body text a human types never approaches 16 KiB,
	// and the bound is well below the frame budget.
	maxCommitMessageRunes = 16_384
	// maxStagePaths bounds the number of paths one git.stage / git.unstage
	// call may carry. The panel can only offer the rows the status list
	// reported, and that list is capped at git.MaxStatusEntries — the same
	// ceiling, so a caller cannot exceed what the product can show.
	maxStagePaths = git.MaxStatusEntries
)

// validatePathspec checks one repo-relative git pathspec. NUL is the one
// character that breaks the framing: stage/unstage feed NUL-separated
// records to --pathspec-from-file, and diff's argv cannot carry a NUL at
// all. Control characters other than NUL are legitimate in a filename
// (tab, newline) — git handles them verbatim under :(literal) — so only
// NUL and the length are checked.
func validatePathspec(path string) string {
	if path == "" {
		return "path is required"
	}
	if strings.ContainsRune(path, '\x00') {
		return "path must not contain NUL"
	}
	if n := utf8.RuneCountInString(path); n > maxPathspecRunes {
		return fmt.Sprintf("path exceeds %d characters", maxPathspecRunes)
	}
	return ""
}

// validateGitOpenRaw checks git.open: the session the requesting connection
// must own, and the optional D2 cwd override that becomes the directory git
// resolves.
func validateGitOpenRaw(raw json.RawMessage) string {
	var p gitOpenParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.SessionID, 32) {
		return "sessionId is required and must be the 32-hex id the backend minted"
	}
	// cwd is the verified OSC 7 cwd (D2): a directory git rev-parses. It is
	// optional — absent is the noCwd RESULT state — and when present it must
	// be absolute: a relative cwd would make rev-parse run in the backend's
	// process-relative directory, which could resolve the wrong repository.
	if p.Cwd != "" {
		if !filepath.IsAbs(p.Cwd) {
			return "cwd must be an absolute path"
		}
		if utf8.RuneCountInString(p.Cwd) > maxCwdRunes {
			return "cwd exceeds the length bound"
		}
	}
	return ""
}

// validateGitBindingRaw checks the binding-only methods (status, headMessage,
// log, remote, close, stageAll, unstageAll): the 32-hex binding id the
// registry minted at git.open. Ownership of that binding is re-checked per
// call by Acquire (D15); this is the shape and presence half.
func validateGitBindingRaw(raw json.RawMessage) string {
	var p gitBindingParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	return ""
}

// validateGitDiffRaw checks git.diff: the binding, the one pathspec, the
// closed side enum, and the byte bound that is the only ceiling on the diff
// sink.
func validateGitDiffRaw(raw json.RawMessage) string {
	var p gitDiffParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	if msg := validatePathspec(p.Path); msg != "" {
		return msg
	}
	switch p.Side {
	case string(git.SideStaged), string(git.SideUnstaged), string(git.SideUntracked):
	default:
		return "side must be one of staged, unstaged, untracked"
	}
	// maxBytes is the sink's cut point: zero or negative cannot bound
	// anything (the domain refuses them too, as an error), and the ceiling
	// keeps a hostile value from making the sink accumulate without bound.
	if p.MaxBytes <= 0 {
		return "maxBytes must be a positive byte bound"
	}
	if p.MaxBytes > maxGitDiffBytes {
		return fmt.Sprintf("maxBytes exceeds %d bytes", maxGitDiffBytes)
	}
	return ""
}

// validateGitStageRaw checks git.stage and git.unstage (one wire shape):
// the binding and the pathspec set. An empty paths array is the deliberate
// no-op that returns the current status (D19) — never "all" — so only a
// non-empty set is bounded by count.
func validateGitStageRaw(raw json.RawMessage) string {
	var p gitStageParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	if len(p.Paths) > maxStagePaths {
		return fmt.Sprintf("paths exceeds %d entries", maxStagePaths)
	}
	for _, path := range p.Paths {
		if msg := validatePathspec(path); msg != "" {
			return msg
		}
	}
	return ""
}

// validateGitCommitRaw checks git.commit: the binding, the message and the
// amend flag. A message with no non-whitespace is refused here rather than
// surfacing as a confusing failed RESULT state — git commit -F - aborts on
// an empty stdin, and the panel's commit button is disabled until a subject
// is typed, so an empty message is a broken caller, never a commit. Newlines
// and quotes are the normal case (D8); only NUL is refused.
func validateGitCommitRaw(raw json.RawMessage) string {
	var p gitCommitParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	if strings.TrimSpace(p.Message) == "" {
		return "message is required and must not be empty"
	}
	if strings.ContainsRune(p.Message, '\x00') {
		return "message must not contain NUL"
	}
	if utf8.RuneCountInString(p.Message) > maxCommitMessageRunes {
		return fmt.Sprintf("message exceeds %d characters", maxCommitMessageRunes)
	}
	return ""
}

// gitBinding is the transport's bookkeeping for one binding it issued.
// internal/git exposes neither a binding's session nor anything else the
// notification addressing needs, so the transport records what it itself
// handed to Register at git.open.
type gitBinding struct {
	sessionID session.ID
}

// ── wire shapes (contracts/git.*.schema.json) ──────────────────────────

type gitOpenParams struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd,omitempty"`
}

// gitOpenResult is the open outcome table (spec §5.1). state is the
// discriminator: every outcome is a RESULT state, never a JSON-RPC error,
// because each one is something the panel can render. The optional fields
// are present iff their state needs them; status rides the open result so
// an open is one round trip (spec §5.2).
type gitOpenResult struct {
	State      string         `json:"state"`
	BindingID  string         `json:"bindingId,omitempty"`  // present iff ok
	Toplevel   string         `json:"toplevel,omitempty"`   // present iff ok
	GitVersion string         `json:"gitVersion,omitempty"` // present when the probe ran
	EnvState   string         `json:"envState,omitempty"`   // "resolved" | "degraded" (D6)
	EnvReason  string         `json:"envReason,omitempty"`  // why degraded; present when degraded
	Status     *gitStatusWire `json:"status,omitempty"`     // first status; absent when the inline read failed
}

type gitBindingParams struct {
	BindingID string `json:"bindingId"`
}

type gitStageParams struct {
	BindingID string   `json:"bindingId"`
	Paths     []string `json:"paths"`
}

// gitStatusResult is the {status} shape git.stage, git.stageAll and
// git.unstageAll answer with (spec §5.2). git.status answers with
// gitStatusPollResult — the same status plus the environment fact, because
// the poll is the repeating channel (nocx-69ey).
type gitStatusResult struct {
	Status gitStatusWire `json:"status"`
}

// gitStatusPollResult is git.status's shape: the status plus the current
// environment fact (D6). Open reports the fact once; the poll repeats it
// because Open's answer is provisional (nocx-6pz0) — it reports whatever
// has settled by open, which in the pre-settle window is degraded — and a
// one-shot fact the panel can never correct would warn about a degradation
// that no longer exists, the exact inversion of D6's purpose (nocx-69ey).
// envState is always present; envReason is present exactly when envState is
// degraded.
type gitStatusPollResult struct {
	Status    gitStatusWire `json:"status"`
	EnvState  string        `json:"envState"`            // "resolved" | "degraded" (D6)
	EnvReason string        `json:"envReason,omitempty"` // why degraded; present when degraded
}

// gitUnstageResult is git.unstage's shape, which is a union where
// git.stage's is not: individual unstaging on an unborn branch fails with
// git's own error, and that failure must arrive as a state the panel can
// render rather than as a transport error (brief, worker A's item 2; D19
// only guarantees unstage-all there). Both branches carry the fresh status
// so the panel repaints from reality.
type gitUnstageResult struct {
	State  string        `json:"state"` // "ok" | "unborn"
	Status gitStatusWire `json:"status"`
}

type gitDiffParams struct {
	BindingID string `json:"bindingId"`
	Path      string `json:"path"`
	Side      string `json:"side"` // enum staged | unstaged | untracked
	MaxBytes  int64  `json:"maxBytes"`
}

type gitDiffResult struct {
	State     string `json:"state"` // enum ok | binary | tooLarge | empty | gone
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type gitCommitParams struct {
	BindingID string `json:"bindingId"`
	Message   string `json:"message"`
	Amend     bool   `json:"amend"`
}

type gitCommitResult struct {
	State           string         `json:"state"` // "ok" | "failed"
	Head            string         `json:"head,omitempty"`
	Output          string         `json:"output,omitempty"`
	OutputTruncated bool           `json:"outputTruncated"`
	Status          *gitStatusWire `json:"status,omitempty"` // absent when failed or stale
	StatusStale     bool           `json:"statusStale,omitempty"`
}

type gitHeadMessageResult struct {
	State   string `json:"state"` // "ok" | "none"
	Message string `json:"message,omitempty"`
}

type gitCloseResult struct {
	Closed bool `json:"closed"`
}

// gitRemoteResult is git.remote's shape (brief, nocx-hc0m): the URL of the
// remote the current branch tracks, or the none state. none is the ordinary
// answer — detached HEAD, no upstream, a deleted remote, a local-path
// remote — and the panel draws no link (D14); it is never an error.
type gitRemoteResult struct {
	State string `json:"state"` // "ok" | "none"
	URL   string `json:"url,omitempty"`
}

type gitChangedParams struct {
	BindingID string `json:"bindingId"`
	Reason    string `json:"reason"` // exactly one value: "sessionClosed"
}

// ── the status wire shape ────────────────────────────────────────────────

type gitEntryWire struct {
	Path string `json:"path"`
	X    string `json:"x"`
	Y    string `json:"y"`
	// Added and Deleted are the numstat line counts for this entry on its
	// side, omitted when no count exists — an untracked file, a binary
	// file, a conflicted entry, or a count read that was bounded out
	// (design D9, brief nocx-i4ki). Omitted is NOT zero: a real 0/0 answer
	// (a pure rename, an empty file) marshals as 0, so the wire uses
	// pointers, never omitempty on an int.
	Added   *int `json:"added,omitempty"`
	Deleted *int `json:"deleted,omitempty"`
}

type gitStatusWire struct {
	Branch       string         `json:"branch"`
	Detached     bool           `json:"detached"`
	Unborn       bool           `json:"unborn"`
	Head         string         `json:"head"`
	Upstream     string         `json:"upstream"`
	Ahead        int            `json:"ahead"`
	Behind       int            `json:"behind"`
	Staged       []gitEntryWire `json:"staged"`
	Unstaged     []gitEntryWire `json:"unstaged"`
	Conflicted   []gitEntryWire `json:"conflicted"`
	Total        int            `json:"total"`
	Completeness string         `json:"completeness"`
}

type gitLogEntryWire struct {
	Hash       string   `json:"hash"`
	ShortHash  string   `json:"shortHash"`
	Subject    string   `json:"subject"`
	AuthorName string   `json:"authorName"`
	AuthoredAt string   `json:"authoredAt"`
	Refs       []string `json:"refs"`
}

type gitLogWire struct {
	Entries      []gitLogEntryWire `json:"entries"`
	Total        int               `json:"total"`
	Completeness string            `json:"completeness"`
}

type gitLogResult struct {
	Log gitLogWire `json:"log"`
}

// wireGitLog maps the domain Log onto the contracted wire shape. Entries
// start non-nil in the domain; a regression there is exactly what the
// contract test is designed to catch.
func wireGitLog(lg git.Log) gitLogWire {
	entries := make([]gitLogEntryWire, 0, len(lg.Entries))
	for _, e := range lg.Entries {
		entries = append(entries, gitLogEntryWire{
			Hash:       e.Hash,
			ShortHash:  e.ShortHash,
			Subject:    e.Subject,
			AuthorName: e.AuthorName,
			AuthoredAt: e.AuthoredAt.Format(time.RFC3339),
			Refs:       e.Refs,
		})
	}
	return gitLogWire{Entries: entries, Total: lg.Total, Completeness: string(lg.Completeness)}
}

// wireGitStatus maps the domain Status onto the contracted wire shape. The
// mapping is deliberately pure: the domain guarantees the lists are never
// nil (git.go), and a regression there is exactly what the contract test
// is designed to catch.
func wireGitStatus(st git.Status) gitStatusWire {
	entries := func(es []git.Entry) []gitEntryWire {
		out := make([]gitEntryWire, 0, len(es))
		for _, e := range es {
			out = append(out, gitEntryWire{
				Path:    e.Path,
				X:       string([]byte{e.X}),
				Y:       string([]byte{e.Y}),
				Added:   e.Added,
				Deleted: e.Deleted,
			})
		}
		return out
	}
	return gitStatusWire{
		Branch:       st.Branch,
		Detached:     st.Detached,
		Unborn:       st.Unborn,
		Head:         st.Head,
		Upstream:     st.Upstream,
		Ahead:        st.Ahead,
		Behind:       st.Behind,
		Staged:       entries(st.Staged),
		Unstaged:     entries(st.Unstaged),
		Conflicted:   entries(st.Conflicted),
		Total:        st.Total,
		Completeness: string(st.Completeness),
	}
}

// ── binding bookkeeping seam ───────────────────────────────────────────────

// gitBindingsSeam is the transport-owned git binding bookkeeping surface the
// git handlers are constructed with: the transport records which session each
// binding belongs to (gitBindings/gitBySession, under gitMu) because
// internal/git exposes neither a binding's session nor anything else the
// notification addressing needs. git.open records at mint time; git.close
// forgets. The git.changed emission and the session-teardown cleanup
// (gitSessionClosed) stay in the transport — they are shared lifecycle, not a
// handler capability (migration map, close finding).
type gitBindingsSeam interface {
	recordBinding(bid string, sid session.ID)
	forgetBinding(bid string)
}

// recordBinding records that the registry just minted bid for sid — the
// transport's own bookkeeping, kept here so a handler constructed without a
// *WSServer still records the binding it handed to Register.
func (s *WSServer) recordBinding(bid string, sid session.ID) {
	s.gitMu.Lock()
	s.gitBindings[bid] = &gitBinding{sessionID: sid}
	set := s.gitBySession[sid]
	if set == nil {
		set = make(map[string]struct{})
		s.gitBySession[sid] = set
	}
	set[bid] = struct{}{}
	s.gitMu.Unlock()
}

// forgetBinding drops bid from the transport's bookkeeping, the mirror of
// recordBinding. It is a no-op for an unknown binding.
func (s *WSServer) forgetBinding(bid string) {
	s.gitMu.Lock()
	b := s.gitBindings[bid]
	delete(s.gitBindings, bid)
	if b != nil {
		if set := s.gitBySession[b.sessionID]; set != nil {
			delete(set, bid)
			if len(set) == 0 {
				delete(s.gitBySession, b.sessionID)
			}
		}
	}
	s.gitMu.Unlock()
}

// gitOpenHandlers answers git.open. It holds the GitOpenOperation (session +
// git gates — the session resolve and the open+register run inside the
// callback), the Responder, the transport's binding bookkeeping seam and a
// logger. It needs the connection's connState per call: git.open is
// authorised by connState (D15), and the inline first status acquires with
// it as the caller. wired distinguishes the two not-available answers:
// false is the whole git plane missing ("git not available"); true with a
// nil operation is a registry wired without a repo factory, which leaves
// only git.open unavailable.
type gitOpenHandlers struct {
	op       capability.GitOpenOperation
	r        Responder
	bindings gitBindingsSeam
	log      log.Logger
	wired    bool
}

// handleOpen resolves a session the requesting connection owns and
// registers a repository for it, minting the binding every later git.*
// call carries. sessionId appears exactly once on the wire — here (D1) —
// and the authorisation is connState's, not the global registry's (D15).
//
// noCwd and remoteUnsupported are decided HERE, from the session's origin,
// before the factory is invoked; the factory itself answers ok,
// notARepository, gitUnavailable or gitTooOld (spec §5.1). The remote
// refusal is a RESULT state, not an error: on an SSH tab the panel shows
// one honest state and offers nothing (D3, D14).
func (h gitOpenHandlers) handleOpen(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		if h.wired {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "git.open not available (no repo factory wired)"})
			return
		}
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitOpenParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: sessionId required"})
		return
	}
	sid := session.ID(params.SessionID)
	if !state.has(sid) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitOpenService) error {
		sess, err := svc.Get(sid)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
			return nil
		}
		if sess.Kind() != session.KindLocal {
			// D3: the remote case waits for the relay (nocx-if6 phase B).
			_ = h.r.TryResult(req.ID, mustMarshal(gitOpenResult{State: string(git.OpenRemoteUnsupported)}))
			return nil
		}
		if params.Cwd == "" {
			// No verified OSC 7 cwd to resolve from (D2).
			_ = h.r.TryResult(req.ID, mustMarshal(gitOpenResult{State: string(git.OpenNoCwd)}))
			return nil
		}
		// OpenBinding owns the ownership-transfer rule (spec §5.1): a live
		// repo on a refusing outcome is closed before the refusal is
		// returned, a Register failure closes the repo, and after Register
		// succeeds the registry owns it.
		bid, outcome, err := svc.OpenBinding(ctx, sid, params.Cwd)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		if outcome.State != git.OpenOK {
			_ = h.r.TryResult(req.ID, mustMarshal(gitOpenResult{
				State:      string(outcome.State),
				GitVersion: outcome.GitVersion,
			}))
			return nil
		}
		// The binding is now the registry's; the transport records which
		// session it belongs to — the bookkeeping the notification
		// addressing needs.
		h.bindings.recordBinding(bid, sid)

		// The first status rides the open result (spec §5.2) — otherwise
		// every open is two round trips and a guaranteed frame of empty
		// lists. A failed inline read is not an open failure: the binding
		// is live and the panel's first poll retries, so the status is
		// omitted rather than failing the open.
		var st *gitStatusWire
		if hnd, release, aerr := svc.Acquire(bid, state); aerr == nil {
			status, serr := hnd.Status(ctx)
			release()
			if serr == nil {
				wire := wireGitStatus(status)
				st = &wire
			} else {
				h.log.Debug("git.open: inline status failed", "binding_id", bid, "error", serr)
			}
		}
		_ = h.r.TryResult(req.ID, mustMarshal(gitOpenResult{
			State:      string(git.OpenOK),
			BindingID:  bid,
			Toplevel:   outcome.Toplevel,
			GitVersion: outcome.GitVersion,
			EnvState:   string(outcome.EnvState),
			EnvReason:  outcome.EnvReason,
			Status:     st,
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// gitBindingHandlers answers the git.* binding methods — status, diff,
// stage, unstage, stageAll, unstageAll, commit, headMessage, log, remote,
// close. Each call acquires the binding per call through the
// GitBindingOperation (git gate) with the connection's connState as the
// caller: bindings close at any moment, so validity is checked per call, not
// at construction, and Registry.Acquire re-checks that the binding's session
// is in the REQUESTING connection's connState (D15) in exactly one place. It
// holds the operation, the Responder and the transport's binding
// bookkeeping seam (close forgets the binding); nothing else.
type gitBindingHandlers struct {
	op       capability.GitBindingOperation // nil → git not wired
	r        Responder
	bindings gitBindingsSeam
}

// handleStatus answers "what changed in this repository" — the poll the
// panel runs while visible (spec §5.4, D13). A status on an unknown or
// already-closed binding answers the unknownBinding error, never a panic:
// Acquire either finds the binding and takes the use-guard, or returns the
// domain error, and the handler maps it onto the wire.
func (h gitBindingHandlers) handleStatus(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitBindingParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		hnd, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		defer release()
		status, err := hnd.Status(ctx)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		// The environment fact rides the poll (nocx-69ey): the panel switches
		// on it, and only a repeating channel can withdraw a warning Open
		// showed for the pre-settle window. The domain answers — never the
		// shell (D16).
		envState, envReason := hnd.EnvState()
		_ = h.r.TryResult(req.ID, mustMarshal(gitStatusPollResult{
			Status:    wireGitStatus(status),
			EnvState:  string(envState),
			EnvReason: envReason,
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleDiff diffs one file on one side. side is a closed enum — the
// three diff forms (spec §5.1 "diff.go") — and the outcome is the four
// RESULT states, never an error: a row can be clicked in the same second
// an agent reverts the file (empty, gone), a binary file has nothing to
// render (binary), and the byte bound is a state, not a failure (tooLarge).
func (h gitBindingHandlers) handleDiff(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitDiffParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" || params.Path == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId and path required"})
		return
	}
	if params.Side != string(git.SideStaged) && params.Side != string(git.SideUnstaged) && params.Side != string(git.SideUntracked) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: side must be staged, unstaged or untracked"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		hnd, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		defer release()
		d, err := hnd.Diff(ctx, params.Path, git.Side(params.Side), params.MaxBytes)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(gitDiffResult{
			State:     string(d.State),
			Text:      d.Text,
			Truncated: d.Truncated,
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleStage stages exactly the given paths (D8: paths ride stdin as a
// pathspec stream, never argv) and returns the fresh status (D12). paths[]
// never means "all": an empty array is a no-op that still returns the
// current status, and "all" is git.stageAll (D19).
func (h gitBindingHandlers) handleStage(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitStageParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		hnd, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		defer release()
		var status git.Status
		if len(params.Paths) == 0 {
			status, err = hnd.Status(ctx)
		} else {
			status, err = hnd.Stage(ctx, params.Paths)
		}
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(gitStatusResult{Status: wireGitStatus(status)}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleUnstage unstages exactly the given paths. It is the one
// mutation whose failure is a RESULT state rather than a transport error:
// individual unstaging on an unborn branch fails with git's own error (git
// reset with pathspecs resolves HEAD, which an unborn branch lacks — D19
// only guarantees unstage-all there). The discriminator is the branch's
// unbornness RE-READ from the repository, never parsed from git's prose
// (D11): when the unstage fails and a fresh status says the branch is
// unborn, the answer is state "unborn" with that fresh status, and the
// panel repaints and stops offering the control.
func (h gitBindingHandlers) handleUnstage(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitStageParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		hnd, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		defer release()
		var status git.Status
		if len(params.Paths) == 0 {
			status, err = hnd.Status(ctx)
		} else {
			status, err = hnd.Unstage(ctx, params.Paths)
		}
		if err != nil {
			st, serr := hnd.Status(ctx)
			if serr == nil && st.Unborn {
				_ = h.r.TryResult(req.ID, mustMarshal(gitUnstageResult{
					State:  "unborn",
					Status: wireGitStatus(st),
				}))
				return nil
			}
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(gitUnstageResult{
			State:  "ok",
			Status: wireGitStatus(status),
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleStageAll stages everything (git add -A, D19) and returns the
// fresh status. While any entry is conflicted it is refused — the
// ErrConflicted domain error, which the panel renders as a visible refusal
// with the reason (a button that resolved conflicts by accident is the
// measured hazard D19 exists to prevent).
func (h gitBindingHandlers) handleStageAll(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitBindingParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		hnd, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		defer release()
		status, err := hnd.StageAll(ctx)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(gitStatusResult{Status: wireGitStatus(status)}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleUnstageAll unstages everything — bare git reset, no HEAD, no
// pathspec — which is what makes it work on an unborn branch (D19,
// measured; no special unborn path is needed or built). It is refused
// while any entry is conflicted, like stage-all.
func (h gitBindingHandlers) handleUnstageAll(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitBindingParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		hnd, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		defer release()
		status, err := hnd.UnstageAll(ctx)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(gitStatusResult{Status: wireGitStatus(status)}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleCommit commits with the message on stdin (-F -, D8) and returns
// the outcome: ok with the new head and the fresh status, or failed with
// git's own account (D11 — we do not classify why). Hooks always run;
// there is no --no-verify.
func (h gitBindingHandlers) handleCommit(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitCommitParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		hnd, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		defer release()
		outcome, err := hnd.Commit(ctx, params.Message, params.Amend)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		res := gitCommitResult{
			State:           string(outcome.State),
			Head:            outcome.Head,
			Output:          outcome.Output,
			OutputTruncated: outcome.OutputTruncated,
			StatusStale:     outcome.StatusStale,
		}
		if outcome.State == git.CommitOK && !outcome.StatusStale {
			wire := wireGitStatus(outcome.Status)
			res.Status = &wire
		}
		_ = h.r.TryResult(req.ID, mustMarshal(res))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleHeadMessage is the Amend prefill (spec §5.2): the full HEAD
// message, fetched once when the box is ticked. An unborn branch has no
// HEAD message to amend — that is the "none" state, not an error (local
// maps it); an invocation that cannot be made is the error.
func (h gitBindingHandlers) handleHeadMessage(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitBindingParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		hnd, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		defer release()
		hm, err := hnd.HeadMessage(ctx)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(gitHeadMessageResult{
			State:   string(hm.State),
			Message: hm.Message,
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleLog answers "what has happened on this branch": the first
// MaxLogEntries commits of HEAD, newest first (brief, git.log). History
// does not change under the user the way the working tree does, so the
// panel reads it when it opens, on manual refresh and after a commit —
// never on the poll (D13). The bound is policy: the implementation asks
// git for one more than the cap, so the answer can say capped rather than
// implying the branch has exactly N commits (D9).
func (h gitBindingHandlers) handleLog(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitBindingParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		hnd, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		defer release()
		lg, err := hnd.Log(ctx, git.MaxLogEntries)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(gitLogResult{Log: wireGitLog(lg)}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleRemote answers "what URL does the branch I am on track"
// (brief, nocx-hc0m): the raw remote URL, derived by Repo.RemoteURL from
// HEAD and git's own upstream atom — never parsed from a client-supplied
// branch. The none state is the ordinary answer — detached HEAD, no
// upstream, a deleted remote, a local-path remote — and the panel draws no
// link for it (D14); only an invocation that could not be made is an error.
// The URL conversion to a host's web page is the renderer's, in one module
// with its own tests: the wire carries what git said, not a URL the backend
// invented for a host it may not know.
func (h gitBindingHandlers) handleRemote(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitBindingParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		hnd, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		defer release()
		url, err := hnd.RemoteURL(ctx)
		if err != nil {
			var noRemote *git.ErrNoRemote
			if errors.As(err, &noRemote) {
				_ = h.r.TryResult(req.ID, mustMarshal(gitRemoteResult{State: "none"}))
				return nil
			}
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(gitRemoteResult{State: "ok", URL: url}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleClose closes the binding: its repository released, the use-guard
// drained, the transport's bookkeeping forgotten. Ownership is re-checked
// like every call (D15) — a binding is closed by the connection that owns
// its session, not by whoever knows its id.
func (h gitBindingHandlers) handleClose(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "git not available"})
		return
	}
	var params gitBindingParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.BindingID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: bindingId required"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.GitBindingService) error {
		_, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		release()

		h.bindings.forgetBinding(params.BindingID)
		if err := svc.Close(params.BindingID); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: gitErrorCode(err), Message: err.Error(), Data: gitErrorReason(err)})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(gitCloseResult{Closed: true}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// gitSessionClosed tears down every git binding of a session and tells that
// session's subscriber the bindings are gone — closing the terminal closes
// its bindings (spec §5.5): a binding is bounded by its session, never by a
// WebSocket.
//
// wconn is the subscriber CAPTURED before the receiver was removed — the
// one thing that makes this notification deliverable at all (spec §5.2).
// Both teardown paths remove the session's receiver before they clean up
// bindings, so an emit-time lookup at this moment finds nobody; that is
// the mechanism behind the open bug nocx-lzfb, and the capture is the fix
// its notification needs. Emit-time resolution remains the rule for a live
// session (that is what survives an AD-9 reconnect); the captured
// subscriber is the rule for the one moment when the session is being torn
// down and there is nothing left to look up. wconn is nil when no
// subscriber was attached (the app-shutdown path), and there is then
// nobody to tell.
//
// The interval, both ends: the registry removes the bindings BEFORE the
// notification is written, so no call can acquire a binding after a client
// has been told it is gone; Close then drains whatever was already in
// flight (D18), and an in-flight call that loses the race answers
// unknownBinding, which is the correct answer.
//
// The write is asynchronous and non-blocking, deliberately: a subscriber
// that stopped reading while its socket stays open must not hang whatever
// wrote it — which on the explicit-close path is the READ LOOP. The
// enqueue is one channel send (the outbound pump owns the socket); the
// frame is dropped if the connection is stalled or closed.
// The subscriber stays a *wsConn rather than a Responder, and that is not an
// oversight left over from the migration. Both callers get it from
// rx.getSubscriber(), which returns a nil *wsConn when nobody is attached —
// and a nil *wsConn stored in an interface is NOT equal to nil, so the guard
// below silently passed and TryNotify dereferenced a nil receiver. The
// parameter that can legitimately be absent keeps the concrete type, so
// "absent" stays expressible as nil.
func (s *WSServer) gitSessionClosed(sid session.ID, wconn *wsConn) {
	s.gitMu.Lock()
	ids := make([]string, 0, len(s.gitBySession[sid]))
	for id := range s.gitBySession[sid] {
		ids = append(ids, id)
	}
	delete(s.gitBySession, sid)
	for _, id := range ids {
		delete(s.gitBindings, id)
	}
	s.gitMu.Unlock()
	if s.git != nil {
		s.git.CloseSession(sid)
	}
	if wconn == nil || len(ids) == 0 {
		return
	}
	go func() {
		for _, id := range ids {
			if err := wconn.TryNotify("git.changed", mustMarshal(gitChangedParams{
				BindingID: id,
				Reason:    "sessionClosed",
			})); err != nil {
				s.log.Debug("write git.changed", "binding_id", id, "error", err)
				return
			}
		}
	}()
}

// ── wire mapping helpers ──────────────────────────────────────────────────
// gitErrorData carries a machine-readable reason in the JSON-RPC error
// data, so the renderer can distinguish the domain refusals that share
// -32602. The vault surface uses the same pattern (vaultErrorData,
// reasonForError) for the same reason: a bare code is indistinguishable,
// and matching on message text is the brittleness a discriminator exists
// to avoid (ADR-0026 §6, the control-saturated precedent; nocx-bpqil).
type gitErrorData struct {
	Reason string `json:"reason"`
}

// gitErrorReason maps a git domain error to its wire reason, so the
// renderer's isUnknownBinding can ask "is this an unknown binding?" rather
// than "is this -32602?" — the code is shared by six distinct refusals.
// Returns nil for a non-git error: an invocation failure carries no
// reason, and the renderer must not treat it as a domain refusal.
func gitErrorReason(err error) *gitErrorData {
	switch err.(type) {
	case *git.ErrUnknownBinding:
		return &gitErrorData{Reason: "unknown-binding"}
	case *git.ErrNotOwned:
		return &gitErrorData{Reason: "not-owned"}
	case *git.ErrHandleReleased:
		return &gitErrorData{Reason: "handle-released"}
	case *git.ErrNothingToCommit:
		return &gitErrorData{Reason: "nothing-to-commit"}
	case *git.ErrAmendUnborn:
		return &gitErrorData{Reason: "amend-unborn"}
	case *git.ErrConflicted:
		return &gitErrorData{Reason: "conflicted"}
	default:
		return nil
	}
}

// gitErrorCode maps git domain errors to JSON-RPC codes: the
// request-shaped refusals — a binding the caller cannot use, an operation
// the repository state refuses — are invalid-params; everything else
// (an invocation that could not be made or completed) is internal. The
// message always carries the domain error's own words, which is what the
// panel surfaces.
func gitErrorCode(err error) int {
	switch err.(type) {
	case *git.ErrUnknownBinding, *git.ErrNotOwned, *git.ErrHandleReleased,
		*git.ErrNothingToCommit, *git.ErrAmendUnborn, *git.ErrConflicted:
		return -32602
	default:
		return -32603
	}
}

// ── spec builder ───────────────────────────────────────────────────────────

// gitSpecs declares the git.* control methods. Every handler needs the
// connection's connState: git.open for the session-ownership check (D15)
// and the inline first status, the binding methods as the caller for the
// per-call Acquire. The operations are built here from the wired stores
// (composition root for this domain), once per builder and shared across
// the methods: GitOpenOperation (session + git gates) and
// GitBindingOperation (git gate). When the git registry is absent every
// handler answers the old not-available error; a registry wired without a
// repo factory leaves only git.open unavailable.
func (s *WSServer) gitSpecs(lane control.Admission, sessionGate, gitGate control.Admission) []methodSpec {
	gitWired := s.git != nil
	var openOp capability.GitOpenOperation
	if gitWired && s.gitFactory != nil {
		openOp = capability.NewGitOpenOperation(sessionGate, gitGate, lane, s.registry, s.gitFactory, s.git)
	}
	var bindingOp capability.GitBindingOperation
	if gitWired {
		bindingOp = capability.NewGitBindingOperation(gitGate, lane, s.git)
	}
	openSub := s.operationQueue("git-open")
	bindingSub := s.operationQueue("git")

	return []methodSpec{
		reg(openSub, "git.open", params(validateGitOpenRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitOpenHandlers{op: openOp, r: r, bindings: s, log: s.log, wired: gitWired}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleOpen(ctx, state, req) }
		}),
		reg(bindingSub, "git.status", params(validateGitBindingRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleStatus(ctx, state, req) }
		}),
		reg(bindingSub, "git.diff", params(validateGitDiffRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleDiff(ctx, state, req) }
		}),
		reg(bindingSub, "git.stage", params(validateGitStageRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleStage(ctx, state, req) }
		}),
		reg(bindingSub, "git.unstage", params(validateGitStageRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleUnstage(ctx, state, req) }
		}),
		reg(bindingSub, "git.stageAll", params(validateGitBindingRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleStageAll(ctx, state, req) }
		}),
		reg(bindingSub, "git.unstageAll", params(validateGitBindingRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleUnstageAll(ctx, state, req) }
		}),
		reg(bindingSub, "git.commit", params(validateGitCommitRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleCommit(ctx, state, req) }
		}),
		reg(bindingSub, "git.headMessage", params(validateGitBindingRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleHeadMessage(ctx, state, req) }
		}),
		reg(bindingSub, "git.log", params(validateGitBindingRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleLog(ctx, state, req) }
		}),
		reg(bindingSub, "git.remote", params(validateGitBindingRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleRemote(ctx, state, req) }
		}),
		reg(bindingSub, "git.close", params(validateGitBindingRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := gitBindingHandlers{op: bindingOp, r: r, bindings: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleClose(ctx, state, req) }
		}),
	}
}
