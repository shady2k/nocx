package transport

import "fmt"

// What a person is told when THIS machine's helper will not come up
// (nocx-ie23r.4, ADR-0057, design L4).
//
// There is no local fallback: every local pane is a session on this machine's
// nocx-helper daemon, so a helper that cannot be installed, started or
// handshaken means no pane opens at all — by another route or by any route.
// What replaces the terminal is this refusal, and it names THREE things:
//
//   - WHAT FAILED — a reason from the closed set below, naming the boundary
//     that broke (the install, the start, or the handshake).
//   - WHY — the concrete error that boundary produced, never a category:
//     "fork/exec …: permission denied", "no space left on device", "sentinel
//     timeout".
//   - WHAT TO DO — an action from the closed set below.
//
// WHY THE FIRST AND THE THIRD ARE CLOSED SETS AND THE SECOND IS NOT. The
// middle part is a fact about the failure, and only the boundary that failed
// knows it, so it is the caller's to carry. The other two are decisions, and
// a refusal that ends at "why" is a defect in the refusal rather than a fact
// about the failure (ADR-0057's consequences): a person told their terminal
// will not open and given nothing to do about it has been informed, not
// helped. So neither is prose a caller writes. helperReasonWords and
// helperActionWords are the rendering, helperReasonActions is the action each
// reason names, and helperRefusalParts REFUSES to render a pair that is not in
// both — a refusal that cannot name an action fails here rather than shipping
// (TestEveryHelperRefusalReasonNamesAnAction walks the same sets).
//
// The sentence is RENDERED FROM the three, and the wire carries all of it: the
// message a person reads, plus data.reason and data.action for a surface that
// keys on values instead of matching prose — the shape the vault's refusals
// already use (rpcErrorFor, ws_vault.go).

// HelperRefusalReason names the boundary of this machine's helper that failed.
// It is closed for the reason every vocabulary on this wire is closed: a
// surface that keys on it must be able to tell an unrecognised value from a
// known one rather than render a blank explanation.
type HelperRefusalReason string

const (
	// HelperInstallFailed — this machine's generation was never put on disk:
	// nothing was installed at start, or the install itself failed.
	HelperInstallFailed HelperRefusalReason = "install"
	// HelperStartFailed — the installed binary did not come up: it is not
	// executable, or it ended before it served the endpoint.
	HelperStartFailed HelperRefusalReason = "start"
	// HelperHandshakeFailed — something answered on the endpoint and it is not
	// the generation this build installed, or nothing answered within the
	// handshake's budget.
	HelperHandshakeFailed HelperRefusalReason = "handshake"
)

// HelperRefusalAction names what the person can do, in the product's own words
// rather than in the failing boundary's.
type HelperRefusalAction string

const (
	// HelperActionFreeSpace — the install could not write its bytes: make room
	// on the filesystem that holds nocx's own directory.
	HelperActionFreeSpace HelperRefusalAction = "free-space"
	// HelperActionReinstallNocx — the helper ships inside the application, so
	// a copy that will not install or will not run is repaired by repairing
	// the application.
	HelperActionReinstallNocx HelperRefusalAction = "reinstall-nocx"
	// HelperActionQuitOtherNocx — one helper serves this machine, and another
	// copy of nocx is answering on its endpoint.
	HelperActionQuitOtherNocx HelperRefusalAction = "quit-other-nocx"
	// HelperActionRetryOpen — the endpoint is this build's and did not answer
	// in time; opening the pane again is the whole of the remedy.
	HelperActionRetryOpen HelperRefusalAction = "retry-open"
	// HelperActionFixPermissions — a directory of nocx's own is not one the
	// person owns or can write (the install directory, or the endpoint
	// directory under the same home), and a reinstall writes to the same
	// place and fails the same way.
	HelperActionFixPermissions HelperRefusalAction = "fix-permissions"
)

// helperRefusalReasons and helperRefusalActions are the closed sets written
// out, so a test can walk them instead of trusting that nobody added a member
// without a rendering.
var (
	helperRefusalReasons = []HelperRefusalReason{
		HelperInstallFailed, HelperStartFailed, HelperHandshakeFailed,
	}
	helperRefusalActions = []HelperRefusalAction{
		HelperActionFreeSpace, HelperActionReinstallNocx,
		HelperActionQuitOtherNocx, HelperActionRetryOpen,
		HelperActionFixPermissions,
	}
)

// helperReasonActions is the action each reason names when the caller has none
// more specific to give — L4's closure requirement in a data structure.
//
// Handshake failure is two different facts and the remedies differ, so a
// caller that knows which one it has overrides this entry; the default is the
// milder of the two, because retrying costs a person one gesture while closing
// another copy of nocx costs them their work.
var helperReasonActions = map[HelperRefusalReason]HelperRefusalAction{
	HelperInstallFailed:   HelperActionReinstallNocx,
	HelperStartFailed:     HelperActionReinstallNocx,
	HelperHandshakeFailed: HelperActionRetryOpen,
}

