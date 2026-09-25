package transport

import (
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/session"
)

// historyRecordedParams replaces history.record's acknowledgement. It is a
// server-owned notification emitted after the lifecycle writer has closed the
// attempt's history entry. AttemptID is the routing key when history policy
// writes no row; the remaining fields are the former acknowledgement, carried
// unchanged for the block's masked-command and capture-offer presentation.
type historyRecordedParams struct {
	SessionID     string          `json:"sessionId"`
	AttemptID     string          `json:"attemptId"`
	MaskedCount   int             `json:"maskedCount"`
	MaskedKinds   []string        `json:"maskedKinds"`
	EntryID       string          `json:"entryId"`
	Source        string          `json:"source"`
	Redactions    []redactionWire `json:"redactions"`
	MaskedCommand string          `json:"maskedCommand"`
	Captures      []captureWire   `json:"captures"`
}

// historyAttemptScope is captured at the authenticated submit boundary. The
// generation belongs to the submitting connection, not to completion delivery.
type historyAttemptScope struct {
	Source     content.Source
	Pane       string
	Generation uint64
}

// historyRecordedData is the internal result of the durable lifecycle
// transition. Capture registration is delayed until PublishLifecycle has the
// current subscriber and connection identity.
type historyRecordedData struct {
	SessionID   session.ID
	AttemptID   string
	EntryID     string
	PaneID      string
	Generation  uint64
	Source      content.Source
	Command     string
	MaskedCount int
	MaskedKinds []string
	Redactions  []content.Redaction
	Credentials []credential.PendingCredential
}

func historyRecordedParamsFor(data historyRecordedData, connection string, sessionIDs []string, captures *credential.CaptureRegistry) historyRecordedParams {
	params := historyRecordedParams{
		SessionID:     string(data.SessionID),
		AttemptID:     data.AttemptID,
		MaskedCount:   data.MaskedCount,
		MaskedKinds:   append([]string(nil), data.MaskedKinds...),
		EntryID:       data.EntryID,
		Source:        string(data.Source),
		Redactions:    redactionsToWire(data.Command, data.Redactions),
		MaskedCommand: data.Command,
		Captures:      []captureWire{},
	}
	if params.MaskedKinds == nil {
		params.MaskedKinds = []string{}
	}
	if params.Redactions == nil {
		params.Redactions = []redactionWire{}
	}
	if captures == nil || len(data.Credentials) == 0 {
		return params
	}
	results := captures.Submit(credential.CaptureScope{
		Connection: connection,
		Pane:       data.PaneID,
		SessionIDs: sessionIDs,
		EntryID:    data.EntryID,
		Generation: data.Generation,
	}, data.Credentials)
	for i, result := range results {
		if result.Outcome != credential.OutcomeCaptured && result.Outcome != credential.OutcomeLinked {
			continue
		}
		params.Captures = append(params.Captures, captureWire{
			ID: string(result.CaptureID), EntryID: data.EntryID,
			Redaction: params.Redactions[i], SuggestedName: result.SuggestedName,
		})
	}
	return params
}

// historyRecordedNotification emits through the current session subscriber.
func (s *WSServer) historyRecordedNotification(wconn *wsConn, state *connState, data historyRecordedData) {
	if wconn == nil || data.SessionID == "" {
		return
	}
	sessionIDs := []string{string(data.SessionID)}
	if state != nil {
		sessionIDs = sessionIDsOf(state)
	}
	params := historyRecordedParamsFor(data, connectionID(wconn), sessionIDs, s.captures)
	if err := wconn.TryNotify("history.recorded", mustMarshal(params)); err != nil {
		s.log.Debug("write history.recorded", "attempt", data.AttemptID, "error", err)
	}
}
