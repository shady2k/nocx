// agentRecords.detect — which of the agents the assistant can run are on this
// computer, what version each prints, and whether each says it is ready.
//
// Settings' agent section reads this one method (ADR-anntnxdj decision 6). It
// is named in the agentRecords family because it answers about the same
// agents, and it is a read: a person opens the section and gets the cached
// answer; refresh asks the login shell again (nocx-a62cl.12.1).
//
// The handler knows no agent. It asks the detector for the record's rows and
// sends the facts: an agent's name and display name come from
// internal/agentrecord's table, never from a branch here.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/operation/catalog"
)

// AgentDetector is the detection seam: what the assistant can run on this
// machine. Unset, agentRecords.detect answers "not available" rather than an
// empty list — "nothing is installed" and "this window cannot look" are
// different facts.
type AgentDetector interface {
	Detect(ctx context.Context, refresh bool) ([]agentrecord.Detection, error)
}

type agentDetectionRow struct {
	ID          string                `json:"id"`
	DisplayName string                `json:"displayName"`
	Found       bool                  `json:"found"`
	Version     string                `json:"version"`
	Readiness   agentrecord.Readiness `json:"readiness"`
}

type agentRecordsDetectResult struct {
	Agents []agentDetectionRow `json:"agents"`
}

type agentRecordsDetectParams struct {
	// Refresh asks detection to run the login shell again instead of serving
	// the cached pass. Absent is false: a section that opens twice does not
	// pay for a shell twice.
	Refresh bool `json:"refresh"`
}

func validateAgentRecordsDetect(raw json.RawMessage) string {
	var p agentRecordsDetectParams
	return decodeObject(raw, &p)
}

type agentDetectionHandlers struct {
	detector AgentDetector
	r        Responder
}

func (s *WSServer) agentDetectionSpecs() []methodSpec {
	return []methodSpec{
		regResponder(s.work, catalog.OpAgentRecordsDetect, params(validateAgentRecordsDetect), func(r Responder) handlerFunc {
			h := agentDetectionHandlers{detector: s.agentDetection, r: r}
			return h.handleDetect
		}),
	}
}

func (h agentDetectionHandlers) handleDetect(ctx context.Context, req jsonrpcRequest) {
	if h.detector == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "agentRecords.detect not available"})
		return
	}
	var p agentRecordsDetectParams
	if len(bytes.TrimSpace(req.Params)) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "invalid params"})
			return
		}
	}
	detections, err := h.detector.Detect(ctx, p.Refresh)
	if err != nil {
		log.From(ctx).Error("agents could not be detected", "error", err)
		_ = h.r.TryError(req.ID, RPCError{Code: -32000, Message: "agents could not be detected: " + fmt.Sprint(err)})
		return
	}
	rows := make([]agentDetectionRow, 0, len(detections))
	for _, detection := range detections {
		rows = append(rows, agentDetectionRow{
			ID:          detection.ID,
			DisplayName: detection.DisplayName,
			Found:       detection.Found,
			Version:     detection.Version,
			Readiness:   detection.Readiness,
		})
	}
	_ = h.r.TryResult(req.ID, mustMarshal(agentRecordsDetectResult{Agents: rows}))
}
