package assistant

// The classifier (bead nocx-kpy23): a second, cheaper model judges a
// proposed tool call and says whether it is riskier than it looks — the
// classification-confidence dimension of ADR-0020 decision 6 delegated to
// a model. It resolves through the classifier ROLE (bead nocx-e6kn2),
// never a model id: the role's assigned (endpoint, model) pair changes in
// ONE place, the roles surface, and every consumer picks it up.
//
// Two invariants, and the failure rule that follows from them, are the
// bead:
//
//  1. The classifier may only RAISE suspicion, never lower it. The two
//     verdicts compose as the maximum over permit < ask < refuse: a
//     permitted call the classifier suspects becomes ask, and nothing a
//     classifier says can move ask to permit or refuse to ask. ADR-0029
//     rule 7 settled this shape for keystrokes — the model's judgement is
//     untrusted input, and the local mechanical gate keeps that path
//     advisory. One-directional is that decision applied to another input.
//     The parser enforces it: the ONLY verdict that does not escalate is
//     an exact "clear"; anything else is a failure, so a model that prints
//     "this is safe" can never write into our control flow.
//
//  2. The classifier is an egress point. It sees the call arguments, and
//     arguments carry secrets. The gate that screens the answering model's
//     input (egress.go, bead nocx-0p7y2) covers the classifier's input
//     exactly as it covers the answering model's — the middleware screens
//     the arguments with the same recognizer and the same vault comparison
//     BEFORE the classifier is asked, so the material does not reach the
//     classifier either.
//
// And the failure rule: unreachable, timed out, unparseable, or a role
// with no model assigned — every one escalates. Never permits, and never
// silently skips itself: a gate that disappears when the network is bad is
// not a gate. A consequence of invariant 1: the classifier is consulted
// only where the policy says permit — on a call the policy already
// escalates or refuses, its verdict cannot change the outcome, so asking
// it would spend a second model call and keep its latency on a path where
// a person is already waiting.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/masking"
)

// ClassifierVerdict is the classifier's judgement of ONE proposed call.
// The vocabulary is closed: the middleware only ever decides "escalate"
// for anything that is not an exact ClassifierClear.
type ClassifierVerdict string

const (
	// ClassifierClear means the classifier raises no suspicion: the call
	// runs as the policy decided. The ONLY verdict that lowers nothing.
	ClassifierClear ClassifierVerdict = "clear"
	// ClassifierSuspect means the classifier says the call is riskier than
	// it looks: a permitted call escalates to ask.
	ClassifierSuspect ClassifierVerdict = "suspect"
)

func (v ClassifierVerdict) valid() bool {
	return v == ClassifierClear || v == ClassifierSuspect
}

// ClassifyInput is what the classifier sees: the tool name and the exact
// arguments the model proposed, byte for byte — the thing its suspicion is
// about. Deliberately not a parsed object: the classifier is shown the
// proposal as it would leave.
type ClassifyInput struct {
	Tool      string
	CallID    string
	Arguments string
}

// Classification is the classifier's answer to one proposal. Verdict is
// the closed vocabulary; Model is the classifier ROLE's model that produced
// the verdict — the trusted resolved fact, never a self-report, so the
// ledger can name which model said it (the e6kn2 rule applied to the
// classifier). Reason is the classifier's one-sentence justification,
// bounded and masked before it is recorded (it is model output, and model
// output can echo the arguments).
type Classification struct {
	Verdict ClassifierVerdict
	Model   string
	Reason  string
}

// CallClassifier is the middleware's seam to the classifier. nil means the
// classifier is not wired for this run: permitted calls run exactly as
// they do without one, and the attempt records nothing (criterion 7's
// "behaves as it does today"). A non-nil seam sees every permitted call,
// and every error it returns is an escalation — an implementation MUST
// fail rather than degrade, because a gate that disappears when the
// network is bad is not a gate.
type CallClassifier interface {
	// Classify asks the classifier role's model whether the proposed call
	// is riskier than it looks. Any error escalates the call.
	Classify(ctx context.Context, in ClassifyInput) (Classification, error)
}

