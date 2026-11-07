package transport

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
)

type captureResponder struct {
	err  *RPCError
	data any
}

func (r *captureResponder) TryResult(json.RawMessage, json.RawMessage) error { return nil }
func (r *captureResponder) TryError(_ json.RawMessage, e RPCError) error {
	r.err = &e
	r.data = e.Data
	return nil
}
func (r *captureResponder) TryNotify(string, json.RawMessage) error { return nil }

// A call whose turn expired is answered with its declared outcome, not with
// the saturation class stage 8 removes. The reason is the declaration's own
// vocabulary so the renderer can say "did not start" rather than "refused".
func TestGitOperationExpiryWireShape(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		reason string
	}{
		{"turn expired before start", &capability.NotStartedError{Operation: "git.stage"}, "not-started"},
		{"mutation past its max", &capability.ExpiredError{Operation: "git.commit", Outcome: catalog.ExpiredOutcomeUnknown}, "outcome-unknown"},
		{"read past its max", &capability.ExpiredError{Operation: "git.status", Outcome: catalog.ExpiredRetryRead}, "retry-read"},
		// A suspect domain refused the work before it started, so even a
		// mutation answers not-started: nothing took effect.
		{"suspect domain", &capability.ExpiredError{Operation: "git.commit", Outcome: catalog.ExpiredOutcomeUnknown, Suspected: true}, "not-started"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &captureResponder{}
			answerGitOperationError(r, jsonrpcRequest{ID: json.RawMessage(`7`)}, tc.err)
			if r.err == nil {
				t.Fatal("no error answered")
			}
			if r.err.Code != GitOperationExpiredCode {
				t.Fatalf("code = %d, want %d", r.err.Code, GitOperationExpiredCode)
			}
			data, ok := r.data.(*gitErrorData)
			if !ok || data.Reason != tc.reason {
				t.Fatalf("data = %#v, want reason %q", r.data, tc.reason)
			}
			if r.err.Message == "" || r.err.Message == operationExpiredMessage {
				t.Fatalf("message = %q; the declared outcome must be named, not the fixed did-not-start sentence", r.err.Message)
			}
		})
	}
}

// A wait that ran out is answered as "did not start" for a git call too: one
// shape for every domain (ADR-8eej3e66 stage 8).
func TestGitsWaitThatRanOutAnswersNotStarted(t *testing.T) {
	r := &captureResponder{}
	answerGitOperationError(r, jsonrpcRequest{ID: json.RawMessage(`7`)},
		&capability.RefusedError{Rejection: control.Rejection{Kind: control.RejectionWaitExceeded, Scope: "git-checkout"}})
	if r.err == nil || r.err.Code != OperationExpiredCode {
		t.Fatalf("err = %+v, want the expiry code %d", r.err, OperationExpiredCode)
	}
	data, ok := r.data.(operationExpiredData)
	if !ok || data.Reason != string(catalog.ExpiredNotStarted) {
		t.Fatalf("data = %#v, want reason %q", r.data, catalog.ExpiredNotStarted)
	}
}
