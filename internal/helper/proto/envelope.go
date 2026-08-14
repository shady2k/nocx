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
}

// Response answers one Request by id, carrying either a result or an error.
type Response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is the machine-readable half of a refusal.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
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
)

// ChunkedResult is the sentinel a Response carries when the real payload
// follows as TypeChunk frames, reassembled by concatenation (D14).
type ChunkedResult struct {
	ChunkedStreamID uint64 `json:"chunkedStreamId"`
	TotalBytes      int    `json:"totalBytes"`
	ChunkCount      int    `json:"chunkCount"`
}
