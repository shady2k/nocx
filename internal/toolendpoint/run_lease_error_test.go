package toolendpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// These are socket responses, not just classifier results: delivery must not
// replace the lease verdict with its underlying canceled request context.
func TestEndpointRunLeaseFailurePreservesSafeSentence(t *testing.T) {
	for _, reason := range []content.TerminationReason{
		content.TermTimeout, content.TermInactivity, content.TermOutputBudget, content.TermAgentDeclined,
	} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/submissionExpired=%t", reason, expired), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				lease := &assistant.RunLeaseError{
					Reason: reason, SubmissionExpired: expired,
					Err: fmt.Errorf("private backend detail: %w", context.Canceled),
				}
				dispatch := &testDispatcher{err: fmt.Errorf("private dispatch detail: %w", lease)}
				auth := &testAuthorizer{inv: assistant.ToolInvocation{Context: ctx}}
				cfg := endpointConfig(t, auth, dispatch)
				cfg.Logger = logtest.Slog(t)
				ep := startEndpoint(t, cfg)
				conn := dialEndpoint(t, ep)
				if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"session.run","params":{}}`+"\n"); err != nil {
					t.Fatal(err)
				}
				response := readResponse(t, conn)
				want := assistant.RunLeaseSentence(reason, expired)
				if response.Error == nil || response.Error.Code != rpcInternalError || response.Error.Message != want || response.Error.Data == nil || response.Error.Data.Reason != want {
					t.Fatalf("response error = %+v, want internal error with safe lease sentence %q", response.Error, want)
				}
				if len(response.Result) != 0 {
					t.Fatalf("failed lease returned a result: %s", response.Result)
				}
			})
		}
	}
}

func TestEndpointUnclassifiedFailureDoesNotExposeBackendDetail(t *testing.T) {
	const private = "private backend credential detail"
	dispatch := &testDispatcher{err: errors.New(private)}
	cfg := endpointConfig(t, &testAuthorizer{}, dispatch)
	cfg.Logger = logtest.Slog(t)
	ep := startEndpoint(t, cfg)
	conn := dialEndpoint(t, ep)
	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"session.run","params":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	response := readResponse(t, conn)
	if response.Error == nil || response.Error.Code != rpcInternalError || response.Error.Message != "internal error" || response.Error.Data == nil {
		t.Fatalf("response error = %+v, want redacted internal error", response.Error)
	}
	if strings.Contains(response.Error.Message, private) || strings.Contains(response.Error.Data.Reason, private) {
		t.Fatalf("backend detail reached wire: %+v", response.Error)
	}
}

func TestEndpointCallerCancellationWinsLateLeaseFailure(t *testing.T) {
	release := make(chan struct{})
	dispatch := &testDispatcher{
		err:     &assistant.RunLeaseError{Reason: content.TermTimeout, Err: context.Canceled},
		started: make(chan struct{}), cancelled: make(chan struct{}), release: release,
	}
	cfg := endpointConfig(t, &testAuthorizer{}, dispatch)
	cfg.Logger = logtest.Slog(t)
	ep := startEndpoint(t, cfg)
	client := dialClient(t, ep)
	client.request(1, "session.run", `{}`)
	<-dispatch.started
	client.notify("calls.cancel", `{"requestId":1}`)
	<-dispatch.cancelled
	close(release)
	response := client.response()
	if response.Error == nil || response.Error.Code != rpcDomainError || response.Error.Message != "call abandoned" {
		t.Fatalf("response error = %+v, want caller cancellation rather than late lease failure", response.Error)
	}
}
