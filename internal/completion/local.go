package completion

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// LocalCompleter resolves path completion locally, against the backend's own
// filesystem. It is the local implementation of Completer; on a remote
// session the SSHCompleter answers instead, so a local path is never offered
// inside an SSH tab.
type LocalCompleter struct{}

func NewLocal() *LocalCompleter { return &LocalCompleter{} }

// Complete implements Completer for the local filesystem.
func (c *LocalCompleter) Complete(ctx context.Context, req Request) (*Response, error) {
	if ctx.Err() != nil {
		return emptyResponse("cancelled"), nil
	}

	text := tokenAt(req.Line, req.Pos)
	if text == "" {
		return emptyResponse(""), nil
	}

	limit := req.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	candidates, completed := completeLocalPathContext(ctx, text, req.Cwd, limit)
	if !completed {
		return emptyResponse("cancelled"), nil
	}
	return &Response{Candidates: candidates}, nil
}

// tokenAt extracts the word at pos in the line. Whitespace and shell control
// characters delimit the word. Returns "" when pos is at a boundary.
func tokenAt(line string, pos int) string {
	if pos < 0 || pos > len(line) {
		return ""
	}
	// Walk back from pos to find word start.
	from := pos
	for from > 0 && !isTokenBoundary(line[from-1]) {
		from--
	}
	// Walk forward from pos to find word end.
	to := pos
	for to < len(line) && !isTokenBoundary(line[to]) {
		to++
	}
	return line[from:to]
}

func isTokenBoundary(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '|', '&', ';', '(', ')', '<', '>', '"', '\'', '`':
		return true
	}
	return false
}

func emptyResponse(reason string) *Response {
	return &Response{Candidates: []Candidate{}, Reason: reason}
}

// ── local path completion (mirrors ws_fs_complete.go) ────────────────────

// completeLocalPathContext walks the directory the text points into and stops
// the moment ctx is done: a renderer that supersedes a keystroke withdraws the
// request (rpc.cancel), and the walk must not outlive it. The bool is false
// exactly when the walk was abandoned, so a cancelled call is never mistaken
// for a directory with nothing in it.
func completeLocalPathContext(ctx context.Context, text, cwd string, limit int) ([]Candidate, bool) {
	if err := ctx.Err(); err != nil {
		return []Candidate{}, false
	}
	if text == "" {
		return []Candidate{}, true
	}

	base, rest, ok := resolvePathBase(text, cwd)
	if !ok {
		return []Candidate{}, true
	}

	dir, prefix := splitDirPrefix(rest)
	if prefix == "." || prefix == ".." {
		dir = filepath.Join(dir, prefix)
		prefix = ""
	}

	entries, err := os.ReadDir(filepath.Join(base, dir))
	if err != nil {
		return []Candidate{}, true
	}
	if err := ctx.Err(); err != nil {
		return []Candidate{}, false
	}

	showHidden := strings.HasPrefix(prefix, ".")
	out := make([]Candidate, 0, minInt(limit, len(entries)))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return []Candidate{}, false
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
			if target, err := os.Stat(filepath.Join(base, dir, name)); err == nil {
				isDir = target.IsDir()
			}
		}
		out = append(out, Candidate{
			Name:   name,
			Path:   filepath.Join(base, dir, name),
			Source: "path",
			IsDir:  isDir,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, true
}

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

func splitDirPrefix(rest string) (dir, prefix string) {
	if rest == "" {
		return "", ""
	}
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		return rest[:i], rest[i+1:]
	}
	return "", rest
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
