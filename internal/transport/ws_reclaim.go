package transport

// Reclaiming a live pane (nocx-oevq4, the nocx-server design D5, D7 and D8):
// the list a fresh client reasons from, the words a refused claim is told
// apart by, and the notification the client that loses a session receives.
//
// THE GAP THIS CLOSES. frontend/src/ipc.ts keeps its sessions in a Map in
// RENDERER PROCESS MEMORY and its reconnect pass reattaches only what is in
// that map. A window that has just started therefore reattaches nothing, and
// live PTYs on the backend are orphaned with no way to find them. sessions.live
// is that map, server-owned: the same reattach, given a list instead of a
// memory.
//
// WHO OWNS WHAT, because two owners of one fact is how they start disagreeing
// (AD-8). The PANE is the renderer's durable identity — it mints the id, it
// outlives the shell and the application, and internal/uistate and the layout
// store persist everything about it: its tab, its cwd, its kind, its endpoint,
// its size. The LIVE BINDING is the backend's: which session is the pipe of
// that pane right now, which incarnation of the backend minted it, and where
// its replayable output starts. Neither owns the other's half, and the only
// thing that crosses is the pane id — recorded on the session at open
// (session.Config.PaneID) and read back here. So nothing in this file writes a
// pane fact, and nothing in this file's answer restates one: a cwd or a kind
// beside the binding would be a copy that starts lying the first time a pane
// is moved or renamed, and the renderer already has the true one.

import (
	"context"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// The refusal vocabulary of a claim. A caller must never have to read prose to
// tell these apart — the vault errors bought that rule (ws_vault.go: a bare
// -32603 with a sentence is indistinguishable from a disk error, so the dialog
// that should have opened never did). They travel in the error's `data.reason`,
// the same discriminator and the same field name.
//
// The set is closed and each member is a different fact about the claim:
const (
	// reasonForeignInstance: the claim names a backend instance that is not
	// this one. Every session that instance held died with it, so the claim
	// could never be true here — as opposed to merely not being true now.
	// This is the answer a binding remembered across a coordinator restart
	// gets, and it is why the instance is judged before anything is looked up.
	reasonForeignInstance = "foreign-instance"
	// reasonForeignIncarnation: this instance, this session id, a different
	// epoch. The id resolves and it is not the session the claim was written
	// against — the ambiguity the identity exists to remove (nocx-3oupk).
	reasonForeignIncarnation = "foreign-incarnation"
	// reasonUnknownSession: this instance holds no such session. It has ended,
	// or it never existed here.
	reasonUnknownSession = "unknown-session"
)

// claimRefusalData is the machine-readable half of a refused claim. A struct
// of one field rather than a shared type with the vault's: that one is a vault
// vocabulary (its constructor switches over vault sentinels) and widening it to
// carry another domain's words would make one enum out of two closed sets.
type claimRefusalData struct {
	Reason string `json:"reason"`
}

// refuseClaim builds the refusal. -32602 because a claim that cannot resolve is
// a statement about the params — the caller named something this backend does
// not have — and never a server fault the caller should retry.
func refuseClaim(reason, message string) RPCError {
	return RPCError{Code: -32602, Message: message, Data: claimRefusalData{Reason: reason}}
}

// sessionsLiveResult is the sessions.live payload, declared once
// (contracts/sessions.live.schema.json) and pinned by the DTO and
// over-the-wire contract tests.
type sessionsLiveResult struct {
	// Sessions is never nil on the wire: the renderer maps over it on the
	// first frame it draws, and a nil slice marshals as null (the defect this
	// directory's first run found in vault.status's providers).
	Sessions []liveSessionResult `json:"sessions"`
}

// liveSessionResult is one live session as a fresh client needs to see it: the
// full identity, the pane it is the pipe of, where its replay starts, and
// whether taking it will displace somebody.
type liveSessionResult struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	// PaneID is a pointer because null and "" are different facts: a session
	// that is the pipe of no recorded pane is a legitimate state, and it is
	// not "the pane whose id is empty". No omitempty — the key is required, so
	// "no pane" and "an old backend" can never look the same.
	PaneID     *string `json:"paneId"`
	ReplayFrom uint64  `json:"replayFrom"`
	Attached   bool    `json:"attached"`
}

// attachResult is the claim outcome (contracts/attach.schema.json). Both
// booleans are always present and exactly one is true: a reader that cannot
// tell "the server said no reset" from "the server did not mention reset"
// cannot act on the answer, and the reset is the case where the renderer must
// clear its decoder and its screen before a single byte is drawn (D7).
type attachResult struct {
	Resumed bool   `json:"resumed"`
	Reset   bool   `json:"reset"`
	From    uint64 `json:"from"`
}

// sessionDisplacedParams is the params object of the session.displaced
// notification (contracts/session.displaced.schema.json): the whole identity,
// never a bare id, because a notification naming only an id could be about a
// previous incarnation and the renderer refuses one whose pair is not its own.
type sessionDisplacedParams struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
}

// instanceIdentity is this backend instance's id as the transport sees it: the
// registry's, which is the single owner of that value (session.Reg.InstanceID).
// It is read once at handler construction. The empty answer for a server built
// with no registry is not a degrade to hide — a claim is then judged by the
// session lookup alone, exactly as one carrying no identity is — and no
// production composition root builds such a server.
func (s *WSServer) instanceIdentity() session.InstanceID {
	if s.registry == nil {
		return ""
	}
	return s.registry.InstanceID()
}

