package assistant

import (
	"context"
	"net/http"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
)

// ModelTarget is resolved by the existing role and credential owners. A
// generator borrows this material for the operation; it does not retain it.
type ModelTarget struct {
	Key     credential.Secret
	BaseURL string
	Model   string
	Headers []Header
}

// TextGenerator performs one tool-free completion. Workflow prompts, parsing,
// budgets and memoization remain with the calling Go operation.
type TextGenerator interface {
	Generate(ctx context.Context, target ModelTarget, messages []Message) (string, error)
}

// ModelHTTPFactory supplies the shared guarded transport, including wire
// recording and redirect policy. It owns no endpoint or credential material.
type ModelHTTPFactory interface {
	NewModelHTTPClient() *http.Client
}

type modelHTTPFactory struct{ client *http.Client }

func NewModelHTTPFactory(logger log.Logger, recorder WireRecorder) ModelHTTPFactory {
	return &modelHTTPFactory{client: newGuardedHTTPClient(logger, nil, recorder)}
}

func (f *modelHTTPFactory) NewModelHTTPClient() *http.Client { return f.client }

// WithModelHeaders identifies the endpoint's custom headers to the shared
// redirect guard. Request builders still set their actual values themselves.
func WithModelHeaders(ctx context.Context, headers []Header) context.Context {
	names := make([]string, len(headers))
	for i, header := range headers {
		names[i] = http.CanonicalHeaderKey(header.Name)
	}
	return withCustomHeaderNames(ctx, names)
}