// helperReasonWords is what failed, in the sentence a person reads.
var helperReasonWords = map[HelperRefusalReason]string{
	HelperInstallFailed:   "Nocx could not install the helper that owns every pane on this machine",
	HelperStartFailed:     "Nocx installed this machine's helper and it did not start",
	HelperHandshakeFailed: "Nocx started this machine's helper and it did not answer as the build that installed it",
}

// helperActionWords is what to do, in the sentence a person reads.
var helperActionWords = map[HelperRefusalAction]string{
	HelperActionFreeSpace:      "Free space on the disk that holds nocx's own directory, then open the pane again",
	HelperActionReinstallNocx:  "Reinstall or update nocx — the helper ships inside the application, so a fresh copy is what repairs it — then open the pane again",
	HelperActionQuitOtherNocx:  "Quit every other nocx window and open the pane again: one helper serves this machine, and another copy of nocx is answering on its endpoint",
	HelperActionRetryOpen:      "Open the pane again",
	HelperActionFixPermissions: "Make sure nocx's own directory on this machine is one you own and can write, then open the pane again",
}

// HelperRefusal is a local helper failure in the three parts a person is owed.
type HelperRefusal struct {
	// Reason is the boundary that failed, from the closed set above.
	Reason HelperRefusalReason
	// Action is what the person can do. Empty means "the action this reason
	// names" (helperReasonActions), which is what a caller that knows only the
	// boundary has to say.
	Action HelperRefusalAction
	// Cause is the concrete error the failing boundary produced. It is
	// ENFORCED, not merely required: message() refuses to render without it,
	// the same way words() refuses an out-of-set reason or action, so all
	// three parts are construction invariants and none of them can reach the
	// wire as a placeholder.
	Cause error
}

// message renders the three parts into the sentence a person reads. The
// concrete error is quoted verbatim: it IS the second part, and paraphrasing it
// is how a refusal stops naming what actually broke.
func (r HelperRefusal) message() string {
	what, do := r.words()
	if r.Cause == nil {
		panic(fmt.Sprintf(
			"transport: helper refusal %q/%q has no cause: the concrete error is a part of the sentence, and a placeholder is not one",
			r.Reason, r.action()))
	}
	return what + ": " + r.Cause.Error() + ". " + do + "."
}

// words answers with this refusal's first and third parts, and REFUSES to
// answer at all when either is outside the sets it must come from.
//
// The refusal is a value a caller builds, so an unrenderable one is a
// programming error rather than a state a person reaches — but it is exactly
// the error ADR-0057 names ("a refusal that cannot name an action is a defect
// in the refusal"), and the alternative is a sentence that trails off at "why"
// and a wire field nobody can act on. So it fails LOUDLY here, and
// TestEveryHelperRefusalReasonNamesAnAction walks both sets in CI, which is the
// same invariant checked where it is cheap.
func (r HelperRefusal) words() (what, do string) {
	action := r.action()
	what, okReason := helperReasonWords[r.Reason]
	do, okAction := helperActionWords[action]
	if !okReason || !okAction {
		panic(fmt.Sprintf(
			"transport: helper refusal %q/%q is outside the closed sets: %q names no reason and %q names no action",
			r.Reason, action, r.Reason, action))
	}
	return what, do
}

// action answers with the action this refusal carries, filling it in from the
// reason when the caller left it empty.
func (r HelperRefusal) action() HelperRefusalAction {
	if r.Action != "" {
		return r.Action
	}
	return helperReasonActions[r.Reason]
}

// RefuseLocalHelper carries a helper refusal to the open path's own answer.
//
// It goes through the openRefusal carrier and not through a phase error, which
// is the whole reason that carrier exists: an answer the open path produced
// itself keeps its sentence instead of being flattened by the ssh taxonomy into
// "Internal error". What failed and what to do ride the error's data as values,
// so a surface keys on them rather than reading the sentence.
func RefuseLocalHelper(r HelperRefusal) error {
	return &openRefusal{
		code:    -32603,
		message: r.message(),
		data:    helperRefusalData{Reason: r.Reason, Action: r.action()},
	}
}

// helperRefusalData is the machine-readable half of a helper refusal: what
// failed and what to do, from the same closed sets the sentence is rendered
// from, and rendered by construction — the reason and the action are the two
// values checked before the sentence was built. The concrete error is
// deliberately NOT a field: it is a sentence about this failure, and a surface
// that needs it has the message.
type helperRefusalData struct {
	Reason HelperRefusalReason `json:"reason"`
	Action HelperRefusalAction `json:"action"`
}
