package transport

// notify.feed.read / notify.feed.markRead — the renderer's window onto the
// notification centre (nocx-p0xhg.5). The DTOs mirror
// contracts/notify.feed.read.schema.json field for field, and `trust` is
// absent from BOTH: it is a routing capability bound (ADR-0029 §3), not
// something a surface renders, and carrying it would invite a renderer to act
// on a decision the router already made.

import (
	"context"
	"time"

	"github.com/shady2k/nocx/internal/notify"
)

// NotifyFeed is the narrow seam onto the centre's feed. *notify.Feed
// satisfies it without an adapter — the same signature-identical shape
// NotifyRaiser uses.
type NotifyFeed interface {
	Snapshot() notify.FeedSnapshot
	MarkAllRead() uint64
}

// WithNotifyFeed enables notify.feed.read and notify.feed.markRead. When
// absent both answer -32601, exactly as notify.raise does without a raiser.
func WithNotifyFeed(f NotifyFeed) WSServerOption {
	return func(s *WSServer) { s.notifyFeed = f }
}

// feedRunMemberDTO is one constituent of a collapsed row. Four fields, and
// the absence of the others is the design: no trust, no level and no body,
// because the ROW owns severity and detail and a member that could disagree
// with its row would be a second answer to one question (AD-8).
type feedRunMemberDTO struct {
	ID    string `json:"id"`
	At    string `json:"at"`
	Title string `json:"title"`
	Read  bool   `json:"read"`
}

type feedOccurrenceDTO struct {
	ID         string             `json:"id"`
	At         string             `json:"at"`
	Title      string             `json:"title"`
	Body       string             `json:"body"`
	Kind       string             `json:"kind"`
	Level      string             `json:"level"`
	Count      int                `json:"count"`
	Read       bool               `json:"read"`
	BackendID  string             `json:"backendId"`
	SessionID  string             `json:"sessionId"`
	Host       string             `json:"host"`
	Run        []feedRunMemberDTO `json:"run"`
	RunDropped int                `json:"runDropped"`
}

type feedDroppedDTO struct {
	Count  int    `json:"count"`
	Oldest string `json:"oldest"`
	Newest string `json:"newest"`
}

type feedReadResult struct {
	Revision    uint64              `json:"revision"`
	UnreadCount int                 `json:"unreadCount"`
	Occurrences []feedOccurrenceDTO `json:"occurrences"`
	Dropped     feedDroppedDTO      `json:"dropped"`
}

type feedMarkReadResult struct {
	Revision uint64 `json:"revision"`
}

// feedChangedParams is the notification payload: the revision and nothing
// else. That is what makes it droppable without loss.
type feedChangedParams struct {
	Revision uint64 `json:"revision"`
}

// stampOrEmpty renders an instant the way the schema declares it: RFC 3339,
// or the empty string for the zero time. The dropped record's two instants
// are zero until something is evicted, and "" is what the contract says a
// count of 0 carries.
func stampOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func snapshotToResult(snap notify.FeedSnapshot) feedReadResult {
	// Built with make, never left nil: the schema says type array, and a nil
	// slice marshals to null. That exact defect is what the contracts' first
	// run caught on vault.status.
	occ := make([]feedOccurrenceDTO, 0, len(snap.Occurrences))
	for _, o := range snap.Occurrences {
		occ = append(occ, feedOccurrenceDTO{
			ID:         string(o.ID),
			At:         stampOrEmpty(o.Event.At),
			Title:      o.Event.Title,
			Body:       o.Event.Body,
			Kind:       string(o.Event.Kind),
			Level:      string(o.Event.Level),
			Count:      o.Count,
			Read:       o.ReadAt != nil,
			BackendID:  o.Event.Attribution.Backend,
			SessionID:  o.Event.SessionID,
			Host:       o.Event.Attribution.Host,
			Run:        runToDTO(o.Run),
			RunDropped: o.RunDropped,
		})
	}
	return feedReadResult{
		Revision:    snap.Revision,
		UnreadCount: snap.UnreadCount,
		Occurrences: occ,
		Dropped: feedDroppedDTO{
			Count:  snap.Dropped.Count,
			Oldest: stampOrEmpty(snap.Dropped.Oldest),
			Newest: stampOrEmpty(snap.Dropped.Newest),
		},
	}
}

