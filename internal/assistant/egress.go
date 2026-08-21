package assistant

// The egress gate (design §7.1, bead nocx-0p7y2): every tool result is
// screened before it leaves for the provider, and a finding REFUSES AND ASKS
// rather than being silently redacted. One recognizer, two policies — the
// recognizer is internal/secrets (via the masking service); the durable path
// masks and continues, this gate suspends and shows what was found.
//
// The finding is this package's contract with the approval surface (the wire
// worker owns the surface that renders it): its shape, how it is produced,
// and its presence on the suspension the Client surfaces. A finding carries
// the facts a person decides on — which detector fired, the kind, where it
// occurred — and NEVER the secret material itself.

import (
	"context"
	"encoding/gob"
	"fmt"

	"github.com/cloudwego/eino/adk"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/secrets"
)

// EgressFindingSource names which detector produced a finding. Known vault
// material and a heuristic match are different confidences, and the surface
// says which one fired (design §7.1).
type EgressFindingSource string

const (
	// EgressFindingHeuristic is a match of the recognizer's patterns
	// (secrets.Detect through the masking service): a shape, not a known
	// value.
	EgressFindingHeuristic EgressFindingSource = "heuristic"
	// EgressFindingKnown is a value the vault holds: an exact comparison,
	// which beats any pattern. The comparison happens in the backend and
	// nothing leaves, so ADR-0011 §2 survives intact.
	EgressFindingKnown EgressFindingSource = "known"
)

// EgressFinding is one secret-shaped region of a tool result that the gate
// found before the bytes left for the provider. It carries the facts a
// person needs — which detector fired, the kind, where it occurred — and
// never the secret material itself: Start/End are byte offsets into the
// result, and the matched text is the thing being withheld.
type EgressFinding struct {
	// Source names the detector that fired: known vault material or a
	// heuristic match.
	Source EgressFindingSource `json:"source"`
	// Kind is the recognizer's closed vocabulary (secrets.Kind) when the
	// finding is heuristic; empty for known material, whose display fact
	// is the vault's own name.
	Kind secrets.Kind `json:"kind,omitempty"`
	// SecretName is the vault catalogue name of the matched secret for a
	// known finding (ADR-0016 — the vault owns the name). Display
	// metadata, never material.
	SecretName string `json:"secretName,omitempty"`
	// Start and End are byte offsets of the match into the tool result,
	// Start inclusive, End exclusive.
	Start int `json:"start"`
	End   int `json:"end"`
}

// EgressRequest is the finding-carrying ask of the egress gate: a tool
// result — or an error string — contained secret-shaped material, and the
// run suspended before the bytes leave for the provider. It is the
// interrupt's info AND its checkpoint state (the inbound ask does the same,
// ADR-0028: checkpoints are process-lifetime state), so a resume
// re-validates the exact proposal. It is exported because the transport
// renders it; the findings are facts, never the material.
type EgressRequest struct {
	// RunID, Attempt, Tool, CallID and Arguments bind the ask to the exact
	// proposal — the same identity the approval store keys (design §7.2).
	RunID     string `json:"runId"`
	Attempt   int    `json:"attempt"`
	Tool      string `json:"tool"`
	CallID    string `json:"callId"`
	Arguments string `json:"arguments"`
	// ArgHash is the canonical-argument hash of the binding (design §7.2):
	// the surface echoes it back on agent.approve so the decision names the
	// exact proposal — the same hash the approval store keys.
	ArgHash string `json:"argHash"`
	// Effect and Resource are the policy gate's own words for the call the
	// findings came out of. The egress surface ignores them — its answers
	// are allow/deny, once — but the agent.approvalRequested notification is
	// ONE shape whichever gate asked, and its schema requires the effect.
	Effect   content.Effect      `json:"effect"`
	Resource *content.GrantScope `json:"resource,omitempty"`
	// Findings are what the surface shows: what was found and where.
	Findings []EgressFinding `json:"findings"`
	// WasError reports whether the findings are in an ERROR string the
	// tool returned rather than in its result — the surface says which,
	// because the two read differently ("the tool failed and its failure
	// mentioned a secret").
	WasError bool `json:"wasError"`
}

func init() {
	gob.Register(EgressRequest{})
	gob.Register(EgressFinding{})
}

// EgressRequestedError is what Ask returns when the run suspended at the
// egress gate: the run is NOT failed — it is awaiting a human's decision
// about what may leave — and Request is what the approval surface renders
// and the resume re-validates.
type EgressRequestedError struct {
	Request *EgressRequest
}

func (e *EgressRequestedError) Error() string {
	if e.Request == nil {
		return "agent run suspended: a tool result contained secret-shaped material"
	}
	return fmt.Sprintf("agent run suspended at the egress gate: %s %s produced %d secret-shaped region(s); nothing was sent",
		e.Request.Tool, e.Request.CallID, len(e.Request.Findings))
}

// KnownMatch is one region of text that matches a secret the vault holds:
// byte offsets into the text plus the secret's catalogue name (ADR-0016).
// The value itself never crosses this seam.
type KnownMatch struct {
	Start      int
	End        int
	SecretName string
}

// KnownMaterial is the vault-comparison seam of the egress gate (design
// §7.1: "the vault knows the real values, and a comparison beats any
// pattern"). It is legitimate precisely because it happens in the backend
// and nothing leaves — ADR-0011 §2 survives intact. The transport adapts
// the vault to this seam when it mints grants; the gate fails closed
// without it, because a run that screens heuristically while the vault sits
// unasked has closed the wide door and left the narrow one open.
type KnownMaterial interface {
	// FindKnown reports the byte spans of text that match a secret the
	// vault holds, and the catalogue name of each matched secret. The
	// values themselves never cross this seam.
	FindKnown(ctx context.Context, text string) ([]KnownMatch, error)
}

// egressRequestFrom finds the egress ask among an interrupt event's
// contexts, mirroring approvalRequestFrom: the asking call carries our
// *EgressRequest as its info; the latched, deferred calls carry a plain
// string. The first egress ask is the one the human decides about.
func egressRequestFrom(info *adk.InterruptInfo) *EgressRequest {
	if info == nil {
		return nil
	}
	for _, ic := range info.InterruptContexts {
		if req, ok := ic.Info.(*EgressRequest); ok {
			return req
		}
	}
	return nil
}
