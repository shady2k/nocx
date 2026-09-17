package assistant

// session.message (design §4.1, §8, Task 10): a message queued for a
// descendant's agent, delivered through a FIFO queue one delivery at a time,
// at most once per idempotency key — and its disjoint cancel form,
// `session.message { sessionId, cancel: id }` (§8.6).
//
// Mirrors Task 8/9's layering: this package owns the interface PaneMessages
// and the wire types the executor needs; internal/app owns the one
// production implementation (pane_messages.go), which this package cannot
// name (app depends on assistant, not the reverse).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/agenttools"
)

// MessagePhase is a message's own closed state set (design §8.3). Every
// session.message response and every session.read pendingMessages entry
// carries one of these — never a bare string a caller has to parse loosely.
type MessagePhase string

const (
	// In flight.
	PhaseQueued             MessagePhase = "queued"
	PhasePasting            MessagePhase = "pasting"
	PhaseAwaitingEcho       MessagePhase = "awaiting_echo"
	PhaseEntering           MessagePhase = "entering"
	PhaseAwaitingSubmission MessagePhase = "awaiting_submission"
	// Terminal.
	PhaseRefused         MessagePhase = "refused"
	PhaseFailedPartial   MessagePhase = "failed_partial"
	PhaseDeliveryUnknown MessagePhase = "delivery_unknown"
	PhasePartial         MessagePhase = "partial"
	PhaseWritten         MessagePhase = "written"
	PhaseSubmitted       MessagePhase = "submitted"
	PhaseCancelled       MessagePhase = "cancelled"
	PhaseIndeterminate   MessagePhase = "indeterminate"
)

// CancelResult is session.message's cancel-form response (design §8.6):
// exactly three shapes. Phase is the zero value ("") only for
// result "no_such_message" — the one response exempt from §8.3's phase
// rule, and the only case where Phase carries nothing.
type CancelResult struct {
	Result string // "cancelled" | "too_late" | "no_such_message"
	Phase  MessagePhase
}

// PaneMessages is session.message's whole write path: send (queue and,
// depending on `when`, attempt delivery) and cancel. access is
// RunContext.PaneAccess, carried as `any` for the identical layering reason
// PaneKeys.Send and PaneReader.Read already receive it that way.
type PaneMessages interface {
	// Send enqueues text under idempotency key id, delivering immediately
	// when when is "now" (tokenID names a target session.read minted of
	// kind input or working, design §8.1) or queuing for later delivery
	// when when is "free" (tokenID is ignored). Same id with the SAME
	// payload (text, when and — for "now" — the target's own kind) returns
	// the recorded phase; the same id with a DIFFERENT payload is an error
	// (design §8.4's id_reused).
	Send(ctx context.Context, access any, sessionID, text, when, id, tokenID string) (MessageView, error)
	// Cancel resolves id to the full server-derived MessageKey in namespace
	// "caller" and linearises on the queue claim (design §8.6). Idempotent:
	// a retry after a lost response gets the same answer.
	Cancel(ctx context.Context, access any, sessionID, id string) (CancelResult, error)
}

// sessionMessageSendParams is session.message's send-form wire request.
type sessionMessageSendParams struct {
	SessionID string  `json:"sessionId"`
	Text      *string `json:"text,omitempty"`
	When      *string `json:"when,omitempty"`
	ID        *string `json:"id,omitempty"`
	TokenID   *string `json:"tokenId,omitempty"`
}

// sessionMessageResultWire is the send form's response — MessageView plus
// the sessionId every session.* write result carries (session.keys' own
// sessionKeysResultWire does the same).
type sessionMessageResultWire struct {
	SessionID    string `json:"sessionId"`
	ID           string `json:"id"`
	Namespace    string `json:"namespace"`
	Phase        string `json:"phase"`
	BytesWritten int    `json:"bytesWritten,omitempty"`
	BoxContents  string `json:"boxContents,omitempty"`
}

