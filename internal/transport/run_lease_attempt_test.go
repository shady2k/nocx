package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type runAttemptTestConn struct{}

func TestRunLeaseAttemptBindingKeepsConcurrentRunsDistinct(t *testing.T) {
	conn := &runAttemptTestConn{}
	broker := NewBroker(
		func() []Conn { return []Conn{conn} },
		func(Conn, string, json.RawMessage) error { return nil },
	)
	kind := RequestKind{
		NotifyMethod:  "test.runRequest",
		ResolveMethod: "test.runResolved",
		NoClientErr:   errors.New("no client"),
		Resolve: func(json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}
	first := &runLease{}
	second := &runLease{}
	firstIDs := make(chan string, 1)
	secondIDs := make(chan string, 1)
	firstKind := kind
	firstKind.BeforeDeliver = func(requestID string) {
		broker.registerRunLease(requestID, first)
		firstIDs <- requestID
	}
	secondKind := kind
	secondKind.BeforeDeliver = func(requestID string) {
		broker.registerRunLease(requestID, second)
		secondIDs <- requestID
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		var result map[string]any
		firstDone <- broker.Request(context.Background(), firstKind, map[string]string{"run": "first"}, &result)
	}()
	go func() {
		var result map[string]any
		secondDone <- broker.Request(context.Background(), secondKind, map[string]string{"run": "second"}, &result)
	}()
	firstID := <-firstIDs
	secondID := <-secondIDs
	if firstID == secondID {
		t.Fatalf("concurrent run request ids collided: %q", firstID)
	}
	if !broker.bindRunAttempt(firstID, "attempt-first") {
		t.Fatal("first run attempt binding was refused")
	}
	if !broker.bindRunAttempt(secondID, "attempt-second") {
		t.Fatal("second run attempt binding was refused")
	}
	if got, ok := broker.runAttemptForLease(first); !ok || got != "attempt-first" {
		t.Fatalf("first run attempt = %q (ok=%v), want attempt-first", got, ok)
	}
	if got, ok := broker.runAttemptForLease(second); !ok || got != "attempt-second" {
		t.Fatalf("second run attempt = %q (ok=%v), want attempt-second", got, ok)
	}
	if broker.bindRunAttempt("foreign-request", "attempt-foreign") {
		t.Fatal("a stale request id accepted an attempt binding")
	}
	if !broker.bindRunAttempt(firstID, "attempt-other") {
		// The first binding is deliberately immutable.
	} else {
		t.Fatal("a run request accepted a second attempt binding")
	}
	if got := broker.Resolve("test.runResolved", json.RawMessage(`{"requestId":"`+firstID+`"}`), conn); got.Code != 0 {
		t.Fatalf("first resolution refused: %+v", got)
	}
	if got := broker.Resolve("test.runResolved", json.RawMessage(`{"requestId":"`+secondID+`"}`), conn); got.Code != 0 {
		t.Fatalf("second resolution refused: %+v", got)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first request: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second request: %v", err)
	}
	if got, ok := broker.runAttemptForLease(first); !ok || got != "attempt-first" {
		t.Fatalf("first run attempt ended with broker request, got %q (ok=%v)", got, ok)
	}
	if got, ok := broker.runAttemptForLease(second); !ok || got != "attempt-second" {
		t.Fatalf("second run attempt ended with broker request, got %q (ok=%v)", got, ok)
	}
	broker.unregisterRunLease(first)
	broker.unregisterRunLease(second)
	if _, ok := broker.runAttemptForLease(first); ok {
		t.Fatal("first run lease binding survived request completion")
	}
	if _, ok := broker.runAttemptForLease(second); ok {
		t.Fatal("second run lease binding survived request completion")
	}
}
