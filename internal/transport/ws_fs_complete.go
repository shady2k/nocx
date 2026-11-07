package transport

// fs.complete — local filesystem path completion (design §8.5, nocx-w7h.3).
// The result shape is declared once in contracts/fs.complete.schema.json and
// belongs to neither side; this file serves it from the backend's own
// filesystem — which is exactly why the frontend's local-path provider is
// inactive on a remote session: the backend's filesystem is the local
// machine's, and a local candidate offered inside an SSH session is wrong
// even when it says "local".

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/operation/domains"
)

// fsCompleteParams is the request the local-path provider sends. The earlier
// decision to leave params unpinned was wrong: fs.complete.params.schema.json is
// now the wire contract, and its registered validator remains runtime enforcement.
//
//	text  — required; the partial path being completed (the current word)
//	cwd   — optional; the session's working directory, used only when text
//	        is relative. Missing/empty means relative text completes nothing.
//	limit — optional; <1 → 50, >200 → 200
type fsCompleteParams struct {
	Text  string `json:"text"`
	Cwd   string `json:"cwd"`
	Limit *int   `json:"limit"`
}

// fsCompleteEntry is one row of the fs.complete result, matching the schema
// exactly. Path is always absolute once resolved.
type fsCompleteEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

// fsCompleteResponse is the result of fs.complete. Entries is never nil: no
// matches is [] (the schema says so, and a null would throw the renderer's
// first .map — the nocx-25k9.14 defect class).
type fsCompleteResponse struct {
	Entries []fsCompleteEntry `json:"entries"`
}

// defaultFsCompleteLimit is the page size when the caller sends none.
const defaultFsCompleteLimit = 50

// maxFsCompleteLimit caps a page so a runaway completion cannot read a whole
// directory tree into the renderer.
const maxFsCompleteLimit = 200

// fsCompleteDirReader is the directory read one completion performs. It is a
// seam rather than a direct os.ReadDir so the whole completion — the read and
// the symlink classifications it makes — can run under the target path's
// failure domain (seamSpecs) and be held open by a test.
type fsCompleteDirReader func(path string) ([]os.DirEntry, error)

// fsCompleteStat classifies one path, following a symlink as os.Stat does.
type fsCompleteStat func(path string) (os.FileInfo, error)

// fsCompleteCompleter runs one completion under its target's failure domain.
type fsCompleteCompleter func(ctx context.Context, text, cwd string, limit int) ([]fsCompleteEntry, bool, error)

// fsCompleteHandlers answers fs.complete. It completes against the backend's
// own filesystem and touches no transport state beyond the bounded completion
// and its Responder.
type fsCompleteHandlers struct {
	complete fsCompleteCompleter
	r        Responder
}

// handleFsComplete serves the fs.complete method.
//
// Completion fails soft, never loud: a typo'd or unreadable directory answers
// an empty page, because the dropdown is a convenience and a JSON-RPC error
// for a path that merely does not exist yet would surface as a toast for
// every half-typed path. The renderer's own applicability rule (local session
// only) is the hard gate; this handler answers the backend's filesystem
// regardless, because the backend cannot see the session's host.
func (h fsCompleteHandlers) handleFsComplete(ctx context.Context, req jsonrpcRequest) {
	text, cwd, limit, errMsg := parseFsCompleteParams(req)
	if errMsg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + errMsg})
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}

	resp := fsCompleteResponse{Entries: []fsCompleteEntry{}}
	if text != "" {
		entries, ok, err := h.complete(ctx, text, cwd, limit)
		if !ok {
			return
		}
		if err != nil {
			// A failure-domain refusal or a declared bound is not a missing
			// directory: it is answered, not turned into an empty page.
			if answerFsCompleteExpiry(ctx, h.r, req, err) {
				return
			}
		}
		resp.Entries = entries
	}
	_ = h.r.TryResult(req.ID, mustMarshal(resp))
}

