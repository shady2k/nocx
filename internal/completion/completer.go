// Package completion provides the completion source interface and
// implementations: local filesystem (the existing fs.complete logic) and
// SSH remote (a second shell that answers what only a shell knows — paths
// from the remote filesystem, command-specific completions from bash
// completion functions).
//
// Design: nocx-w7h.15 — the completion adapter.
package completion

import "context"

// Request is one completion query. The caller owns the cancellation signal
// (latency budget and keystroke abort); the completer must be a no-op when
// the context is done.
type Request struct {
	// Host is the session's remote hostname. Empty for a local session.
	// The SSH completer leases a probe on it.
	Host string

	// Cwd is the session's current working directory, from OSC 7. When
	// empty, relative path completion cannot answer.
	Cwd string

	// Line is the whole line the user is typing.
	Line string

	// Pos is the caret offset into Line (0 = start of line). The token being
	// completed is the word at this position.
	Pos int

	// Limit caps the number of candidates returned. The completer may return
	// fewer; the caller clamps to a reasonable range.
	Limit int
}

// Candidate is one row of the completion result.
type Candidate struct {
	// Name is the display name — the last path segment for a file, the
	// command name for a command, the completion word for a function
	// answer.
	Name string `json:"name"`

	// Path is the absolute path of a path candidate. Empty for
	// non-path candidates (commands, function completions).
	Path string `json:"path,omitempty"`

	// Source names where the answer came from. The UI distinguishes an
	// adapter answer from a local guess; the source is what lets it.
	// "path" means compgen -f / compgen -d; "function" means a completion
	// function answered; "command" means compgen -c.
	Source string `json:"source"`

	// IsDir is true when the candidate is a directory. The renderer appends
	// a trailing slash and treats it as a step rather than a terminal
	// acceptance.
	IsDir bool `json:"isDir,omitempty"`
}

// Response is the result of one completion query. Candidates is never nil:
// no matches is []. Truncated is true when the output cap was hit and the
// result is not complete. Reason is a stated explanation when candidates is
// empty — silence is indistinguishable from a broken feature.
type Response struct {
	Candidates []Candidate `json:"candidates"`
	Truncated  bool        `json:"truncated"`
	Reason     string      `json:"reason,omitempty"`
}

// Completer answers one completion query. Implementations: local filesystem
// (LocalCompleter) and SSH remote (SSHCompleter, which asks this machine's
// helper for one named completion probe — see ProbeConn).
type Completer interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}
