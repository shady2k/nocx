package assistant

// session.read through the middleware (nocx-ljfwz, nocx-2ryxf.1): the
// session narrowing, asserted by TRYING — a grant naming session A cannot
// read session B's screen, and the renderer is never asked — plus the region
// the call carries and the refusal a run with no requester gets.
//
// These began as the readScreen tool's tests. session.read took that tool's
// job, so what they drive is session.read; three siblings that called the
// removed executeReadScreen directly went with the tool it belonged to, and
// what they asserted is covered on the live path by session_test.go's
// bounds and failure-propagation tests.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// recordingRequester is the renderer-request seam with a call log and a
// scripted answer: the readScreen tests assert what the tool ASKED the
// renderer — the "asserted by trying, not by inspecting" seam.
type recordingRequester struct {
	unscriptedBlocks
	mu    sync.Mutex
	asked []askedScreen
	body  json.RawMessage
	err   error
}

type askedScreen struct {
	sessionID string
	region    *FrameRegion
}

func (r *recordingRequester) RequestScreen(ctx context.Context, sessionID string, region *FrameRegion) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, askedScreen{sessionID: sessionID, region: region})
	if r.err != nil {
		return nil, r.err
	}
	return r.body, nil
}

// RequestRun exists only to satisfy the RendererRequester interface: the
// readScreen tests never call it. The run tool's tests use recordingRunner.
func (r *recordingRequester) RequestRun(ctx context.Context, sessionID string, command string) (json.RawMessage, error) {
	return nil, errors.New("readscreen test: RequestRun is not scripted")
}

func (r *recordingRequester) calls() []askedScreen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]askedScreen(nil), r.asked...)
}

// liveFrameBody builds a minimal validated frame body the way the transport's
// readScreen kind resolves one: cells rows, cursor, identity and range.
func liveFrameBody(rows ...string) json.RawMessage {
	cells := make([]map[string]any, 0, len(rows))
	for _, text := range rows {
		cs := make([]map[string]any, 0, len(text))
		for _, ch := range text {
			cs = append(cs, map[string]any{
				"char":  string(ch),
				"attrs": map[string]any{},
			})
		}
		cells = append(cells, map[string]any{"kind": "cells", "cells": cs})
	}
	b, _ := json.Marshal(map[string]any{
		"rows":   cells,
		"cursor": map[string]any{"line": 0, "col": 0},
		"identity": map[string]any{
			"buffer": map[string]any{"kind": "normal"},
			"cols":   80, "rows": len(rows), "generation": 7,
		},
		"range": map[string]any{"start": 0, "end": len(rows)},
	})
	return b
}

func sessionGrant(sessionID string, policy content.EffectPolicy) content.Grant {
	// Minted as the transport mints: the matrix AsGrant with the session
	// as the base scope, so the row scopes carry the session bound.
	return policy.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: sessionID}})
}

// TestMiddleware_ReadScreenRefusedOutsideGrantIsAResult is the policy half
// of the same rule, through the real middleware: a model call naming a
// session the grant does not cover is REFUSED — the refusal is the call's
// result in our words (nocx-uvac6.1), and the renderer is never asked. The
// grant names session-a; the model names session-b.
func TestMiddleware_ReadScreenRefusedOutsideGrantIsAResult(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	req := &recordingRequester{body: liveFrameBody("x")}
	mw := middlewareForWithRequester(t, grant, &fakeLedger{}, nil, req)

	out, err := wrappedEndpoint(mw, "session.read", "c1", `{"sessionId":"session-b"}`)
	if err != nil {
		t.Fatalf("out-of-grant session.read error = %v, want the refusal as a tool result", err)
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, "session.read") {
		t.Fatalf("refusal result = %q, want a refusal naming the tool in our words", out)
	}
	if calls := req.calls(); len(calls) != 0 {
		t.Fatalf("a refused call reached the renderer: %+v", calls)
	}

	out, err = wrappedEndpoint(mw, "session.read", "c2", `{"sessionId":"session-a"}`)
	if err != nil {
		t.Fatalf("in-grant session.read failed: %v", err)
	}
	calls := req.calls()
	if len(calls) != 1 || calls[0].sessionID != "session-a" {
		t.Fatalf("requester asked %+v, want exactly one read of session-a", calls)
	}
	if !strings.Contains(out, `"text":"x"`) {
		t.Fatalf("result %q lacks the frame's text", out)
	}
}

// TestMiddleware_ReadScreenRegionTravels asserts the requested region is
// carried to the renderer (region? is the tool's one parameter beyond the
// session), and a malformed region is refused before the request.
func TestMiddleware_ReadScreenRegionTravels(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	req := &recordingRequester{body: liveFrameBody("a", "b", "c")}
	mw := middlewareForWithRequester(t, grant, &fakeLedger{}, nil, req)

	if _, err := wrappedEndpoint(mw, "session.read", "c1", `{"sessionId":"session-a","start":1,"count":0}`); err == nil {
		t.Fatal("a zero-span region was accepted, want a refusal")
	}
	if calls := req.calls(); len(calls) != 0 {
		t.Fatalf("a malformed region reached the renderer: %+v", calls)
	}

	if _, err := wrappedEndpoint(mw, "session.read", "c2", `{"sessionId":"session-a","start":1,"count":2}`); err != nil {
		t.Fatalf("a valid region was refused: %v", err)
	}
	calls := req.calls()
	if len(calls) != 1 || calls[0].region == nil || calls[0].region.Start != 1 || calls[0].region.End != 3 {
		t.Fatalf("requester asked %+v, want region [1,3)", calls)
	}
}

// TestMiddleware_ReadScreenWithoutRequesterIsHonest: a run whose transport
// wired no renderer-request seam reports the wiring gap as an error — a
// declared InRenderer tool never silently no-ops.
func TestMiddleware_ReadScreenWithoutRequesterIsHonest(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	mw := middlewareFor(t, grant, &fakeLedger{}, nil) // requester nil

	_, err := wrappedEndpoint(mw, "session.read", "c1", `{"sessionId":"session-a"}`)
	if err == nil || !strings.Contains(err.Error(), "no renderer requester is wired") {
		t.Fatalf("error = %v, want the wiring-gap refusal", err)
	}
}
