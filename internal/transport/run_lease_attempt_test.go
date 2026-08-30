package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type runAttemptTestConn struct{ name string }

type runAttemptDelivery struct {
	conn      Conn
	requestID string
}

func TestRunLeaseAttemptBindingKeepsConcurrentRunsDistinct(t *testing.T) {
	connA := &runAttemptTestConn{name: "A"}
	connB := &runAttemptTestConn{name: "B"}
	deliveries := make(chan runAttemptDelivery, 2)
	snapshots := 0
	broker := NewBroker(
		func() []Conn {
			snapshots++
			if snapshots == 1 {
				return []Conn{connA}
			}
			return []Conn{connB}
		},
		func(conn Conn, _ string, params json.RawMessage) error {
			var envelope struct {
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(params, &envelope); err != nil {
				return err
			}
			deliveries <- runAttemptDelivery{conn: conn, requestID: envelope.RequestID}
			return nil
		},
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
	firstID := <-firstIDs
	firstDelivery := <-deliveries
	if firstDelivery.conn != connA || firstDelivery.requestID != firstID {
		t.Fatalf("first run notification = %#v, want connection A and request %q", firstDelivery, firstID)
	}
	go func() {
		var result map[string]any
		secondDone <- broker.Request(context.Background(), secondKind, map[string]string{"run": "second"}, &result)
	}()
	secondID := <-secondIDs
	secondDelivery := <-deliveries
	if secondDelivery.conn != connB || secondDelivery.requestID != secondID {
		t.Fatalf("second run notification = %#v, want connection B and request %q", secondDelivery, secondID)
	}
	if firstID == secondID {
		t.Fatalf("concurrent run request ids collided: %q", firstID)
	}

	if broker.bindRunAttempt(firstID, "attempt-foreign", connB) {
		t.Fatal("connection B bound an attempt to connection A's request")
	}
	if _, ok := broker.runAttemptForLease(first); ok {
		t.Fatal("connection B's refused bind changed connection A's lease")
	}
	if !broker.bindRunAttempt(firstID, "attempt-first", connA) {
		t.Fatal("connection A could not bind its own run request")
	}
	if !broker.bindRunAttempt(secondID, "attempt-second", connB) {
		t.Fatal("connection B could not bind its own run request")
	}
	if got, ok := broker.runAttemptForLease(first); !ok || got != "attempt-first" {
		t.Fatalf("first run attempt = %q (ok=%v), want attempt-first", got, ok)
	}
	if got, ok := broker.runAttemptForLease(second); !ok || got != "attempt-second" {
		t.Fatalf("second run attempt = %q (ok=%v), want attempt-second", got, ok)
	}
	if broker.bindRunAttempt(firstID, "attempt-other", connA) {
		t.Fatal("a run request accepted a second attempt binding")
	}
	if broker.bindRunAttempt("foreign-request", "attempt-foreign", connA) {
		t.Fatal("a stale request id accepted an attempt binding")
	}
	if got := broker.Resolve("test.runResolved", json.RawMessage(`{"requestId":"`+firstID+`"}`), connA); got.Code != 0 {
		t.Fatalf("first resolution refused: %+v", got)
	}
	if got := broker.Resolve("test.runResolved", json.RawMessage(`{"requestId":"`+secondID+`"}`), connB); got.Code != 0 {
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
		t.Fatal("first run lease binding survived RequestRun cleanup")
	}
	if _, ok := broker.runAttemptForLease(second); ok {
		t.Fatal("second run lease binding survived RequestRun cleanup")
	}
}

func TestRunLeaseAttemptBindingRejectsUncomparableConnection(t *testing.T) {
	broker := NewBroker(
		func() []Conn { return []Conn{&runAttemptTestConn{name: "A"}} },
		func(Conn, string, json.RawMessage) error { return nil },
	)
	if broker.bindRunAttempt("request", "attempt", []byte("not-comparable")) {
		t.Fatal("uncomparable connection accepted an attempt binding")
	}
}
