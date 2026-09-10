package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/ssh"
)

// RequestConnectionPassword asks any connected renderer for a connection
// password: it sends a connections.passwordRequest notification naming the
// connection and account (nocx-s8jn — every password prompt must say which
// password it is asking for) and blocks until one responds via
// connections.passwordResolved, or the context is done.
//
// It is the same shape as UnlockRequester — backend asks a renderer and
// blocks for the answer, behind an interface wired at the one composition
// root — with a different meaning. The outcome is one of three, each with
// its own error type and message:
//
//   - ErrPasswordNoClientConnected — no renderer is attached;
//   - ErrPasswordPromptCancelled — the user dismissed the prompt;
//   - any other error — the vault sealed / store refused (surfaced by the
//     caller, which is where the vault lives).
//
// The answer's Remember flag is a request, not a fact: the caller decides
// where and whether the password is stored (vault secret + profile
// reference, ADR-0017), and only reports success once the reference
// persisted.
func (s *WSServer) RequestConnectionPassword(ctx context.Context, req ssh.PasswordRequest) (ssh.PasswordAnswer, error) {
	rid, ch, err := s.asks.register()
	if err != nil {
		return ssh.PasswordAnswer{}, err
	}

	if err := s.broadcastAsk("connections.passwordRequest", map[string]any{
		"requestId":  rid,
		"connection": req.Connection,
		"user":       req.User,
		"host":       req.Host,
		"reason":     req.Reason,
	}, ErrPasswordNoClientConnected); err != nil {
		s.asks.drop(rid)
		return ssh.PasswordAnswer{}, err
	}

	select {
	case res := <-ch:
		if res.err != nil {
			return ssh.PasswordAnswer{}, res.err
		}
		var payload passwordAnswerPayload
		if err := json.Unmarshal(res.result, &payload); err != nil {
			return ssh.PasswordAnswer{}, fmt.Errorf("decode password answer: %w", err)
		}
		return ssh.PasswordAnswer{Password: payload.Password, Remember: payload.Remember}, nil
	case <-ctx.Done():
		s.asks.drop(rid)
		return ssh.PasswordAnswer{}, ctx.Err()
	}
}

// ErrPasswordNoClientConnected is returned by RequestConnectionPassword
// when no renderer is attached to receive the notification. One of the
// three distinct outcomes of a connection-password ask (with the sealed
// vault, surfaced by the caller, and the cancelled prompt) — distinct
// message, distinct type, never folded into the unlock ask's error.
var ErrPasswordNoClientConnected = errors.New("no client connected to ask for the connection password")

// ErrPasswordPromptCancelled is returned by RequestConnectionPassword when
// the user dismissed the password prompt. The connection fails with this
// reason rather than with ErrNoAuthMethod — the user was asked and said
// no; the configuration error never fired.
var ErrPasswordPromptCancelled = errors.New("connection password prompt cancelled")

// passwordAnswerPayload is the wire shape of a submitted answer. It is the
// only place the password crosses as a JSON value; it is deliberately a
// transport-local type so the ssh package's answer struct never becomes a
// serialization contract.
type passwordAnswerPayload struct {
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

// handlePasswordResolved handles the connections.passwordResolved RPC from
// the renderer: it looks up the pending ask and signals its channel.
func (h askResolverHandlers) handlePasswordResolved(req jsonrpcRequest) {
	var params struct {
		RequestID string `json:"requestId"`
		Outcome   string `json:"outcome"`
		Password  string `json:"password,omitempty"`
		Remember  bool   `json:"remember,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}

	pa, ok := h.asks.consume(params.RequestID)
	if !ok {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Unknown request id"})
		return
	}

	switch params.Outcome {
	case "submitted":
		payload, err := json.Marshal(passwordAnswerPayload{Password: params.Password, Remember: params.Remember}) // #nosec G117 -- password is ephemeral transport payload and is never persisted or logged
		if err != nil {
			pa.ch <- askResolution{err: fmt.Errorf("encode password answer: %w", err)}
		} else {
			pa.ch <- askResolution{result: payload}
		}
	case "cancelled":
		pa.ch <- askResolution{err: ErrPasswordPromptCancelled}
	default:
		pa.ch <- askResolution{err: fmt.Errorf("password prompt resolved with unknown outcome: %q", params.Outcome)}
	}

	_ = h.r.TryResult(req.ID, json.RawMessage("{}"))
}
