package ssh

import (
	"context"
)

// PasswordRequest names which connection and account a password is being
// asked for. Every password prompt must name the password it is asking
// about (nocx-s8jn) — a bare "enter password" box is how the wrong
// password ends up in the wrong connection.
type PasswordRequest struct {
	// Connection is the saved profile's display name; empty for
	// direct-host opens, which do not raise prompts.
	Connection string
	User       string
	Host       string
	// Reason says why the password is being asked: no password is stored
	// for the connection, or the stored one was rejected. Display text,
	// never a code.
	Reason string
}

// PasswordAnswer is the renderer's answer to a PasswordRequest.
type PasswordAnswer struct {
	// Password is the value to use for THIS connection's auth attempt.
	Password string
	// Remember asks the caller to store the answer as a vault secret the
	// profile references (ADR-0017), so the next open is silent. The
	// caller — the connection layer — decides how and where it stores;
	// this package only ever uses Password for the auth attempt.
	Remember bool
}

// ConnectionPasswordRequester asks the user for a connection password. The
// same shape as the vault unlock requester (backend asks a renderer and
// blocks for the answer, AD-8: behind an interface, wired at the one
// composition root), so the ssh package never imports transport. The
// connection resolver implements it: it adapts the transport's wire ask
// and does the remember (vault secret + profile reference).
type ConnectionPasswordRequester interface {
	RequestConnectionPassword(ctx context.Context, req PasswordRequest) (PasswordAnswer, error)
}