// runToDTO reverses the feed's tail onto the wire: the feed holds members
// oldest first and the schema declares them NEWEST first, the same direction
// as occurrences, so the renderer draws an expansion in the order it
// receives it rather than owning a second opinion about ordering.
//
// make, never nil, for the same reason occurrences is: the schema says
// `type: array` and a nil slice marshals to null.
func runToDTO(run []notify.RunMember) []feedRunMemberDTO {
	out := make([]feedRunMemberDTO, 0, len(run))
	for i := len(run) - 1; i >= 0; i-- {
		out = append(out, feedRunMemberDTO{
			ID:    string(run[i].ID),
			At:    stampOrEmpty(run[i].At),
			Title: run[i].Title,
			Read:  run[i].ReadAt != nil,
		})
	}
	return out
}

// notifyFeedHandlers answers both feed methods. A constructed type holding
// its capability and its Responder, never the *WSServer — the shape every
// handler in this package has.
type notifyFeedHandlers struct {
	feed NotifyFeed
	r    Responder
}

func (h notifyFeedHandlers) handleFeedRead(_ context.Context, req jsonrpcRequest) {
	_ = h.r.TryResult(req.ID, mustMarshal(snapshotToResult(h.feed.Snapshot())))
}

func (h notifyFeedHandlers) handleFeedMarkRead(_ context.Context, req jsonrpcRequest) {
	_ = h.r.TryResult(req.ID, mustMarshal(feedMarkReadResult{Revision: h.feed.MarkAllRead()}))
}

// notifyFeedSpecs declares the two feed methods. They ride the plain lane
// like notify.raise: both are a mutex-guarded read or write of in-memory
// state, so neither can conflict with a domain and neither may sit behind
// one — the moment a person opens the bell is often the moment something
// else has gone wrong.
//
// Availability is declared with whenAvailable rather than checked inside the
// handler: "the domain is not wired" has one owner in this package
// (registration.go's validated), and it answers before the validator, which
// is the right order — a method that does not exist for this build has
// nothing to say about the shape of params sent to it.
func (s *WSServer) notifyFeedSpecs() []methodSpec {
	wired := func() bool { return s.notifyFeed != nil }
	return []methodSpec{
		whenAvailable(regResponder(s.lane, "notify.feed.read", noParams(), func(r Responder) handlerFunc {
			return notifyFeedHandlers{feed: s.notifyFeed, r: r}.handleFeedRead
		}), wired, "notification feed not available"),
		whenAvailable(regResponder(s.lane, "notify.feed.markRead", noParams(), func(r Responder) handlerFunc {
			return notifyFeedHandlers{feed: s.notifyFeed, r: r}.handleFeedMarkRead
		}), wired, "notification feed not available"),
	}
}

// BroadcastFeedChanged tells every attached renderer the feed moved. It
// carries the revision only, so it is safe on the refreshable outbound
// queue: a dropped one costs the renderer one refetch, never a row it never
// learns about. That is precisely why this is NOT the terminal class
// nocx-sb3f describes — this notification has a successor, namely the
// snapshot.
//
// TryNotify, not a blocking write: a client whose queue is full must not
// block the feed's mutation path. Same best-effort broadcast shape as
// broadcastSettingsChanged and broadcastHistoryStatusChanged.
func (s *WSServer) BroadcastFeedChanged(revision uint64) {
	s.connsMu.Lock()
	// Copy under lock so enqueues happen outside the critical section.
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()

	params := mustMarshal(feedChangedParams{Revision: revision})
	for _, wc := range conns {
		_ = wc.TryNotify("notify.feed.changed", params)
	}
}
