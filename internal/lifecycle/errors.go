package lifecycle

import "errors"

// Sentinel errors. Every Ingest path that rejects an envelope returns one of
// these and mutates nothing; callers distinguish reasons with errors.Is.
var (
	// Addressing and transport.
	ErrUnknownTransport = errors.New("lifecycle: unknown transport")
	ErrUnknownDomain    = errors.New("lifecycle: unknown domain")
	ErrUnknownLane      = errors.New("lifecycle: unknown lane")
	ErrWrongTransport   = errors.New("lifecycle: envelope arrived on the wrong transport")
	ErrWrongLane        = errors.New("lifecycle: envelope lane does not match its domain")
	ErrBadVersion       = errors.New("lifecycle: unsupported protocol version")
	ErrInvalidArgument  = errors.New("lifecycle: invalid argument")

	// Authentication (decision 2). These failures are counted toward the
	// per-lane handshake rate limit.
	ErrStaleEpoch    = errors.New("lifecycle: envelope carries a stale epoch")
	ErrBadCapability = errors.New("lifecycle: envelope does not possess the domain capability")

	// Sequence (decision 7).
	ErrSequenceReplay = errors.New("lifecycle: duplicate or decreasing sequence number")

	// Domain state.
	ErrDomainPending        = errors.New("lifecycle: domain not past accept")
	ErrDomainInactive       = errors.New("lifecycle: domain is suspended")
	ErrDomainNotLive        = errors.New("lifecycle: domain is closed or lost")
	ErrDomainDesynchronized = errors.New("lifecycle: domain is desynchronized; only a snapshot restores it")
	ErrDomainNotTop         = errors.New("lifecycle: domain is not the top of its lane stack")
	ErrLaneBusy             = errors.New("lifecycle: lane already has a live top-level domain")
	ErrUnknownParent        = errors.New("lifecycle: unknown parent domain")
	ErrParentNotLive        = errors.New("lifecycle: parent domain is not live")
	ErrParentActive         = errors.New("lifecycle: parent domain must be suspended before the child establishes")
	ErrParentNotTop         = errors.New("lifecycle: parent domain is not the top of the lane stack")
	ErrNotSuspended         = errors.New("lifecycle: domain is not suspended")
	ErrNotLost              = errors.New("lifecycle: lane is not lost; only a lost lane may be recovered")

	// Handshake (decision 3).
	ErrHandshakeRateLimited = errors.New("lifecycle: too many failed handshakes on this lane")
	// ErrNoRandomness is a secret that could not be minted: the randomness
	// source failed or came up short. No domain is created (nocx-s16k8).
	//
	// The mint used to tolerate it and return a ZERO capability, on the
	// argument that a caller has no useful answer to "this machine has no
	// randomness". Two things were wrong with that. The authenticator
	// compares equal to itself, so a domain holding zeros authenticated
	// anyone who sent the corresponding run of zero bytes — closed at ingest
	// by refusing a zero capability, and that guard stays. And the caller
	// does have an answer, the same one it has for every other refusal on
	// this path: no domain, no integration, a plain shell, a named reason.
	// A value nobody can distinguish from a valid one is the worst of the
	// three outcomes.
	ErrNoRandomness = errors.New("lifecycle: the randomness source failed; no secret was minted")

	// Events.
	ErrIllegalEvent = errors.New("lifecycle: event kind is not inbound or has no payload")

	// Attempts (decision 5).
	ErrNotPromptReady        = errors.New("lifecycle: no prompt is ready; submit and start require one")
	ErrAttemptOpen           = errors.New("lifecycle: an attempt is already open in this domain")
	ErrAttemptMismatch       = errors.New("lifecycle: start names an attempt that is not the pending one")
	ErrAttemptIDExists       = errors.New("lifecycle: attempt id already in use")
	ErrAttemptNotOpen        = errors.New("lifecycle: attempt is not open")
	ErrAttemptNotStarted     = errors.New("lifecycle: attempt has not been started by the shell")
	ErrAttemptDomainMismatch = errors.New("lifecycle: attempt does not belong to the active domain")
	ErrFenceMissing          = errors.New("lifecycle: completion carries no fence")
	ErrPromptOverAttempt     = errors.New("lifecycle: prompt ready over an open attempt")
	ErrNoActiveDomain        = errors.New("lifecycle: lane has no active domain")
	ErrOversizeCommand       = errors.New("lifecycle: command exceeds the frame command budget")
	ErrBadRequest            = errors.New("lifecycle: domain request is malformed or names an unsupported environment")
	ErrRequestIDShape        = errors.New("lifecycle: domain request id has an unexpected shape")

	// Snapshot (decision 7).
	ErrSnapshotUnexpected = errors.New("lifecycle: snapshot without an outstanding refresh request")
	ErrSnapshotMismatch   = errors.New("lifecycle: snapshot does not answer the outstanding refresh request")
	ErrSnapshotSequence   = errors.New("lifecycle: snapshot next sequence does not advance")
	ErrSnapshotConflict   = errors.New("lifecycle: snapshot state contradicts the kernel's records")
)
