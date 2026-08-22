package apisend

// The two shapes every send now comes back in, asserted in one place.
//
// Send returns an EXCHANGE and an error, and the error means one thing only:
// a request this package refuses to send at all. So a test that used to say
// "err != nil means it failed" is no longer asking about the send — it is
// asking about the calling contract. These two helpers keep the difference
// visible at every call site, and each of them checks the invariants the
// contract states so that no individual test has to remember them:
//
//   - an answered exchange has a response and no failure;
//   - a failure has no response and a phase;
//   - and BOTH carry the composed request, because the sender has it before
//     it dials. That last one is the whole of task 2 and it is asserted here
//     rather than in one test, so every failure test in this package proves
//     it in passing.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

// answered asserts the exchange came back and returns the response.
func answered(t *testing.T, ex Exchange, err error) Response {
	t.Helper()
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ex.Outcome != Answered {
		reason := "(no failure recorded)"
		if ex.Failure != nil {
			reason = string(ex.Failure.Phase) + ": " + ex.Failure.Reason
		}
		t.Fatalf("outcome = %q, want answered — %s", ex.Outcome, reason)
	}
	if ex.Response == nil {
		t.Fatal("an answered exchange carries no response")
	}
	if ex.Failure != nil {
		t.Errorf("an answered exchange carries a failure: %+v", *ex.Failure)
	}
	if ex.Certificates == nil {
		t.Error("Certificates is nil; a chain of none is [] (the renderer's first .map on a null throws)")
	}
	return *ex.Response
}

// failed asserts the attempt did not answer and returns its failure. It
// deliberately accepts a stop as well as a failure: both are exchanges that
// ended without a response, and the tests that care which one assert the
// outcome themselves.
func failed(t *testing.T, ex Exchange, err error) Failure {
	t.Helper()
	if err != nil {
		t.Fatalf("Send returned an ERROR rather than a failed exchange: %v — an "+
			"error is reserved for a request this package refuses to send", err)
	}
	if ex.Outcome == Answered {
		t.Fatal("the send answered; this test is about an attempt that does not")
	}
	if ex.Failure == nil {
		t.Fatalf("outcome = %q with no failure on it", ex.Outcome)
	}
	if ex.Response != nil {
		t.Error("an exchange that did not answer carries a response")
	}
	if ex.Failure.Reason == "" {
		t.Error("a failure with no reason; the run has nothing to show a person")
	}
	if ex.Certificates == nil {
		t.Error("Certificates is nil; a chain of none is []")
	}
	return *ex.Failure
}

// failedAt is failed plus the phase, which is what most of the failure tests
// in this package are actually about.
func failedAt(t *testing.T, ex Exchange, err error, want Phase) Failure {
	t.Helper()
	f := failed(t, ex, err)
	if f.Phase != want {
		t.Errorf("phase = %q, want %q (reason: %s)", f.Phase, want, f.Reason)
	}
	// AND THE REQUEST SURVIVED — for EVERY phase, compose included. The
	// sender has the composed text before it dials, and at compose it has
	// the request as written; dropping either is the defect this whole
	// change is against, so it is asserted here rather than in the one test
	// somebody thought to write it in.
	if ex.Request.Text == "" {
		t.Error("the failed exchange carries no request text; the sender had it before it dialled")
	}
	if ex.Request.Spans == nil {
		t.Error("Request.Spans is nil; a side with nothing to mark is []")
	}
	return f
}

// The resolver's answer rides the attempt. It is a separate fact from the
// address that answered: a name with several records says the one that
// answered was one of several, and a name that resolves to something stale
// says why a request went somewhere nobody expected.
func TestExchange_CarriesWhatTheResolverAnswered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New()
	ex, err := c.Send(context.Background(), apicoll.Request{Method: "GET", URL: srv.URL}, Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ex.Outcome != Answered {
		t.Fatalf("outcome = %q", ex.Outcome)
	}
	// httptest serves on 127.0.0.1 as an address literal, so there is no
	// lookup to make and the answer is EMPTY — never nil, because the wire
	// declares a list and a nil marshals as null.
	if ex.DNSAddresses == nil {
		t.Fatal("DNSAddresses is nil; the wire declares a list and [] is the empty answer")
	}
	if len(ex.DNSAddresses) != 0 {
		t.Fatalf("DNSAddresses = %v, want empty for an address literal", ex.DNSAddresses)
	}
}