// newFsCompleteHandlers builds fs.complete's handler for this server. The
// completion reads the backend's own filesystem with no binding to name a
// failure domain, so the whole completion — the read and the symlink
// classifications — rides the same bulkheads files.* uses
// (nocx-3an1s.2.17). The target directory names the domain; the composition
// root supplies that answer from the local provider's mount table, and naming
// it touches no disk.
func (s *WSServer) newFsCompleteHandlers(r Responder) fsCompleteHandlers {
	readDir := s.fsCompleteReadDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	return fsCompleteHandlers{
		complete: func(ctx context.Context, text, cwd string, limit int) ([]fsCompleteEntry, bool, error) {
			target, ok := resolveFsCompleteTarget(text, cwd)
			if !ok {
				return []fsCompleteEntry{}, true, nil
			}
			var entries []fsCompleteEntry
			err := s.filesDomainDo(ctx, s.fsCompleteDomainNamer, target.Dir, func(callCtx context.Context) error {
				var rerr error
				entries, rerr = completeInDir(callCtx, target, limit, readDir, os.Stat)
				return rerr
			})
			if err != nil {
				if ctx.Err() != nil {
					// The renderer withdrew the keystroke: nothing ran that
					// needs an answer.
					return []fsCompleteEntry{}, false, nil
				}
				return []fsCompleteEntry{}, true, err
			}
			if err := ctx.Err(); err != nil {
				return []fsCompleteEntry{}, false, nil
			}
			return entries, true, nil
		},
		r: r,
	}
}

// parseFsCompleteParams validates the request against the handler contract
// above. The returned message is empty when the params are usable.
func parseFsCompleteParams(req jsonrpcRequest) (string, string, int, string) {
	var p fsCompleteParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return "", "", 0, "params must be an object"
	}
	if p.Text == "" {
		return "", "", 0, "text is required"
	}
	limit := defaultFsCompleteLimit
	if p.Limit != nil {
		limit = *p.Limit
		if limit < 1 {
			limit = defaultFsCompleteLimit
		} else if limit > maxFsCompleteLimit {
			limit = maxFsCompleteLimit
		}
	}
	return p.Text, p.Cwd, limit, ""
}

// fsCompleteTarget is what one completion asks the filesystem for: the
// absolute directory to list and the name prefix to match in it.
type fsCompleteTarget struct {
	Dir    string
	Prefix string
}

// resolveFsCompleteTarget applies the shell's resolution rules to the partial
// path, mirroring what a shell's path completion does:
//
//   - text starting with `~` (alone or as `~/…`) resolves against the current
//     user's home directory; `~user` cannot be resolved cheaply and answers
//     nothing;
//   - text starting with `/` is absolute;
//   - anything else is relative to cwd; with no cwd (the session never
//     reported one) relative text answers nothing;
//   - the text up to the last `/` is the directory to list; the last segment
//     is the prefix. A trailing `/` means "list this directory": empty prefix;
//   - `.` and `..` as the final segment list that directory itself.
//
// It is pure string work — no disk is touched — so the failure domain can be
// named before the bounded read begins (ADR-8eej3e66 §6: naming a mount must
// not stat it, because a stat on a hung mount never returns).
func resolveFsCompleteTarget(text, cwd string) (fsCompleteTarget, bool) {
	if text == "" {
		// Empty text is no completion: the handler refuses it, and this
		// resolver refuses it too, so a future caller cannot bypass the guard
		// and get a whole-directory listing by accident.
		return fsCompleteTarget{}, false
	}
	base, rest, ok := resolvePathBase(text, cwd)
	if !ok {
		return fsCompleteTarget{}, false
	}
	dir, prefix := splitDirPrefix(rest)
	// The final segment being "." or ".." means "list that directory", not
	// "match names starting with ." — the shell completes `cd ..` by showing
	// the parent's entries.
	if prefix == "." || prefix == ".." {
		dir = filepath.Join(dir, prefix)
		prefix = ""
	}
	return fsCompleteTarget{Dir: filepath.Join(base, dir), Prefix: prefix}, true
}

