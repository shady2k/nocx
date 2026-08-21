package assistant

// readScreen tests (nocx-ljfwz): the first renderer-executed tool. The
// middleware's InRenderer branch (design §6.6 — execution differs by exactly
// one field of the declaration), the session narrowing (criterion 2 —
// asserted by trying: a grant naming session A cannot read session B's
// screen, and the renderer is never asked), and the window contract of the
// return (design §4.4).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// recordingRequester is the renderer-request seam with a call log and a
// scripted answer: the readScreen tests assert what the tool ASKED the
// renderer — the "asserted by trying, not by inspecting" seam.
type recordingRequester struct {
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

// TestExecuteReadScreen_SessionOutsideGrantNeverRequests is criterion 2:
// a grant naming session A cannot read session B's screen — asserted by
// trying, at the executor, through the narrowed capability. Naming B is
// refused BEFORE any renderer request: the recording requester proves the
// broker was never asked about B. The paired end: a read of A succeeds and
// the requester was asked exactly about A.
func TestExecuteReadScreen_SessionOutsideGrantNeverRequests(t *testing.T) {
	reader := agenttools.NewScreenReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}})
	req := &recordingRequester{body: liveFrameBody("hello", "world")}

	_, err := executeReadScreen(context.Background(), reader, req, json.RawMessage(`{"sessionId":"session-b"}`))
	if err == nil || !strings.Contains(err.Error(), "outside the run's grant") {
		t.Fatalf("read of session-b error = %v, want the grant refusal", err)
	}
	if calls := req.calls(); len(calls) != 0 {
		t.Fatalf("a refused session reached the renderer: %+v", calls)
	}

	out, err := executeReadScreen(context.Background(), reader, req, json.RawMessage(`{"sessionId":"session-a"}`))
	if err != nil {
		t.Fatalf("read of session-a failed: %v", err)
	}
	calls := req.calls()
	if len(calls) != 1 || calls[0].sessionID != "session-a" || calls[0].region != nil {
		t.Fatalf("requester asked %+v, want exactly one default-region read of session-a", calls)
	}
	var res struct {
		SessionID string `json:"sessionId"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if res.SessionID != "session-a" || res.Text != "hello\nworld" {
		t.Fatalf("result = %+v, want session-a with text hello\\nworld", res)
	}
}

// TestMiddleware_ReadScreenRefusedOutsideGrantTerminates is the policy half
// of the same rule, through the real middleware: a model call naming a
// session the grant does not cover is REFUSED (terminal — ErrPolicyRefused),
// and the renderer is never asked. The grant names session-a; the model
// names session-b.
func TestMiddleware_ReadScreenRefusedOutsideGrantTerminates(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	req := &recordingRequester{body: liveFrameBody("x")}
	mw := middlewareForWithRequester(t, grant, &fakeLedger{}, nil, req)

	_, err := wrappedEndpoint(mw, "readScreen", "c1", `{"sessionId":"session-b"}`)
	if !errors.Is(err, ErrPolicyRefused) {
		t.Fatalf("out-of-grant readScreen error = %v, want ErrPolicyRefused", err)
	}
	if calls := req.calls(); len(calls) != 0 {
		t.Fatalf("a refused call reached the renderer: %+v", calls)
	}

	out, err := wrappedEndpoint(mw, "readScreen", "c2", `{"sessionId":"session-a"}`)
	if err != nil {
		t.Fatalf("in-grant readScreen failed: %v", err)
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

	if _, err := wrappedEndpoint(mw, "readScreen", "c1", `{"sessionId":"session-a","region":{"start":1,"end":1}}`); err == nil {
		t.Fatal("a zero-span region was accepted, want a refusal")
	}
	if calls := req.calls(); len(calls) != 0 {
		t.Fatalf("a malformed region reached the renderer: %+v", calls)
	}

	if _, err := wrappedEndpoint(mw, "readScreen", "c2", `{"sessionId":"session-a","region":{"start":1,"end":3}}`); err != nil {
		t.Fatalf("a valid region was refused: %v", err)
	}
	calls := req.calls()
	if len(calls) != 1 || calls[0].region == nil || calls[0].region.Start != 1 || calls[0].region.End != 3 {
		t.Fatalf("requester asked %+v, want region [1,3)", calls)
	}
}

// TestExecuteReadScreen_WindowIsHonest is design §4.4's window contract on
// the readScreen return: total (the screen's height), the window that was
// asked for, the window that was actually returned (the frame's span — a
// region past the end clamps, never errors), and the text.
func TestExecuteReadScreen_WindowIsHonest(t *testing.T) {
	reader := agenttools.NewScreenReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}})
	req := &recordingRequester{body: liveFrameBody("one", "two", "three")}

	// Ask for rows [0, 1000) of a 3-row screen: the frame clamps to its own
	// span and the window states it — answered honestly, never an error.
	out, err := executeReadScreen(context.Background(), reader, req, json.RawMessage(`{"sessionId":"session-a","region":{"start":0,"end":1000}}`))
	if err != nil {
		t.Fatalf("window past the end errored: %v", err)
	}
	var res struct {
		Total  int `json:"total"`
		Window struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"window"`
		Returned struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"returned"`
		Text     string `json:"text"`
		Identity struct {
			Generation int `json:"generation"`
		} `json:"identity"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if res.Total != 3 {
		t.Errorf("total = %d, want 3 (the screen's height)", res.Total)
	}
	if res.Window.Start != 0 || res.Window.End != 1000 {
		t.Errorf("window = [%d,%d), want the asked [0,1000)", res.Window.Start, res.Window.End)
	}
	if res.Returned.Start != 0 || res.Returned.End != 3 {
		t.Errorf("returned = [%d,%d), want the frame's actual [0,3)", res.Returned.Start, res.Returned.End)
	}
	if res.Text != "one\ntwo\nthree" {
		t.Errorf("text = %q, want one\\ntwo\\nthree", res.Text)
	}
	if res.Identity.Generation != 7 {
		t.Errorf("identity generation = %d, want 7 (the capture identity the frame carried)", res.Identity.Generation)
	}
}

// TestMiddleware_ReadScreenWithoutRequesterIsHonest: a run whose transport
// wired no renderer-request seam reports the wiring gap as an error — a
// declared InRenderer tool never silently no-ops.
func TestMiddleware_ReadScreenWithoutRequesterIsHonest(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	mw := middlewareFor(t, grant, &fakeLedger{}, nil) // requester nil

	_, err := wrappedEndpoint(mw, "readScreen", "c1", `{"sessionId":"session-a"}`)
	if err == nil || !strings.Contains(err.Error(), "no renderer requester is wired") {
		t.Fatalf("error = %v, want the wiring-gap refusal", err)
	}
}

// TestExecuteReadScreen_FailedOutcomeSurfaces: a renderer that answers
// "failed" surfaces as a tool error — the model hears why, and the run is
// not left hanging (the broker's honest terminal answer crosses as an
// error, not a hang).
func TestExecuteReadScreen_FailedOutcomeSurfaces(t *testing.T) {
	reader := agenttools.NewScreenReader([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}})
	req := &recordingRequester{err: errors.New("readScreen: the renderer could not capture the screen: no such session")}

	_, err := executeReadScreen(context.Background(), reader, req, json.RawMessage(`{"sessionId":"session-a"}`))
	if err == nil || !strings.Contains(err.Error(), "could not capture the screen") {
		t.Fatalf("error = %v, want the renderer's failure sentence", err)
	}
}
