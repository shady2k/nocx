// Package sandbox implements the opt-in, experimental, filesystem-only
// per-tab sandbox (ADR-0030, design spec 2026-08-02-native-filesystem-sandbox).
//
// The renderer requests a workspace plus bounded add/remove deltas; the
// backend canonicalizes them and owns policy construction and enforcement.
// Platform backends live behind the
// single Service interface; the composition root selects one via build tags
// (sandbox_linux.go / sandbox_darwin.go / sandbox_other.go) and injects it at
// internal/app/app.go — no package globals.
package sandbox

import (
	"context"
	"os"
	"os/exec"
	"sync"
)

// Backend names are frozen wire values (design spec §4.2).
const (
	BackendLandlock    = "landlock"
	BackendSeatbelt    = "seatbelt"
	BackendUnsupported = "unsupported"
)

// Stable status reasons (design spec §4.2).
const (
	ReasonLandlockUnavailable    = "landlock-unavailable"
	ReasonLandlockABITooOld      = "landlock-abi-too-old"
	ReasonSandboxExecUnavailable = "sandbox-exec-unavailable"
	ReasonProbeFailed            = "probe-failed"
	ReasonUnsupportedPlatform    = "unsupported-platform"
)

// helperEnvPrefix marks variables the Linux helper strips before exec so
// helper internals never reach the shell. Defined here (not in the
// linux-tagged helper file) because the shared enforcement probe also asserts
// the strip on every platform.
const helperEnvPrefix = "NOCX_SANDBOX_HELPER_"

// Request carries the backend-owned policy inputs for one sandboxed tab
// (design spec §6). Global is the persisted baseline of additional writable
// directories; Add and Remove are the per-tab deltas. The backend
// canonicalizes every path and never mutates the caller's slices.
type Request struct {
	Workspace string
	Global    []string
	Add       []string
	Remove    []string
}

// Status reports backend availability. It is the payload of sandbox.status
// and the reason surfaced by the Quick Connect "Sandbox unavailable" row.
type Status struct {
	Available bool   `json:"available"`
	Backend   string `json:"backend"`
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
	ABI       int    `json:"abi,omitempty"`
}

// CommandSpec is the ordinary shell command pty.NewLocal builds today:
// shell detection, cmd.Dir, and the scrubbed/UTF-8-forced environment. The
// backend wraps it (helper re-exec on Linux, sandbox-exec on macOS) or, for
// an ordinary request, pty.NewLocal never touches this package at all.
type CommandSpec struct {
	Path       string
	Args       []string
	Dir        string
	Env        []string
	ExtraFiles []*os.File `json:"-"`
}

// PreparedCommand owns the *exec.Cmd of the sandboxed process, the
// post-start readiness handshake, and idempotent cleanup. WaitReady must be
// called after the process is started; Close may be called once — later
// calls are no-ops, so failure and normal-exit paths can both run it.
// Backend and Policy are the realized enforcement metadata surfaced to the
// tab (design spec §3.3).
type PreparedCommand struct {
	Cmd     *exec.Cmd
	Backend string
	Policy  *Policy

	// policyFile is the sole parent-side owner of the Linux helper's policy
	// descriptor. ExtraFiles duplicates it for the child; Close releases this
	// object once, never the raw descriptor number.
	policyFile *os.File
	waitReady  func(context.Context) error
	cleanup    func()
	once       sync.Once
}

// WaitReady blocks until the sandboxed process reports enforcement
// readiness, or ctx is done. Both native backends use a post-enforcement
// acknowledgement; a nil seam exists only for focused tests.
func (p *PreparedCommand) WaitReady(ctx context.Context) error {
	if p == nil || p.waitReady == nil {
		return nil
	}
	return p.waitReady(ctx)
}

// Close releases the sandboxed process and its runtime resources exactly
// once. Safe after WaitReady failure and after child exit; idempotent.
func (p *PreparedCommand) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		if p.policyFile != nil {
			_ = p.policyFile.Close()
		}
		if p.cleanup != nil {
			p.cleanup()
		}
	})
}

// Service is the platform-neutral sandbox boundary. Status answers
// sandbox.status; Prepare turns an ordinary CommandSpec into a sandboxed
// process that fails closed on any policy/launch/handshake error.
type Service interface {
	Status(ctx context.Context) Status
	Prepare(ctx context.Context, req Request, spec CommandSpec) (*PreparedCommand, error)
}

// SessionInfo is the immutable sandbox metadata carried by a sandboxed tab
// and returned in the open result (design spec §3.3, §4.5). Ordinary and SSH
// sessions have none.
type SessionInfo struct {
	Backend       string   `json:"backend"`
	Workspace     string   `json:"workspace"`
	WritableRoots []string `json:"writableRoots"`
}

// Clone returns a deep copy so session metadata cannot be mutated through an
// interface accessor after native enforcement has fixed the policy.
func (s *SessionInfo) Clone() *SessionInfo {
	if s == nil {
		return nil
	}
	return &SessionInfo{
		Backend:       s.Backend,
		Workspace:     s.Workspace,
		WritableRoots: append([]string(nil), s.WritableRoots...),
	}
}

// CanonicalizeWorkspace resolves the open-param workspace to its single
// canonical value (design spec §6): Abs → EvalSymlinks → Stat, requiring an
// existing absolute directory. The transport calls it; errors wrap
// ErrInvalidPermissions (-32602).
func CanonicalizeWorkspace(workspace string) (string, error) {
	return canonicalizeWorkspace(workspace)
}
