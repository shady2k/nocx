package transport

// pane.connection — what a pane's SSH connection is doing, from the moment the
// pane opens (design §5.6, nocx-oalo1.5.3).
//
// # Why this is keyed by pane and not by session
//
// AD-7 says the backend assigns a session id and the renderer addresses a
// session by it. But an SSH pane's connection is registered BEFORE its shell
// exists (design §5.5), and at that moment there is no session id to address.
// The pane is the renderer's own durable identity and it exists from the first
// frame, so it is what this notification is keyed by; the session id arrives
// here when a shell is adopted, exactly as the open ack used to be the only
// place it could.
//
// # Why the epoch is here, and what refuses what
//
// A connection's state is revised for as long as it exists, and an observation
// can be overtaken by a newer one before it is written. The driver stamps a
// per-pane epoch at observation, this publisher refuses an epoch that is not
// greater than the last one it sent for that pane, and the renderer refuses one
// that is not greater than the last it applied. Three refusals of one thing, at
// the three boundaries the value crosses.
//
// # What this file is not
//
// It is not the mapper. Which pane rides which connection, and what a
// connection event means for a pane, belong to internal/panedriver; this is
// only the last mile: the wire shape, the per-pane ordering guard, and the
// subscriber lookup. The composition root adapts one to the other, which keeps
// this package out of the connection state machine (design §5.1.1).

// PaneConnection is the wire payload, declared once
// (contracts/pane.connection.schema.json) and pinned by the contract tests.
//
// The fields are pointers where the schema makes presence conditional, because
// `0` and `absent` are different statements: a connected connection with no
// plain shell says plainShells 0, while an opening connection must not carry the
// key at all. A value with omitempty cannot express the first.
type PaneConnection struct {
	PaneID          string                  `json:"paneId"`
	ConnectionID    string                  `json:"connectionId"`
	ConnectionEpoch uint64                  `json:"connectionEpoch"`
	InstanceID      string                  `json:"instanceId"`
	SessionID       string                  `json:"sessionId,omitempty"`
	Transport       PaneConnectionTransport `json:"transport"`
	Quality         *PaneConnectionQuality  `json:"quality,omitempty"`
	Attempt         PaneConnectionAttempt   `json:"attempt"`
	ObservedAt      string                  `json:"observedAt"`
}

// PaneConnectionTransport is the state, with exactly the fields that state
// makes true. The schema's oneOf is the authority on which may appear together;
// this struct is the carrier.
type PaneConnectionTransport struct {
	State          string `json:"state"`
	Stage          string `json:"stage,omitempty"`
	Person         string `json:"person,omitempty"`
	Cause          string `json:"cause,omitempty"`
	Attempt        int    `json:"attempt,omitempty"`
	StateMs        int64  `json:"stateMs"`
	LookAnsweredAt string `json:"lookAnsweredAt,omitempty"`
	PlainShells    *int   `json:"plainShells,omitempty"`
}

// PaneConnectionQuality is the measurement and its grade, as session.liveness
// carries them. The whole object is absent when nothing measured one; a grade
// without a measurement would be a claim about a number nobody took.
type PaneConnectionQuality struct {
	Slow        bool  `json:"slow"`
	RoundTripMS int64 `json:"roundTripMs"`
}

// PaneConnectionAttempt is the open order's state. Until the open order lands
// every member is the backend's `none`, and the object is present so a reader
// has one shape to read.
type PaneConnectionAttempt struct {
	Shown     string `json:"shown"`
	Candidate string `json:"candidate"`
	Reason    string `json:"reason"`
}

// bindPane records that this connection is the one a pane's notifications go
// to. It is called before the open's own work starts: the first observation can
// arrive while the dial is still running, and a subscription installed after
// the ack would miss exactly the stage the pane exists for.
func (s *WSServer) bindPane(paneID string, wconn *wsConn) {
	if paneID == "" || wconn == nil {
		return
	}
	s.paneMu.Lock()
	if s.paneSubs == nil {
		s.paneSubs = make(map[string]*wsConn)
	}
	s.paneSubs[paneID] = wconn
	s.paneMu.Unlock()
}

// unbindPanesFor drops every pane this connection opened. A pane's state lives
// on its connection, and a pane whose window is gone has nobody to draw it.
func (s *WSServer) unbindPanesFor(wconn *wsConn) {
	if wconn == nil {
		return
	}
	s.paneMu.Lock()
	defer s.paneMu.Unlock()
	for paneID, sub := range s.paneSubs {
		if sub == wconn {
			delete(s.paneSubs, paneID)
		}
	}
}

// PublishPaneConnection sends one observation to the renderer that opened the
// pane, refusing an epoch that is not newer than the last one sent for it.
//
// A pane nobody is attached to is not an error: a pane can be opened by a
// backend caller, or its window can be gone, and the state is still true and
// still recorded by the driver for whoever asks next. The epoch advances either
// way, so a later observation is ordered against the one nobody saw.
func (s *WSServer) PublishPaneConnection(p PaneConnection) {
	if p.PaneID == "" || p.ConnectionEpoch == 0 {
		return
	}
	s.paneMu.Lock()
	if p.ConnectionEpoch <= s.paneEpochs[p.PaneID] {
		s.paneMu.Unlock()
		return
	}
	if s.paneEpochs == nil {
		s.paneEpochs = make(map[string]uint64)
	}
	s.paneEpochs[p.PaneID] = p.ConnectionEpoch
	wconn := s.paneSubs[p.PaneID]
	s.paneMu.Unlock()
	if wconn == nil {
		return
	}
	if err := wconn.TryNotify("pane.connection", mustMarshal(p)); err != nil {
		s.log.Debug("write pane.connection notification", "pane", p.PaneID, "error", err)
	}
}