// ClassifierTarget is the resolved facts of one classification call: the
// classifier role's assigned (endpoint, model) pair with its credential —
// the same shape the answering role's pair resolves to, nothing less.
type ClassifierTarget struct {
	Key     credential.Secret
	BaseURL string
	Model   string
	Headers []Header
}

// ClassifierResolver resolves the classifier role per classification call.
// The transport adapts THE ONE role resolver (profile.ResolveRole through
// its config service, bead e6kn2) and the vault's credential resolution to
// this seam. A role refusal — unassigned, endpoint gone, model gone — is
// RETURNED as-is: the middleware escalates, so a classifier that cannot
// resolve is a classifier that asks.
type ClassifierResolver interface {
	ResolveClassifier(ctx context.Context) (ClassifierTarget, error)
}

// classifierCallTimeout bounds one classification model call. A classifier
// that does not answer in time escalates; the bound is the same shape as
// the answering call's own deadlines — a hang must be a failure, not a
// vanished gate.
const classifierCallTimeout = 30 * time.Second

// classifierReasonMaxBounds the classifier's justification before it is
// masked and recorded: model output about the arguments can echo them, and
// the bound is a consumer's defense against a hostile echo.
const classifierReasonMax = 512

// classifierEngine is the concrete classifier: ONE model call with a
// fixed, machine-parseable prompt, over the same guarded HTTP client the
// answering role uses, per one fresh resolution of the classifier role.
// The engine owns model calls (design §6 — usage has an owner); the
// resolution is the transport's; the middleware sequences and enforces.
type classifierEngine struct {
	logger   log.Logger
	http     *http.Client
	resolver ClassifierResolver
	timeout  time.Duration
}

// newClassifierEngine builds the concrete classifier over the guarded
// client.
func newClassifierEngine(logger log.Logger, httpClient *http.Client, resolver ClassifierResolver) *classifierEngine {
	return &classifierEngine{
		logger:   logger,
		http:     httpClient,
		resolver: resolver,
		timeout:  classifierCallTimeout,
	}
}

// Classify implements CallClassifier: resolve the role, build the model,
// ask it, and parse the answer — strictly. The resolution and the
// transport failures return as-is: the middleware escalates them all.
func (c *classifierEngine) Classify(ctx context.Context, in ClassifyInput) (Classification, error) {
	target, err := c.resolver.ResolveClassifier(ctx)
	if err != nil {
		return Classification{}, err
	}
	cm, err := buildModel(c.http, target.Key, target.BaseURL, target.Model)
	if err != nil {
		return Classification{}, err
	}
	// The classification is a single bounded completion, unlike the
	// answering stream: its output is machine-readable, nobody waits on
	// deltas, and the run must not hand its lifespan to a classifier that
	// never finishes.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	msgs := []*schema.Message{
		schema.SystemMessage(classifierSystemPrompt),
		schema.UserMessage(fmt.Sprintf("tool: %s\ncallId: %s\narguments:\n%s", in.Tool, in.CallID, in.Arguments)),
	}
	var opts []einoModel.Option
	if m, names := headerMap(target.Headers); m != nil {
		opts = append(opts, openai.WithExtraHeader(m))
		// The endpoint's custom headers follow the guarded client's
		// redirect rule like every other credential-bearing header
		// (httpguard.go): their names must ride the request context so a
		// redirect to another origin drops exactly them.
		ctx = withCustomHeaderNames(ctx, names)
	}
	resp, err := cm.Generate(ctx, msgs, opts...)
	if err != nil {
		return Classification{}, err
	}
	if resp == nil {
		return Classification{}, errors.New("classifier: the model returned no answer")
	}
	out, err := parseClassification(resp.Content)
	if err != nil {
		return Classification{}, err
	}
	out.Model = target.Model // trusted fact of the resolution, never a self-report
	return out, nil
}