// ringOwner is the transport-owned half of an answer about a live session: its
// replay ring and who is currently attached to it. Declared as the narrow seam
// the sessions.live handler needs rather than taken as the whole server — the
// handler may read a session's ring position and may not touch a store.
type ringOwner interface {
	getRx(sid session.ID) *sessionRx
}

// sessionsLiveHandlers answers sessions.live. It holds the whole-domain
// SessionOperation (the registry read, under the session gate), the ring owner
// and its Responder; never the *WSServer.
type sessionsLiveHandlers struct {
	op    capability.SessionOperation
	rings ringOwner
	r     Responder
	log   log.Logger
}

// handleSessionsLive answers with every session this backend is holding, each
// with its pane binding and the offset a claim should start at.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"sessions.live","params":{}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"sessions":[{"sessionId":"…","instanceId":"…","sessionEpoch":1,"paneId":"…","replayFrom":0,"attached":false}]}}
//
// A session whose ring has gone is still LISTED, with replayFrom 0 and
// attached false. That is the state between the registry insert and the ring's
// creation, and again after a teardown that removed the ring first; the list is
// what a fresh client reasons from, and a silently shorter list is the worst of
// the available answers — it says "that pane has no session" about a shell that
// is running.
func (h sessionsLiveHandlers) handleSessionsLive(ctx context.Context, req jsonrpcRequest) {
	err := h.op.Run(ctx, func(_ context.Context, svc capability.SessionService) error {
		live := svc.List()
		result := sessionsLiveResult{Sessions: make([]liveSessionResult, 0, len(live))}
		for _, sess := range live {
			ident := sess.Identity()
			entry := liveSessionResult{
				SessionID:    string(sess.ID()),
				InstanceID:   string(ident.InstanceID),
				SessionEpoch: ident.Epoch,
			}
			if pane := sess.PaneID(); pane != "" {
				entry.PaneID = &pane
			}
			if rx := h.rings.getRx(sess.ID()); rx != nil {
				entry.ReplayFrom = rx.ring.oldestLocked()
				wconn, _ := rx.getSubscriber()
				entry.Attached = wconn != nil
			}
			result.Sessions = append(result.Sessions, entry)
		}
		_ = h.r.TryResult(req.ID, mustMarshal(result))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// judgeClaim decides whether a claim may resolve to sess, and says WHY when it
// may not. It is the single owner of that question on this plane.
//
// THE INSTANCE IS JUDGED FIRST, and without a lookup — the rule
// internal/session/lineage.go already applies to the parent edge, restated here
// because it is the same claim shape being judged: the instance is the only
// component that can be decided on its own, and the one whose failure means the
// claim could never be true here rather than merely is not true now. So a claim
// carried across a coordinator restart is refused as a foreign instance even
// when the session id it names is unknown too, which is the whole point: the
// client learns its binding is stale, not that it guessed a bad id.
//
// mine is this backend's instance identity; claimedInstance and claimedEpoch
// are what the caller sent, and either may be absent (see attachParams). A nil
// sess is "this backend holds no such session".
func judgeClaim(mine session.InstanceID, claimedInstance string, claimedEpoch uint64, sess session.Session) *RPCError {
	if foreignInstance(mine, claimedInstance) {
		e := foreignInstanceRefusal()
		return &e
	}
	if sess == nil {
		e := refuseClaim(reasonUnknownSession, "Invalid params: unknown sessionId")
		return &e
	}
	if claimedEpoch != 0 && claimedEpoch != sess.Identity().Epoch {
		e := refuseClaim(reasonForeignIncarnation,
			"Invalid params: the claim names a different incarnation of this session")
		return &e
	}
	return nil
}

// foreignInstance reports whether a claim names a backend instance that is not
// this one. Its own function because it is the one component of a claim that
// can be judged with nothing looked up, and two callers need exactly that: the
// attach handler asks BEFORE resolving the session id (so a binding from a dead
// coordinator is not reported as a bad id), and judgeClaim asks again as the
// first of the three verdicts. One owner, two moments.
func foreignInstance(mine session.InstanceID, claimed string) bool {
	return claimed != "" && session.InstanceID(claimed) != mine
}

// foreignInstanceRefusal is that verdict as an answer, in one place so both
// callers say it identically.
func foreignInstanceRefusal() RPCError {
	return refuseClaim(reasonForeignInstance,
		"Invalid params: the claim names a different backend instance")
}

// announceDisplacement tells the client that has just lost a session that it
// lost it (D8), and takes the session away from it: the notification, and the
// removal from that connection's own state so its input, resize and close are
// refused from this moment. Both halves, because either alone is a surface
// still advertising what it can no longer deliver — a client told it lost the
// session while its keystrokes are still accepted has been told a story.
//
// Best-effort on the wire and it says so by returning nothing: the displaced
// client may be a socket that has already gone, and the NEW owner's claim must
// not fail because the old owner's connection did. That failure path is the
// ordinary one, not the exception — a fresh window reclaiming a pane is almost
// always displacing a window that is closed.
func (s *WSServer) announceDisplacement(sid session.ID, ident session.Identity, prev *wsConn, prevState *connState) {
	if prevState != nil {
		prevState.remove(sid)
	}
	if prev == nil {
		return
	}
	params := sessionDisplacedParams{
		SessionID:    string(sid),
		InstanceID:   string(ident.InstanceID),
		SessionEpoch: ident.Epoch,
	}
	if err := prev.TryNotify("session.displaced", mustMarshal(params)); err != nil {
		// Said out loud rather than swallowed: "the loser was told" is a claim
		// this feature makes, and the one case where it is not true is a
		// connection that could not be written to.
		s.log.Debug("session.displaced not delivered", "session_id", string(sid), "error", err)
	}
}
