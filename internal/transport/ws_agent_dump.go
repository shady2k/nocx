package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/capability"
)

// agentDumpResponse is the recorded provider exchange for one finished turn.
// Each direction is a list because an approval resume is a second provider
// drive. The list preserves drives instead of silently concatenating them.
type agentDumpResponse struct {
	Request  []agentDumpDrive `json:"request"`
	Response []agentDumpDrive `json:"response"`
}

type agentDumpDrive struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type agentDumpParams struct {
	EntryID string `json:"entryId"`
}

func validateAgentDumpRaw(raw json.RawMessage) string {
	var p agentDumpParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if strings.TrimSpace(p.EntryID) == "" || len(p.EntryID) > maxIDRunes {
		return "entryId is required and bounded"
	}
	return ""
}

type agentDumpHandlers struct {
	op capability.LedgerOperation
	r  Responder
}

func (h agentDumpHandlers) handle(ctx context.Context, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "method not found: content store not wired"})
		return
	}
	var p agentDumpParams
	if msg := decodeParams(req.Params, &p); msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
		return
	}
	var artifacts []agentDumpArtifact
	missing := false
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.LedgerService) error {
		entry, err := svc.Entry(ctx, p.EntryID)
		if err != nil {
			return err
		}
		if entry == nil {
			missing = true
			return nil
		}
		for _, art := range entry.Artifacts {
			item, err := readAgentDumpArtifact(ctx, svc, art.ID)
			if err != nil {
				return err
			}
			if item != nil {
				artifacts = append(artifacts, *item)
			}
		}
		for _, execution := range entry.Executions {
			for _, art := range execution.Artifacts {
				item, err := readAgentDumpArtifact(ctx, svc, art.ID)
				if err != nil {
					return err
				}
				if item != nil {
					artifacts = append(artifacts, *item)
				}
			}
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
		return
	}
	if missing {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: fmt.Sprintf("no ledger entry carries id %q", p.EntryID)})
		return
	}
	out := agentDumpResponse{
		Request:  make([]agentDumpDrive, 0),
		Response: make([]agentDumpDrive, 0),
	}
	sort.SliceStable(artifacts, func(i, j int) bool {
		if artifacts[i].hasOrdinal != artifacts[j].hasOrdinal {
			return artifacts[i].hasOrdinal
		}
		return artifacts[i].ordinal < artifacts[j].ordinal
	})
	for _, item := range artifacts {
		if item.kind == "request" {
			out.Request = append(out.Request, item.drive)
		} else {
			out.Response = append(out.Response, item.drive)
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(out))
}

type agentDumpArtifact struct {
	kind       string
	ordinal    uint64
	hasOrdinal bool
	drive      agentDumpDrive
}

func readAgentDumpArtifact(ctx context.Context, svc capability.LedgerService, id string) (*agentDumpArtifact, error) {
	art, err := svc.Artifact(ctx, id)
	if err != nil {
		return nil, err
	}
	if art == nil {
		return nil, nil
	}
	var b strings.Builder
	for _, chunk := range art.Chunks {
		_, _ = b.Write(chunk)
	}
	var marker struct {
		Wire    string  `json:"wire"`
		Ordinal *uint64 `json:"ordinal"`
	}
	if err := json.Unmarshal([]byte(art.Payload), &marker); err != nil || (marker.Wire != "request" && marker.Wire != "response") {
		return nil, nil
	}
	return &agentDumpArtifact{
		kind:       marker.Wire,
		ordinal:    derefUint64(marker.Ordinal),
		hasOrdinal: marker.Ordinal != nil,
		drive:      agentDumpDrive{Text: b.String(), Truncated: art.Truncated != nil},
	}, nil
}

func derefUint64(v *uint64) uint64 {
	if v == nil {
		return 0
	}
	return *v
}