// classifierSystemPrompt is the ONE prompt the classifier ever sees: the
// closed verdict vocabulary, the honesty rule of design §7.2 (an opaque
// effect is a suspect effect — "the default for anything unreadable is to
// ask"), and the exact machine-readable reply contract. The prompt is part
// of the contract: the parser accepts exactly the shape this prompt
// demands and nothing else.
const classifierSystemPrompt = `You are a risk classifier for a terminal agent's proposed tool calls. A tool call is proposed; you are told its name and its exact JSON arguments.

Say whether the call is RISKIER THAN IT LOOKS. The default for anything you cannot read is SUSPECT: an escalation is cheap, and a false "clear" turns an unknown into a permission.

Your verdict for this ONE call:
- "clear" only when the call does exactly what it appears to do, you can read every argument, and nothing about it is indirect: no command execution inside an argument, no expansion, no delegation, no resource outside the one named.
- "suspect" when the apparent effect is not the real one (an opaque string, a shell expansion, a pipe into a shell, an alias, an indirect invocation, a destructive flag hidden in a long string, input that is assumed, something that spans a boundary) — or when you cannot tell.

Reply with exactly one JSON object, no prose:
{"verdict": "clear" or "suspect", "reason": "one sentence saying why"}`

// parseClassification is the mechanical floor of invariant 1. It accepts
// EXACTLY the closed vocabulary — {"verdict":"clear"} or
// {"verdict":"suspect"} — and treats everything else, including an unknown
// word, as a failure. {"verdict":"safe"} is what a program that prints
// "this is safe" would answer; it must be a failure, never a permission.
func parseClassification(body string) (Classification, error) {
	var doc struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return Classification{}, fmt.Errorf("classifier: answer is not JSON: %w", err)
	}
	v := ClassifierVerdict(doc.Verdict)
	if !v.valid() {
		return Classification{}, fmt.Errorf("classifier: unrecognised verdict %q — only exact \"clear\" or \"suspect\" are accepted", doc.Verdict)
	}
	return Classification{Verdict: v, Reason: doc.Reason}, nil
}

// maskClassifierReason is the durable-policy pass over the classifier's
// justification before it is recorded on the ledger: the reason is model
// output, model output can echo the arguments, and the arguments may carry
// secrets. One recognizer, the durable policy — mask and continue, exactly
// as the durable history path masks (ADR-0021). A recognizer that cannot
// see (the fail-closed belt of masking.MaskWithSegments) WITHHOLDS the
// reason — the ledger gets the empty string, never an unvetted echo.
func maskClassifierReason(reason string) string {
	if reason == "" {
		return ""
	}
	masked, _, _, err := masking.MaskWithSegments(reason)
	if err != nil {
		return "" // cannot see: the reason does not get recorded
	}
	if len(masked) > classifierReasonMax {
		masked = masked[:classifierReasonMax]
	}
	return masked
}

// classifierFact is the classifier block recorded on an action entry — the
// ledger's answer to "why was this asked" (criterion 6), carried either on
// the proposal an escalation records or on the attempt of a cleared call.
// It carries facts, never material: Verdict and Model name who said what,
// Reason is the bounded, masked justification, and Findings are the egress
// gate's fact-shaped withholdings.
type classifierFact struct {
	// Consulted reports whether the classifier's model answered at all —
	// false when the failure happened before any model call (role
	// resolution refused) or the input gate withheld the call.
	Consulted bool `json:"consulted"`
	// Verdict is the classifier's closed-vocabulary verdict. Omitted when
	// no verdict exists (a failure or a withheld input).
	Verdict ClassifierVerdict `json:"verdict,omitempty"`
	// Model is the classifier role's model that produced the verdict.
	Model string `json:"model,omitempty"`
	// Reason is the sentence that says why: the classifier's own
	// justification (bounded and masked), or the failure sentence.
	Reason string `json:"reason,omitempty"`
	// Findings are the egress gate's findings when the classifier was NOT
	// consulted because its input carried secret-shaped material.
	Findings []EgressFinding `json:"findings,omitempty"`
}