// completeInDir lists one target directory and returns the entries whose name
// starts with the prefix, capped at limit. Hidden entries (leading `.`) are
// listed only when the prefix itself starts with `.`, so a bare prefix never
// surfaces dotfiles (shell behaviour).
//
// Any listing failure (missing directory, permission, race) is returned to
// the caller, which answers an empty page for an ordinary one — see
// handleFsComplete for why. The walk stops the moment ctx is done: a renderer
// that supersedes a keystroke withdraws the request (rpc.cancel), and the
// listing must not outlive it.
func completeInDir(
	ctx context.Context,
	target fsCompleteTarget,
	limit int,
	readDir fsCompleteDirReader,
	stat fsCompleteStat,
) ([]fsCompleteEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := readDir(target.Dir)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	showHidden := strings.HasPrefix(target.Prefix, ".")
	out := make([]fsCompleteEntry, 0, min(limit, len(entries)))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := e.Name()
		if target.Prefix != "" && !strings.HasPrefix(name, target.Prefix) {
			continue
		}
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		isDir := e.IsDir()
		path := filepath.Join(target.Dir, name)
		if e.Type()&os.ModeSymlink != 0 {
			// A symlink completes as its TARGET's kind: `cd link-to-dir/` is
			// the common motion, and the dirs-only rule for cd/pushd/rmdir
			// would otherwise hide half of a typical home directory. A
			// broken link keeps the dirent type — never a directory it is
			// not.
			if info, serr := stat(path); serr == nil {
				isDir = info.IsDir()
			}
		}
		out = append(out, fsCompleteEntry{
			Name:  name,
			Path:  path,
			IsDir: isDir,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// resolvePathBase returns the absolute base directory text points into, the
// remainder of the text after the base, and whether the text is resolvable at
// all. rest is "" when text names the base itself.
func resolvePathBase(text, cwd string) (base, rest string, ok bool) {
	if strings.HasPrefix(text, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", false
		}
		if text == "~" {
			return home, "", true
		}
		if !strings.HasPrefix(text, "~/") {
			// `~user/…` — resolving another user needs a passwd lookup that
			// completion is not worth; answer nothing.
			return "", "", false
		}
		return home, text[2:], true
	}
	if strings.HasPrefix(text, "/") {
		return "/", strings.TrimPrefix(text, "/"), true
	}
	if cwd == "" {
		return "", "", false
	}
	return cwd, text, true
}

// splitDirPrefix splits the text (relative to its base) into the directory
// part and the last-segment prefix. `a/b/c` → ("a/b", "c"); `a/b/` → ("a/b",
// ""); `c` → ("", "c").
func splitDirPrefix(rest string) (dir, prefix string) {
	if rest == "" {
		return "", ""
	}
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		return rest[:i], rest[i+1:]
	}
	return "", rest
}

// min is Go 1.21's builtin; keep a local copy so this file reads without
// relying on the toolchain's exact version.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// answerFsCompleteExpiry answers a completion its failure domain refused or
// whose declared bound ended, and reports whether it answered. It reuses the
// catalogue's one expiry shape, so a person's surface reads the same
// machine-readable reason here as for every other operation.
//
// A refusal before the work started — a suspect domain, a turn that ran out
// — is not-started: nothing was read, and the same keystroke may be asked
// again. A work bound that ended after a read began is retry-read: the
// listing may have been partially read, and asking again is safe because the
// read has no effect.
func answerFsCompleteExpiry(ctx context.Context, r Responder, req jsonrpcRequest, err error) bool {
	var refusal *domains.Refusal
	expired := errors.Is(err, context.DeadlineExceeded)
	if !expired && !errors.As(err, &refusal) {
		return false
	}
	reason := catalog.OpFsComplete.Declaration().Expired
	outcome := catalog.OutcomeExpired
	message := "this call reached its declared deadline"
	if refusal != nil {
		message = refusal.Sentence()
		if errors.Is(refusal, domains.ErrTurnExpired) || errors.Is(refusal, domains.ErrDomainSuspect) {
			reason = catalog.ExpiredNotStarted
			outcome = catalog.OutcomeNotStarted
		}
	}
	if scope, ok := catalog.ScopeFrom(ctx); ok {
		scope.Settle(outcome)
	}
	answerOperationExpiry(r, req, reason, message)
	return true
}
