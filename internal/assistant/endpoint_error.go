package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	openai "github.com/meguminnnnnnnnn/go-openai"
)

// UnexplainedFailureSentence is what a person reads when nothing below could
// name the cause. It says "the log" because the log is where the framework's
// own text was kept, once, at the boundary that caught it (nocx-avogl.3).
//
// It lives here rather than at either call site because there are two of them
// — the ask path's terminal arm and the probe's — and one sentence written
// twice is the shape this whole file exists to remove.
const UnexplainedFailureSentence = "the model failed to answer. The details are in nocx's log."

// UnexecutedToolCallSentence is what a person reads when a model asked for a
// tool using text that nocx could not execute. It names the recoverable action
// without exposing the provider's envelope.
const UnexecutedToolCallSentence = "the model asked for a tool in a form nocx could not act on. Ask again — a different model may handle tools better."

// EgressScreeningFailureSentence is what a person reads when the egress
// gate could not inspect a tool result. It names the withheld result and the
// configuration to check without exposing detector or framework text.
const EgressScreeningFailureSentence = "the result could not be screened, so it was withheld. Check the vault or egress screening configuration, then ask again."

// UnexecutedToolCallError marks a successful stream whose answer was only a
// textual tool-call envelope. It is a typed outcome so the transport can
// terminalize the run without matching provider error text.
type UnexecutedToolCallError struct{}

func (*UnexecutedToolCallError) Error() string {
	return "assistant: unexecuted tool-call envelope"
}

// IsUnexecutedToolCallEnvelope recognizes one or more complete textual
// tool-call envelopes, not a vendor name. The serving stack has been observed
// to emit both the XML-shaped function/parameter dialect and a JSON object
// inside the same <tool_call> wrapper. Multiple blocks are accepted only when
// separated by whitespace; requiring the entire trimmed prose to be that
// sequence keeps an answer that quotes <tool_call> ordinary prose.
//
// finish_reason is deliberately not inspected: some endpoints say "stop",
// some omit it, and some provide it only on the final delta. A list of
// provider strings is also rejected because it would make this judgement stale
// as soon as another compatible endpoint uses the same shape.
func IsUnexecutedToolCallEnvelope(text string) bool {
	const (
		open  = "<tool_call>"
		close = "</tool_call>"
	)
	remaining := strings.TrimSpace(text)
	found := false
	for remaining != "" {
		if !strings.HasPrefix(remaining, open) {
			return false
		}
		closeOffset := strings.Index(remaining[len(open):], close)
		if closeOffset < 0 {
			return false
		}
		closeOffset += len(open)
		blockEnd := closeOffset + len(close)
		block := remaining[:blockEnd]
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(block, open), close))
		// Nested wrappers are not a dialect. Reject them before either
		// inner parser can mistake their tags for parameter or JSON data.
		if strings.Contains(inner, open) || strings.Contains(inner, close) {
			return false
		}
		if !isXMLToolCallEnvelope(inner) && !isJSONToolCallEnvelope(inner) {
			return false
		}
		found = true
		remaining = strings.TrimSpace(remaining[blockEnd:])
	}
	return found
}

func isJSONToolCallEnvelope(inner string) bool {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(inner), &call); err != nil || strings.TrimSpace(call.Name) == "" {
		return false
	}
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return false
	}
	return arguments != nil
}

func isXMLToolCallEnvelope(inner string) bool {
	const (
		functionPrefix  = "<function="
		functionClose   = "</function>"
		parameterPrefix = "<parameter="
		parameterClose  = "</parameter>"
	)
	if !strings.HasPrefix(inner, functionPrefix) {
		return false
	}
	functionEnd := strings.IndexByte(inner, '>')
	if functionEnd <= len(functionPrefix) {
		return false
	}
	if strings.TrimSpace(inner[len(functionPrefix):functionEnd]) == "" {
		return false
	}
	rest := strings.TrimSpace(inner[functionEnd+1:])
	if !strings.HasSuffix(rest, functionClose) {
		return false
	}
	rest = strings.TrimSpace(strings.TrimSuffix(rest, functionClose))
	for rest != "" {
		if !strings.HasPrefix(rest, parameterPrefix) {
			return false
		}
		parameterEnd := strings.IndexByte(rest, '>')
		if parameterEnd <= len(parameterPrefix) {
			return false
		}
		if strings.TrimSpace(rest[len(parameterPrefix):parameterEnd]) == "" {
			return false
		}
		valueAndFollowing := rest[parameterEnd+1:]
		valueEnd := strings.Index(valueAndFollowing, parameterClose)
		if valueEnd < 0 {
			return false
		}
		rest = strings.TrimSpace(valueAndFollowing[valueEnd+len(parameterClose):])
	}
	return true
}

// Why this file imports go-openai directly, and why go.mod promotes it from an
// indirect dependency: the HTTP status is carried on a STRUCT FIELD
// (HTTPStatusCode) rather than behind a method, so there is no interface to
// depend on instead and no way to reach it without naming the type. The
// alternative was matching the framework's error TEXT, which classifyAskFailure
// forbids in as many words — every arm is reached by a type, so that the typed
// chain survives eino. A pinned dependency we can see in go.mod is the lesser
// coupling: if eino changes adapters this stops compiling, which is exactly the
// failure we want, rather than silently classifying nothing.

// EndpointErrorSentence turns a typed HTTP response from the model endpoint
// into a sentence a person can act on. The adapter preserves the provider's
// response type through its own APIError wrapper or the underlying
// RequestError, so this owner can classify it without exposing framework text.
func EndpointErrorSentence(err error, model string) (string, bool) {
	var requestErr *openai.RequestError
	var adapterAPIError *einoopenai.APIError
	var clientAPIError *openai.APIError
	if !errors.As(err, &requestErr) &&
		!errors.As(err, &adapterAPIError) &&
		!errors.As(err, &clientAPIError) {
		return "", false
	}

	status := 0
	if requestErr != nil {
		status = requestErr.HTTPStatusCode
	}
	if adapterAPIError != nil && adapterAPIError.HTTPStatusCode != 0 {
		status = adapterAPIError.HTTPStatusCode
	}
	if clientAPIError != nil && clientAPIError.HTTPStatusCode != 0 {
		status = clientAPIError.HTTPStatusCode
	}
	if status == 0 {
		return "", false
	}

	var code any
	if adapterAPIError != nil {
		code = adapterAPIError.Code
	} else if clientAPIError != nil {
		code = clientAPIError.Code
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "the model endpoint rejected the credential. Check the endpoint's API key or credential, then ask again.", true
	case http.StatusNotFound:
		if isModelNotFound(code) {
			return fmt.Sprintf("the model endpoint could not find model %q. Check the model id, then ask again.", model), true
		}
		return "the model endpoint returned 404 Not Found. Check the endpoint's address — the path may be wrong; an OpenAI-compatible base URL usually ends in /v1 — then ask again.", true
	default:
		return endpointStatusSentence(status), true
	}
}

func isModelNotFound(code any) bool {
	value, ok := code.(string)
	return ok && value == "model_not_found"
}

func endpointStatusSentence(status int) string {
	statusText := http.StatusText(status)
	if statusText == "" {
		return fmt.Sprintf("the model endpoint returned HTTP %d. Check the endpoint's configuration or status, then ask again.", status)
	}
	return fmt.Sprintf("the model endpoint returned HTTP %d %s. Check the endpoint's configuration or status, then ask again.", status, statusText)
}