// proposalApproved reports whether this exact proposal already carries a
// human's approval. The approval covers the proposal INCLUDING its
// classification — a person approved the call as proposed — so the resumed
// pass must not consult the classifier again: a second suspect verdict
// would ask about a call the person just answered, forever.
func (m *policyMiddleware) proposalApproved(toolName, callID, rawArgs string) bool {
	if m.approvals == nil {
		return false
	}
	return m.approvals.IsApproved(m.proposal(toolName, callID, rawArgs))
}

// classifyProposal runs the classifier gate over one permitted call. The
// order is the invariant's own:
//
//  1. The egress gate on the INPUT (invariant 2) — the classifier is an
//     egress point, so its arguments pass the SAME gate that screens the
//     answering model's input: the same recognizer, the same vault
//     comparison. A finding means the classifier CANNOT be shown the
//     arguments — the call escalates with the findings recorded, and the
//     material never leaves.
//  2. The consultation. Any error — unreachable, timed out, unparseable,
//     unresolved role — escalates with the failure sentence recorded. A
//     verdict other than ClassifierClear escalates with the verdict, the
//     model and the masked reason. ClassifierClear alone lets the call
//     run.
//
// The returned ask is the suspension's approval request; the returned fact
// is what the ledger records. A nil ask means the verdict was clear.
func (m *policyMiddleware) classifyProposal(ctx context.Context, decl agenttools.Tool, callID, rawArgs string, args map[string]any) (*ApprovalRequest, *classifierFact, error) {
	findings, err := m.screenResult(ctx, rawArgs, nil)
	if err != nil {
		return nil, nil, err
	}
	if len(findings) > 0 {
		fact := &classifierFact{
			Findings: findings,
			Reason:   "the classifier could not be consulted: " + findingsSentence(findings),
		}
		return m.request(decl, callID, rawArgs, args), fact, nil
	}
	classification, err := m.classifier.Classify(ctx, ClassifyInput{Tool: decl.Name, CallID: callID, Arguments: rawArgs})
	if err != nil {
		fact := &classifierFact{
			Reason: maskClassifierReason("the classifier could not be consulted: " + summarizeClassifierError(err)),
		}
		return m.request(decl, callID, rawArgs, args), fact, nil
	}
	if classification.Verdict != ClassifierClear {
		fact := &classifierFact{
			Consulted: true,
			Verdict:   classification.Verdict,
			Model:     classification.Model,
			Reason:    maskClassifierReason(classification.Reason),
		}
		return m.request(decl, callID, rawArgs, args), fact, nil
	}
	return nil, &classifierFact{
		Consulted: true,
		Verdict:   ClassifierClear,
		Model:     classification.Model,
	}, nil
}

// findingsSentence is the fact-shaped, material-free sentence that names
// what the input gate found: which detector fired and the name of the
// known value or the recognizer kind — never the value itself.
func findingsSentence(findings []EgressFinding) string {
	names := make([]string, 0, len(findings))
	for _, f := range findings {
		switch f.Source {
		case EgressFindingKnown:
			if f.SecretName != "" {
				names = append(names, "known value "+f.SecretName)
			} else {
				names = append(names, "a known vault value")
			}
		default:
			if f.Kind != "" {
				names = append(names, "a secret-shaped value ("+string(f.Kind)+")")
			} else {
				names = append(names, "a secret-shaped value")
			}
		}
	}
	return strings.Join(names, ", ")
}

// summarizeClassifierError bounds the classifier failure sentence for the
// ledger: the failure is a sentence a person reads, never a Go error dump.
const classifierErrorLimit = 300

func summarizeClassifierError(err error) string {
	s := err.Error()
	if len(s) > classifierErrorLimit {
		s = s[:classifierErrorLimit] + "…"
	}
	return s
}
