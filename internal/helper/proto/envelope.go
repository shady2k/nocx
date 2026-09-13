package proto

import "encoding/json"

// The envelope types are the JSON payloads carried inside frames. Their
// field names are the wire contract: exact and lowerCamel, and the optional
// halves (params, result, error) are omitted rather than null when absent
// (design §4).

// Hello is the first frame the backend sends.
type Hello struct {
	Version string `json:"version"`
	Nonce   string `json:"nonce"`
	Corr    string `json:"corr"`
}

// HelloOK echoes the nonce and identifies the helper build.
type HelloOK struct {
	Version     string `json:"version"`
	Nonce       string `json:"nonce"`
	ContentHash string `json:"contentHash"`
	InstanceID  string `json:"instanceId"`
}

// Request is one named operation on one service.
type Request struct {
	ID      uint64          `json:"id"`
	Service string          `json:"service"`
	Op      string          `json:"op"`
	Params  json.RawMessage `json:"params,omitempty"`
	Corr    string          `json:"corr"`
	// Traceparent is the caller's W3C Trace Context header (nocx-n14oo.2).
	//
	// It is NOT a second Corr. Corr pairs this request's two log lines across
	// the hop and is minted per request; the traceparent names the EXCHANGE
	// the request belongs to — the tool call, the run, the frame off the wire
	// — and joins the helper's lines to everything the backend did before and
	// after asking. A pane that failed to integrate is diagnosed from the
	// second and not from the first.
	//
	// Absent is ordinary: a request from a caller that carries no span is
	// served under a trace of its own.
	Traceparent string `json:"traceparent,omitempty"`
}

// Response answers one Request by id, carrying either a result or an error.
type Response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is the machine-readable half of a refusal. Details carries the
// structured error when a code needs fields the message cannot round-trip
// (the git service's ErrConflicted path); it is omitted when absent, so
// an older peer that does not send it still parses.
type Error struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

// Error codes are the closed set of refusals a helper can answer. The host
// and the backend client both switch on these strings, so the set is a wire
// contract too.
const (
	ErrCodeUnknownService = "unknown_service"
	ErrCodeUnknownOp      = "unknown_op"
	ErrCodeCancelRefused  = "cancel_refused"
	ErrCodeBadParams      = "bad_params"
	ErrCodeInternal       = "internal"

	// The session service's refusals (nocx-k6p18.3). ErrCodeNoSuchSession is
	// the load-bearing one: it is an ANSWER that the session does not exist,
	// which is what the coordinator's reconciliation turns into the `absent`
	// verdict. A refused connection, a timeout or an unreachable host produce
	// no code at all and stay `unknown` — a failure is never a verdict
	// (level-1 design D5).
	ErrCodeNoSuchSession = "no_such_session"
	// ErrCodeWriteRefused covers every way an inbound data frame is not the
	// current holder's at the current epoch. One code, because the caller's
	// action is the same in all of them: ask for the capability.
	ErrCodeWriteRefused = "write_refused"
	// ErrCodeWindowBudget is the helper's aggregate memory budget refusing a
	// spawn. It is distinct from a spawn failure because the caller can act on
	// it — close a session — and cannot act on a failed fork.
	ErrCodeWindowBudget = "window_budget"
	// ErrCodeSpawnFailed is the shell or its PTY not starting.
	ErrCodeSpawnFailed = "spawn_failed"
)

// Refusal is one named refusal as a Go error, so a refusal survives the round
// trip through a caller's ordinary error handling instead of being flattened
// into a string at the point it is written and re-parsed by whoever reads it.
//
// It is the ENCODING half of the wire's error, and it exists because this
// wire is spoken in BOTH directions. A refusal the coordinator answers to a
// reverse request (the helper asked for material and the vault is sealed) has
// to travel back out through the helper — which is not the party that
// understands the vault — and reach the coordinator's caller as the same named
// state. A code carried in an error, on both hops, is what makes that one
// answer rather than three paraphrases.
//
// The DECODED half is the client's own RefusalError, which is a shipped shape
// a caller already switches on; this type is what a handler returns and what
// the host hands a service, and the two are not merged because they sit on
// opposite sides of the encode/decode boundary.
type Refusal struct {
	Code    string
	Message string
	Details json.RawMessage
}

// Error makes a refusal an ordinary error. The message is the one that crosses.
func (r *Refusal) Error() string { return r.Message }

// Unwrap is nil: a refusal is a state, not a wrapper. It carries the code that
// matters, and an errors.Is chain behind it would invite a caller to switch on
// the cause instead of on the answer.
func (r *Refusal) Unwrap() error { return nil }

// WireCode implements Coded: a refusal names its own code and details.
func (r *Refusal) WireCode() (string, json.RawMessage) { return r.Code, r.Details }

// Coded is an error that knows the wire code it is answered with. A responder
// that finds one sends its code; one that does not is `internal`, which is the
// honest default — an unrecognised failure is not a state the caller can act
// on, and inventing a code for it would be worse than admitting it.
type Coded interface {
	WireCode() (code string, details json.RawMessage)
}

// ChunkedResult is the sentinel a Response carries when the real payload
// follows as TypeChunk frames, reassembled by concatenation (D14).
type ChunkedResult struct {
	ChunkedStreamID uint64 `json:"chunkedStreamId"`
	TotalBytes      int    `json:"totalBytes"`
	ChunkCount      int    `json:"chunkCount"`
}

// Chunk is the payload of one TypeChunk frame (D14): the stream id names
// the ChunkedResult sentinel that preceded the chunk, and Bytes are one
// piece of the original result, in order — the receiver concatenates them
// to recover the value. Bytes rides as base64 so the envelope keeps the
// frame's "payload is JSON" rule; the sentinel's stream id is what routes
// the chunk, so concurrent chunked responses cannot interleave (D13).
type Chunk struct {
	ChunkedStreamID uint64 `json:"chunkedStreamId"`
	Bytes           []byte `json:"bytes"`
}
