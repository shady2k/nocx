package transport

// The architectural prohibition, tested as a prohibition rather than as a
// habit: a control method with no declared params validator must not be able
// to exist in a built server, and every request must pass through the
// validator before its handler is entered.
//
// This file exists because the defect it guards against was invisible:
// endpoints.probe shipped checking only that its base URL was non-empty while
// dialling that URL, putting its model into a request body and its key into an
// HTTP header. Nothing in the code said anything was missing, because nothing
// required anything to be there.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/transport/control"
)

// TestBuildMethodSpecs_RefusesAMethodWithNoValidator is the prohibition
// itself. A registration without a validator must fail the server BUILD — not
// warn, not default to permissive, not pass a test somebody remembered to
// write.
func TestBuildMethodSpecs_RefusesAMethodWithNoValidator(t *testing.T) {
	specs := []methodSpec{{
		method:     "example.method",
		submission: control.ImmediateSubmission{},
		build: func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(context.Context, jsonrpcRequest) {}
		},
		// validate deliberately absent — this is the whole test.
	}}
	if _, err := buildMethodSpecs(specs); err == nil {
		t.Fatal("buildMethodSpecs accepted a method with no params validator; " +
			"the prohibition is not a prohibition")
	} else if !strings.Contains(err.Error(), "without a params validator") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

// TestValidated_RefusesBeforeTheHandlerRuns proves the middleware is a gate
// and not an observer: a refused request must never reach the handler, so a
// handler cannot touch a store, a vault or a socket on params nobody accepted.
func TestValidated_RefusesBeforeTheHandlerRuns(t *testing.T) {
	entered := false
	next := func(context.Context, jsonrpcRequest) { entered = true }
	rec := &recordingResponder{}
	h := validated(methodSpec{validate: params(func(json.RawMessage) string { return "no" })}, next, rec)

	h(context.Background(), jsonrpcRequest{ID: json.RawMessage(`1`), Params: json.RawMessage(`{"a":1}`)})

	if entered {
		t.Fatal("the handler ran despite a refusing validator")
	}
	if rec.code != -32602 {
		t.Fatalf("error code = %d, want -32602", rec.code)
	}
}

// TestValidated_AdmitsWhatTheValidatorAccepts is the paired positive: a
// validator that accepts must let the handler run, or the gate is a wall.
func TestValidated_AdmitsWhatTheValidatorAccepts(t *testing.T) {
	entered := false
	next := func(context.Context, jsonrpcRequest) { entered = true }
	h := validated(methodSpec{validate: params(func(json.RawMessage) string { return "" })}, next, &recordingResponder{})

	h(context.Background(), jsonrpcRequest{ID: json.RawMessage(`1`), Params: json.RawMessage(`{"a":1}`)})

	if !entered {
		t.Fatal("the handler did not run despite an accepting validator")
	}
}

// TestNoParams_IsAnAssertionNotAnExemption: a method that declares it takes no
// params must REFUSE a payload rather than ignore one. A renderer that starts
// sending something has to be met by a deliberate change here.
func TestNoParams_IsAnAssertionNotAnExemption(t *testing.T) {
	v := noParams()
	for _, ok := range []string{"", "null", "{}", "  "} {
		if msg := v(json.RawMessage(ok)); msg != "" {
			t.Fatalf("noParams refused %q: %s", ok, msg)
		}
	}
	for _, bad := range []string{`{"a":1}`, `[1]`, `"x"`, `5`} {
		if msg := v(json.RawMessage(bad)); msg == "" {
			t.Fatalf("noParams accepted a payload: %s", bad)
		}
	}
}

// recordingResponder captures the one error a refused request produces.
type recordingResponder struct {
	code int
	msg  string
}

func (r *recordingResponder) TryResult(json.RawMessage, json.RawMessage) error { return nil }
func (r *recordingResponder) TryError(_ json.RawMessage, e RPCError) error {
	r.code, r.msg = e.Code, e.Message
	return nil
}
func (r *recordingResponder) TryNotify(string, json.RawMessage) error { return nil }

// TestValidated_UnavailableAnswersMethodNotFound: a method whose domain is not
// wired says so, whatever it was sent. The caller's next move is to stop
// calling it, not to fix its arguments — and the validator never runs, so a
// method that does not exist cannot diagnose the shape of params sent to it.
func TestValidated_UnavailableAnswersMethodNotFound(t *testing.T) {
	validatorRan := false
	spec := methodSpec{
		available: func() bool { return false },
		validate: params(func(json.RawMessage) string {
			validatorRan = true
			return "params are wrong too"
		}),
	}
	entered := false
	rec := &recordingResponder{}
	h := validated(spec, func(context.Context, jsonrpcRequest) { entered = true }, rec)

	h(context.Background(), jsonrpcRequest{ID: json.RawMessage(`1`), Params: json.RawMessage(`{"a":1}`)})

	if entered {
		t.Fatal("an unavailable method reached its handler")
	}
	if validatorRan {
		t.Fatal("the validator ran for a method that does not exist")
	}
	if rec.code != -32601 {
		t.Fatalf("error code = %d, want -32601", rec.code)
	}
}
