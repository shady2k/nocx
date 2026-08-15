package hostsvc

import "github.com/shady2k/nocx/internal/git"

// The wire shapes of the git service's operations. Params are the
// service-side declarations the host's D3 audit reads; results carry the
// domain types of internal/git verbatim, JSON-encoded (plan Task 7).

// OpenParams is git.open's params: the directory the backend verified.
type OpenParams struct {
	Cwd string `json:"cwd"`
}

// OpenResult is git.open's answer: the service-issued binding id and the
// resolution outcome. The outcome embeds, so its fields cross with the
// domain type's own JSON shape, verbatim; every later op addresses the held
// repository by BindingID.
type OpenResult struct {
	BindingID string `json:"bindingId"`
	git.OpenOutcome
}

// BindingParams addresses a previously opened repository (git.status,
// git.envState).
type BindingParams struct {
	BindingID string `json:"bindingId"`
}

// EnvStateResult is git.envState's answer: the environment git will run in
// and, when degraded, why. The state is the domain EnvState verbatim; the
// pair is what internal/git/local already returns.
type EnvStateResult struct {
	State  git.EnvState `json:"state"`
	Reason string       `json:"reason,omitempty"`
}

// DiffParams addresses one file's diff on a held repository: the path and
// side the panel clicked, and the byte bound the HELPER applies (D9). The
// bound travels to where the work happens; the backend never bounds after
// the bytes have arrived.
type DiffParams struct {
	BindingID string   `json:"bindingId"`
	Path      string   `json:"path"`
	Side      git.Side `json:"side"`
	MaxBytes  int64    `json:"maxBytes"`
}

// LogParams addresses one repository's recent history (git.log): the
// caller-named max bound, applied where the work happens (D9).
type LogParams struct {
	BindingID string `json:"bindingId"`
	Max       int    `json:"max"`
}

// StageParams is git.stage and git.unstage's params: the pathspecs to
// move between the index and the worktree. Paths is the one legitimate
// []string on the wire (D8) — the nocx:"pathspec" tag is what the host's
// D3 audit accepts, and no path ever reaches a command line: local writes
// them to git's stdin as NUL-separated literal pathspecs, so a path with
// a space, a quote, a leading dash or a newline stages exactly itself.
type StageParams struct {
	BindingID string   `json:"bindingId"`
	Paths     []string `json:"paths" nocx:"pathspec"`
}

// CommitParams is git.commit's params: the message and whether it amends
// HEAD. The message crosses as a JSON string and reaches git through
// commit -F - over stdin — never argv (D8) — which is what makes a
// multi-line message with quotes and non-ASCII the normal case, not an
// escape.
type CommitParams struct {
	BindingID string `json:"bindingId"`
	Message   string `json:"message"`
	Amend     bool   `json:"amend"`
}
