package assistant

// The egress gate (design §7.1, bead nocx-0p7y2): every tool result is
// screened before it leaves for the provider, and a finding REFUSES AND ASKS
// rather than being silently redacted. One recognizer, two policies — the
// recognizer is internal/secrets (via the masking service); the durable path
// masks and continues, this gate withholds and shows what was found.
//
// The finding is this package's contract with the approval surface (the wire
// worker owns the surface that renders it): its shape, how it is produced,
// and its presence on the question the Client puts. A finding carries the
// facts a person decides on — which detector fired, the kind, where it
// occurred — and NEVER the secret material itself.

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/masking"
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
// result — or an error string — contained secret-shaped material, and nothing
// leaves for the provider until a person answers. It is what the approval
// surface renders, and the proposal it names is what the answer is bound to.
// It is exported because the transport renders it; the findings are facts,
// never the material.
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

// ── the screen ────────────────────────────────────────────────────────────

// egressScreen is the kernel's Screen over what actually leaves (ADR-anntnxdj
// §2, "A screen reads what actually leaves"): the finding — the recognizer's
// patterns and the vault's known values — and the retained result. It reads the
// final outbound bytes, the tool's error included, and answers Pass, Refuse or
// Ask. It never replaces them: a pass and an approved ask release the exact
// bytes the tool produced, and a refusal withholds them.
//
// It owns BOTH halves of the retention, because they are one decision: the
// bytes an Ask withheld are the bytes an approval releases, and never a
// re-run's freshly produced ones — the effect would repeat, and the result
// could differ from the one the person saw.
type egressScreen struct {
	kernel *effectKernel
	// state is the call's own facts: where a gate that could not see is
	// recorded for the kernel to fail the run on. held and findings need no
	// per-call state — the approval is keyed by the proposal — so the kernel's
	// executor builds the screen without one.
	state *kernelCall
}

// Inspect is the Screen point's whole contract: it decides whether the bytes
// leave, never what they say.
func (s egressScreen) Inspect(ctx context.Context, c Call, out Outbound) Verdict {
	findings, err := s.findings(ctx, out.Text)
	if err != nil {
		// Nothing leaves when the gate cannot see. The verdict can only say
		// no; the reason the run must END rather than answer the call is
		// recorded for the kernel, which reports it as EgressScreeningError.
		s.state.egressGateErr = err
		return Refuse{Reason: "the result could not be screened for secret-shaped material, so nothing was sent"}
	}
	if len(findings) == 0 {
		return Pass{}
	}
	// A finding REFUSES AND ASKS (design §7.1): nothing is sent until a person
	// decides, and it never silently masks and continues — off-machine a miss
	// is invisible and permanent. The bytes are not copied anywhere: the call
	// is blocked on the answer with its own result on the stack, so an
	// approved question releases exactly the bytes the person was shown, and
	// the tool does not run a second time (ADR-anntnxdj §2).
	return Ask{
		Reason: fmt.Sprintf("%s produced %d secret-shaped region(s), and nothing has been sent", c.Tool(), len(findings)),
		Egress: &EgressAsk{Findings: findings, WasError: out.WasError},
	}
}

// findings is the egress gate's one screening of a piece of text: the
// recognizer's patterns (masking.Detect) and the vault's known values
// (KnownMaterial), reported as facts about where each match sits. The values
// themselves never cross this seam. The classifier's input gate reads the same
// function, because one recognizer with two readers is one gate.
func (s egressScreen) findings(ctx context.Context, text string) ([]EgressFinding, error) {
	heuristic, err := masking.Detect(text)
	if err != nil {
		return nil, err
	}
	findings := make([]EgressFinding, 0, len(heuristic))
	for _, f := range heuristic {
		findings = append(findings, EgressFinding{
			Source: EgressFindingHeuristic,
			Kind:   f.Kind,
			Start:  f.Start,
			End:    f.End,
		})
	}
	known, err := s.kernel.known.FindKnown(ctx, text)
	if err != nil {
		return nil, err
	}
	for _, match := range known {
		findings = append(findings, EgressFinding{
			Source:     EgressFindingKnown,
			SecretName: match.SecretName,
			Start:      match.Start,
			End:        match.End,
		})
	}
	if len(findings) == 0 {
		return nil, nil
	}
	return findings, nil
}