// sessionMessageCancelResultWire is the cancel form's response (design
// §8.6): phase is absent exactly when result is "no_such_message" — the one
// response §8.3's phase rule does not cover.
type sessionMessageCancelResultWire struct {
	Result string `json:"result"`
	Phase  string `json:"phase,omitempty"`
}

// executeSessionMessage is session.message's whole InGo execution: the
// cancel form when `cancel` is set, the send form otherwise. Both forms
// share one capability field (cap.SessionMessages) and one authority
// (cap.PaneAccess) — mirroring session.keys' cap.SessionKeys/cap.PaneAccess
// pair exactly.
func executeSessionMessage(ctx context.Context, cap *agenttools.SessionDescendantCapability, args json.RawMessage) (string, error) {
	if cap == nil {
		return "", errors.New("session.message: capability carries no session access")
	}
	var probe struct {
		SessionID string  `json:"sessionId"`
		Cancel    *string `json:"cancel"`
	}
	if unmarshalErr := json.Unmarshal(args, &probe); unmarshalErr != nil {
		return "", fmt.Errorf("session.message: args: %w", unmarshalErr)
	}
	if probe.SessionID == "" {
		return "", errors.New("session.message: name the session whose pane to message")
	}
	if cap.PaneAccess == nil || cap.SessionMessages == nil {
		return "", fmt.Errorf("session.message: %q is not this run's own session and no descendant message authority is wired for this run", probe.SessionID)
	}
	messages, ok := cap.SessionMessages.(PaneMessages)
	if !ok {
		return "", fmt.Errorf("session.message: session messages capability is %T, not a PaneMessages", cap.SessionMessages)
	}
	if probe.Cancel != nil {
		return executeSessionMessageCancel(ctx, cap, messages, probe.SessionID, *probe.Cancel)
	}
	return executeSessionMessageSend(ctx, cap, messages, args)
}

func executeSessionMessageCancel(ctx context.Context, cap *agenttools.SessionDescendantCapability, messages PaneMessages, sessionID, id string) (string, error) {
	if id == "" {
		return "", errors.New("session.message: cancel names no id")
	}
	result, err := messages.Cancel(ctx, cap.PaneAccess, sessionID, id)
	if err != nil {
		return "", fmt.Errorf("session.message: %w", err)
	}
	out := sessionMessageCancelResultWire{Result: result.Result}
	if result.Result != "no_such_message" {
		out.Phase = string(result.Phase)
	}
	b, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		return "", fmt.Errorf("session.message: marshal cancel result: %w", marshalErr)
	}
	return string(b), nil
}

func executeSessionMessageSend(ctx context.Context, cap *agenttools.SessionDescendantCapability, messages PaneMessages, args json.RawMessage) (string, error) {
	var p sessionMessageSendParams
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("session.message: args: %w", unmarshalErr)
	}
	if p.Text == nil || *p.Text == "" {
		return "", errors.New("session.message: text is required")
	}
	if p.When == nil || (*p.When != "free" && *p.When != "now") {
		return "", errors.New(`session.message: when must be "free" or "now"`)
	}
	if p.ID == nil || *p.ID == "" {
		return "", errors.New("session.message: id is required, for at-most-once delivery")
	}
	tokenID := ""
	if p.TokenID != nil {
		tokenID = *p.TokenID
	}
	if *p.When == "now" && tokenID == "" {
		return "", errors.New(`session.message: when "now" requires tokenId, from a prior session.read target of kind input or working`)
	}
	view, err := messages.Send(ctx, cap.PaneAccess, p.SessionID, *p.Text, *p.When, *p.ID, tokenID)
	if err != nil {
		return "", fmt.Errorf("session.message: %w", err)
	}
	out := sessionMessageResultWire{
		SessionID: p.SessionID, ID: view.ID, Namespace: view.Namespace,
		Phase: string(view.Phase), BytesWritten: view.BytesWritten, BoxContents: view.BoxContents,
	}
	b, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		return "", fmt.Errorf("session.message: marshal result: %w", marshalErr)
	}
	return string(b), nil
}
