package transport

// The assistant's route to the pane's own files (stage 10, ADR-anntnxdj §5).
//
// The file tools used to build their capabilities over local.New(): files.read,
// files.edit and files.create always reached THIS machine's disk, and an SSH
// pane was kept out by a stub sentence rather than by a missing route. This is
// the missing route. Every tool call asks for one short-lived Handle through
// the same factory the Files panel uses, registers a binding of its own in the
// same registry, does its work and closes it — so the assistant acts on the
// pane's host, the panel still owns its own binding, and closing one never
// closes the other.
//
// # Why a fresh binding per call rather than the panel's
//
// A pane's Files panel may never have been opened, and its binding is the
// panel's: its root, its watch set and its lifetime belong to a surface a
// person is looking at. The assistant's binding has the life of one tool call
// and no watches at all, which is the only shape where "the registry holds zero
// bindings afterwards" is a property rather than a hope — including after a
// cancelled call and after the pane closes mid-call.
//
// # What cannot be reached through it
//
// The session is the one the server fixed at ask time (AD-7); nothing a tool's
// arguments say can name another. Registry.Acquire re-checks that this caller
// owns that session on every acquisition, so a binding minted here can only be
// used for the run it was minted for.

import (
	"context"
	"errors"
	"sync"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/session"
)

// assistantFiles is the filesystem.HandleSource a run's file-tool capabilities
// hold. It carries the pane's identity and the two things needed to reach it —
// the session registry and the provider factory — and no provider of its own.
type assistantFiles struct {
	reg       *filesystem.Registry
	factory   FilesystemProviderFactory
	sessions  session.Registry
	sessionID session.ID
}

var _ filesystem.HandleSource = assistantFiles{}

// ErrNoAssistantFiles says the build wired no files composition for the agent,
// so a file tool has nothing to act through. It is a refusal rather than a
// fallback to this machine's disk, because a fallback is precisely the leak the
// stage exists to close.
var ErrNoAssistantFiles = errors.New("transport: no file binding is available to the assistant")

// Acquire builds the pane's provider, registers it as a binding of its own, and
// hands back a guarded Handle over it. The release closes the binding, so the
// provider is torn down with the call.
//
// The factory runs once per call, and that is a decision (reversible): the
// panel's binding is not reused because it may not exist, because its root and
// watches are the panel's, and because a cached binding would need an owner to
// close it on a path nobody owns.
func (a assistantFiles) Acquire(ctx context.Context) (filesystem.Handle, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if a.reg == nil || a.factory == nil || a.sessions == nil || a.sessionID == "" {
		return nil, nil, ErrNoAssistantFiles
	}
	sess, err := a.sessions.Get(a.sessionID)
	if err != nil {
		return nil, nil, err
	}
	provider, err := a.factory(sess, "")
	if err != nil {
		return nil, nil, err
	}
	caps, endpointID := capability.ProviderCapabilities(provider)
	id, err := a.reg.Register(provider, sess.ID(), endpointID, caps)
	if err != nil {
		return nil, nil, err
	}
	handle, release, err := a.reg.Acquire(id, runCaller{sessionID: a.sessionID})
	if err != nil {
		_ = a.reg.Close(id)
		return nil, nil, err
	}
	// The call may have been cancelled while the provider was being built —
	// dialing a host is the expensive half — and a cancelled call must leave no
	// binding behind. The same check the caller's capability makes before it
	// acts, made at the one point where an abandoned binding could survive.
	if err := ctx.Err(); err != nil {
		release()
		_ = a.reg.Close(id)
		return nil, nil, err
	}
	var once sync.Once
	return handle, func() {
		once.Do(func() {
			release()
			_ = a.reg.Close(id)
		})
	}, nil
}

// runCaller owns exactly one session: the run's. It is filesystem.Caller's whole
// contract, and keeping it this small is the point — the authority is the
// session the server fixed for the run, never something a call can name.
type runCaller struct {
	sessionID session.ID
}

func (c runCaller) Owns(id session.ID) bool { return id == c.sessionID }
