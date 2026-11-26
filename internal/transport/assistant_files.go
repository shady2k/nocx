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
// hold. It carries the pane's identity and the two OPERATIONS that reach it —
// files.open's (resolve the session, register a provider, take a use-guard) and
// the filesystem-binding's (close the binding this call minted) — and no
// provider, no registry and no factory of its own.
//
// The operations rather than the raw registry are what keep this file inside
// ADR-8eej3e66 §4: a provider call is only legitimate inside an operation's
// scope, and the static check in internal/operation/scopes reads a direct
// `reg.Register(...)` here as exactly what it is — a provider touched with no
// scope open. Reaching the same registration through the service the operation
// hands its callback is not a formality; it is the guard that makes the call
// belong to a scoped request rather than to whatever goroutine holds the
// registry.
type assistantFiles struct {
	open      capability.FilesystemOpenOperation
	bind      capability.FilesystemBindingOperation
	sessionID session.ID
}

var _ filesystem.HandleSource = assistantFiles{}

// filesFor is the ONE place a run's route to the pane's own files is assembled
// from the server's wiring: the files operations the Files panel uses, plus the
// session the server fixed at ask time (AD-7).
//
// It is a function rather than a literal at each site because the run's
// AskParams is built in more than one place — the ask path, and the transport
// tests that drive the handler directly to observe delivery order — and a
// hand-assembled second copy is how files.read lost its route: the harness had
// a sender, the copy had the zero value, and every tool call answered
// ErrNoAssistantFiles (nocx-a62cl.11.1). A caller that wants the tools to work
// asks this; a caller that wants the honest refusal names no composition.
func (h agentHandlers) filesFor(sessionID session.ID) assistantFiles {
	return assistantFiles{
		open:      h.filesOpen,
		bind:      h.filesBind,
		sessionID: sessionID,
	}
}

// ErrNoAssistantFiles says the build wired no files composition for the agent,
// so a file tool has nothing to act through. It is a refusal rather than a
// fallback to this machine's disk, because a fallback is precisely the leak the
// stage exists to close.
var ErrNoAssistantFiles = errors.New("transport: no file binding is available to the assistant")

// filesOperationsForAgent is the assistant's half of the files route: the two
// operations the Files panel runs on, or neither of them.
//
// A build can wire the registry and leave the provider factory unset — the
// honest shape of "no filesystem anywhere", and the state a transport test
// names when it wants the unwired answer. Handing the operations over anyway
// would give the run a route whose every acquisition dereferences a nil
// factory: a panic where the composition promises ErrNoAssistantFiles
// (ws_agent.go's field comment says so, and this is the line that keeps it
// true). The panel keeps its own answer for the same build; only the
// assistant's route is gated here.
func filesOperationsForAgent(route filesRoute) (capability.FilesystemOpenOperation, capability.FilesystemBindingOperation) {
	if route.factory == nil {
		return nil, nil
	}
	return route.open, route.binding
}

// Acquire builds the pane's provider, registers it as a binding of its own, and
// hands back a guarded Handle over it. The release closes the binding, so the
// provider is torn down with the call.
//
// The factory runs once per call, and that is a decision (reversible): the
// panel's binding is not reused because it may not exist, because its root and
// watches are the panel's, and because a cached binding would need an owner to
// close it on a path nobody owns.
//
// The whole creation runs INSIDE files.open's operation: the session lookup,
// the registration and the use-guard are that operation's service methods, so
// the provider is only ever touched while a scope is open (ADR-8eej3e66 §4).
// The handle outlives it, which is what the operation is for — the service is
// the reach, not the lifetime — and the binding is closed through the
// filesystem-binding operation for the same reason.
func (a assistantFiles) Acquire(ctx context.Context) (filesystem.Handle, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if a.open == nil || a.bind == nil || a.sessionID == "" {
		return nil, nil, ErrNoAssistantFiles
	}
	var (
		handle    filesystem.Handle
		release   func()
		bindingID string
	)
	err := a.open.Open(ctx, func(ctx context.Context, svc capability.FilesystemOpenService) error {
		sess, gerr := svc.Get(a.sessionID)
		if gerr != nil {
			return gerr
		}
		id, _, oerr := svc.OpenBinding(ctx, sess, "")
		if oerr != nil {
			return oerr
		}
		bindingID = id
		h, rel, aerr := svc.Acquire(id, runCaller{sessionID: a.sessionID})
		if aerr != nil {
			return aerr
		}
		handle, release = h, rel
		return nil
	})
	if err != nil {
		a.abandon(ctx, bindingID)
		return nil, nil, err
	}
	// The call may have been cancelled while the provider was being built —
	// dialing a host is the expensive half — and a cancelled call must leave no
	// binding behind. The same check the caller's capability makes before it
	// acts, made at the one point where an abandoned binding could survive.
	if err := ctx.Err(); err != nil {
		release()
		a.abandon(ctx, bindingID)
		return nil, nil, err
	}
	var once sync.Once
	return handle, func() {
		once.Do(func() {
			release()
			a.abandon(ctx, bindingID)
		})
	}, nil
}

// abandon closes a binding this call minted and will not use. It runs after the
// call's own operation has ended, so it opens the filesystem-binding operation
// for itself. The call's own context is what it passes, cancelled or not: that
// operation holds no admission and close reads no deadline, so the close does
// not depend on the context being live — and a detached context would both drop
// the invocation's own values and be refused by the operation-scope ratchet.
func (a assistantFiles) abandon(ctx context.Context, bindingID string) {
	if bindingID == "" {
		return
	}
	_ = a.bind.Run(ctx, func(_ context.Context, svc capability.FilesystemBindingService) error {
		return svc.Close(bindingID)
	})
}

// runCaller owns exactly one session: the run's. It is filesystem.Caller's whole
// contract, and keeping it this small is the point — the authority is the
// session the server fixed for the run, never something a call can name.
type runCaller struct {
	sessionID session.ID
}

func (c runCaller) Owns(id session.ID) bool { return id == c.sessionID }
