package client

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Dial errors — each a distinct sentinel because the product renders them as
// distinct states (design §6): a peer that is not our helper, a peer that
// never answered, a version the helper refused, and a helper whose content
// differs from what the installer wrote are four different things the panel
// must say four different ways.
var (
	// ErrExecForbidden — the server refused the exec (or the session) that
	// would run the helper.
	ErrExecForbidden = errors.New("helper: exec forbidden")
	// ErrSentinelTimeout — nothing arrived on stdout within SentinelTTL.
	ErrSentinelTimeout = errors.New("helper: sentinel timeout")
	// ErrNotOurHelper — something else answered; the error carries what was
	// seen.
	ErrNotOurHelper = errors.New("helper: not our helper")
	// ErrVersionMismatch — the peer exited with the version-mismatch code
	// (42, D5): an incompatible helper. Non-retryable until reinstall.
	ErrVersionMismatch = errors.New("helper: version mismatch")
	// ErrHashMismatch — a hello-ok whose content hash differs from the one
	// installed (D21).
	ErrHashMismatch = errors.New("helper: content hash mismatch")
	// ErrLost — the transport died with requests in flight. Distinct from a
	// refusal so the caller can tell "this may have happened" (D12) from
	// "this was refused".
	ErrLost = errors.New("helper: connection lost")
)

// exitVersionMismatch is the helper's exit code for a refused protocol
// version (D5): the helper writes nothing to stdout and exits 42, and the
// client maps that one pre-sentinel exit to ErrVersionMismatch. Defined here
// rather than imported from the host so the backend never links the remote
// serving half; the value is the same wire constant.
const exitVersionMismatch = 42

// RefusalError is a request the helper refused: the machine-readable code
// and message from the wire (proto.Error). It is a refusal, never a loss —
// errors.Is(err, ErrLost) is false for it. Details carries the structured
// half of the refusal (the git service's ErrConflicted path) so the caller
// can reconstruct the typed domain error, fields intact.
type RefusalError struct {
	Code    string
	Message string
	Details json.RawMessage
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf("helper: %s: %s", e.Code, e.Message)
}
