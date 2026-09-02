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
	"os"
	"path/filepath"
	"strings"
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

// fsCompleteHandlers answers fs.complete. The handler is pure — it completes
// against the backend's own filesystem and touches no transport state, so it
// holds only its Responder.
type fsCompleteHandlers struct {
	r Responder
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
		var ok bool
		resp.Entries, ok = completeLocalPathContext(ctx, text, cwd, limit)
		if !ok {
			return
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(resp))
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

// completeLocalPath resolves text against cwd and returns the directory
// entries whose name starts with the last path segment, capped at limit.
//
// Resolution rules, mirroring what a shell's path completion does:
//
//   - empty text is no completion — the handler refuses it, and the helper
//     refuses it too, so a future caller cannot bypass the guard and get a
//     full directory listing by accident;
//   - text starting with `~` (alone or as `~/…`) resolves against the
//     current user's home directory; `~user` cannot be resolved cheaply and
//     answers nothing;
//   - text starting with `/` is absolute;
//   - anything else is relative to cwd; with no cwd (the session never
//     reported one) relative text answers nothing;
//   - the text up to the last `/` is the directory to list; the last segment
//     is the prefix. A trailing `/` means "list this directory": empty prefix;
//   - `.` and `..` as the final segment list that directory itself;
//   - hidden entries (leading `.`) are listed only when the prefix itself
//     starts with `.`, so a bare prefix never surfaces dotfiles (shell
//     behaviour).
//
// Any listing failure (missing directory, permission, race) answers an empty
// page — see handleFsComplete for why.
// The walk stops the moment ctx is done: a renderer that supersedes a
// keystroke withdraws the request (rpc.cancel), and the listing must not
// outlive it. The bool is false exactly when the walk was abandoned, so a
// cancelled call is never mistaken for a directory with nothing in it.
func completeLocalPathContext(ctx context.Context, text, cwd string, limit int) ([]fsCompleteEntry, bool) {
	if err := ctx.Err(); err != nil {
		return []fsCompleteEntry{}, false
	}
	if text == "" {
		return []fsCompleteEntry{}, true
	}
	// Resolve the base directory the text points into.
	base, rest, ok := resolvePathBase(text, cwd)
	if !ok {
		return []fsCompleteEntry{}, true
	}

	dir, prefix := splitDirPrefix(rest)

	// The final segment being "." or ".." means "list that directory", not
	// "match names starting with ." — the shell completes `cd ..` by showing
	// the parent's entries.
	if prefix == "." || prefix == ".." {
		dir = filepath.Join(dir, prefix)
		prefix = ""
	}

	entries, err := os.ReadDir(filepath.Join(base, dir))
	if err != nil {
		return []fsCompleteEntry{}, true
	}
	if err := ctx.Err(); err != nil {
		return []fsCompleteEntry{}, false
	}

	showHidden := strings.HasPrefix(prefix, ".")
	out := make([]fsCompleteEntry, 0, min(limit, len(entries)))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return []fsCompleteEntry{}, false
		}
		name := e.Name()
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			// A symlink completes as its TARGET's kind: `cd link-to-dir/` is
			// the common motion, and the dirs-only rule for cd/pushd/rmdir
			// would otherwise hide half of a typical home directory. A
			// broken link keeps the dirent type — never a directory it is
			// not.
			if target, err := os.Stat(filepath.Join(base, dir, name)); err == nil {
				isDir = target.IsDir()
			}
		}
		out = append(out, fsCompleteEntry{
			Name:  name,
			Path:  filepath.Join(base, dir, name),
			IsDir: isDir,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, true
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
