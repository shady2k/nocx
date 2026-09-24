package transport

// The backend owns command recording now. This file keeps the one masking and
// credential-detection preparation used by the lifecycle writer and the
// history.recorded notification; no client acknowledgement method remains.

import (
	"sort"
	"strconv"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/masking"
	"github.com/shady2k/nocx/internal/secrets"
)

// maxRecordCommandRunes bounds one command-shaped ledger intent. The old
// method used the same bound; keeping the shared product limit avoids a second
// definition for ledger.open.
const maxRecordCommandRunes = 16_384

// redactionWire is one redaction segment on the wire: kind and span in UTF-16
// code units into the command the row carries. Never the credential's value.
type redactionWire struct {
	Kind   string `json:"kind"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
}

// captureWire is the non-secret display metadata for one pending capture.
type captureWire struct {
	ID            string        `json:"id"`
	EntryID       string        `json:"entryId"`
	Redaction     redactionWire `json:"redaction"`
	SuggestedName string        `json:"suggestedName"`
}

// maskedLedgerCommand is the durable command text and its receipt from the
// single masking owner. Every durable writer starts from this result.
type maskedLedgerCommand struct {
	text     string
	findings []secrets.Finding
	segments []secrets.Segment
}

func maskLedgerCommand(command string) (maskedLedgerCommand, error) {
	masked, findings, segments, err := masking.MaskWithSegments(command)
	if err != nil {
		return maskedLedgerCommand{}, err
	}
	return maskedLedgerCommand{text: masked, findings: findings, segments: segments}, nil
}

// preparedHistoryCommand is the backend's complete command receipt. The
// lifecycle submit writer uses rowCommand and redactions for the durable row;
// completion uses credentials to create the offers carried by history.recorded.
type preparedHistoryCommand struct {
	rowCommand  string
	maskedCount int
	maskedKinds []string
	redactions  []content.Redaction
	credentials []credential.PendingCredential
}

func prepareHistoryCommand(command string, captures *credential.CaptureRegistry) (preparedHistoryCommand, error) {
	masked, err := maskLedgerCommand(command)
	if err != nil {
		return preparedHistoryCommand{}, err
	}
	redactions := redactionsOf(masked.findings, masked.segments)
	rowCommand := masked.text
	savedAt := make(map[int]string, len(masked.findings))
	if captures != nil {
		for i, finding := range masked.findings {
			fingerprint := captures.Fingerprint([]byte(command[finding.ValueStart:finding.ValueEnd]))
			if name, ok := captures.SavedName(fingerprint); ok {
				savedAt[i] = name
			}
		}
	}
	for i := len(redactions) - 1; i >= 0; i-- {
		if name, ok := savedAt[i]; ok {
			r := redactions[i]
			rowCommand = rowCommand[:r.Start] + "{{secret:" + name + "}}" + rowCommand[r.End:]
		}
	}

	rowRedactions := make([]content.Redaction, 0, len(redactions))
	credentials := make([]credential.PendingCredential, 0, len(redactions))
	delta := 0
	for i, finding := range masked.findings {
		r := redactions[i]
		if name, ok := savedAt[i]; ok {
			delta += len("{{secret:"+name+"}}") - (r.End - r.Start)
			continue
		}
		adjusted := content.Redaction{
			Kind: r.Kind, Start: r.Start + delta, End: r.End + delta,
			Prefix: r.Prefix, Suffix: r.Suffix,
		}
		rowRedactions = append(rowRedactions, adjusted)
		credentials = append(credentials, credential.PendingCredential{
			Value:         []byte(command[finding.ValueStart:finding.ValueEnd]),
			SuggestedName: secrets.SuggestName(command, finding),
			Redaction:     adjusted,
		})
	}
	return preparedHistoryCommand{
		rowCommand:  rowCommand,
		maskedCount: len(masked.findings),
		maskedKinds: maskedKindsOf(masked.findings),
		redactions:  rowRedactions,
		credentials: credentials,
	}, nil
}

func redactionsOf(findings []secrets.Finding, segs []secrets.Segment) []content.Redaction {
	out := make([]content.Redaction, 0, len(segs))
	for i, seg := range segs {
		out = append(out, content.Redaction{
			Kind: string(findings[i].Kind), Start: seg.Start, End: seg.End,
			Prefix: seg.Prefix, Suffix: seg.Suffix,
		})
	}
	return out
}

func maskedKindsOf(findings []secrets.Finding) []string {
	seen := make(map[secrets.Kind]struct{}, len(findings))
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		if _, ok := seen[finding.Kind]; ok {
			continue
		}
		seen[finding.Kind] = struct{}{}
		out = append(out, string(finding.Kind))
	}
	return out
}

func redactionsToWire(command string, redactions []content.Redaction) []redactionWire {
	out := make([]redactionWire, 0, len(redactions))
	for _, r := range redactions {
		start, end := secrets.ToUTF16Span(command, r.Start, r.End)
		out = append(out, redactionWire{
			Kind: r.Kind, Start: start, End: end, Prefix: r.Prefix, Suffix: r.Suffix,
		})
	}
	return out
}

// connectionID is the backend's per-connection identity used by capture
// lifetime ownership. It never crosses the wire.
func connectionID(wconn *wsConn) string {
	return strconv.FormatUint(wconn.id, 10)
}

// sessionIDsOf snapshots the connection's sessions for a capture scope.
func sessionIDsOf(state *connState) []string {
	state.mu.Lock()
	ids := make([]string, 0, len(state.sessions))
	for id := range state.sessions {
		ids = append(ids, string(id))
	}
	state.mu.Unlock()
	sort.Strings(ids)
	return ids
}
