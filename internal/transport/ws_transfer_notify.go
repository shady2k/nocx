package transport

// transfer.finished — the notification a background transfer raises when it
// reaches its end (nocx-zlxmm).
//
// WHY HERE AND NOT IN THE RENDERER. The outcome already reached a person, by
// a route that went past the pipeline entirely: upload-surface.ts and
// download-surface.ts each subscribed to their `*Done` notification and
// called showToast directly. The toast appeared, expired, and nothing
// remained — the notification centre had no record of a transfer, ever, so
// "what did I miss" could not answer the one question a person who walked
// away actually has.
//
// The fact is nocx's OWN, which is why this is attested and why it is not a
// renderer-callable method. settleUpload (ws_upload.go) and settleDownload
// (ws_download.go) are the points where a transfer's outcome becomes known
// to the backend: they are the single writers of files.uploadDone and
// files.downloadDone, and there is no earlier moment at which the answer
// exists. So there is no new wire method, no new contract, and no routing of
// showToast through the pipeline — the same shape session.ended has, where
// the registry that observed the fact raises it.
//
// It sits in its own file rather than inside the two settle functions
// because the WORDING is a product decision with one owner, exactly as
// blockFinishedTitle and sessionEndedTitle are: two places deciding how a
// finished transfer reads would part company the first time either changed.
// The two directions share it because they share the question — one Kind
// means one settings toggle, and a person has one answer to "tell me when a
// transfer ends".

import (
	"context"
	"strings"

	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/session"
)

// transferOutcome is everything the raise needs about a settled transfer,
// taken at the settle point from the same snapshot the *Done notification is
// built from.
//
// It is a struct of the closing FACTS rather than either direction's params
// type, for blockFinishedEvent's reason: a constructor typed to
// filesUploadDoneParams could not serve a download, whose account carries no
// finalName and no stranded list and does carry a byte count. What the two
// share is the four fields below, and they are the four the wording turns on.
type transferOutcome struct {
	// up is which way the bytes travelled. It decides the verb and nothing
	// else: the outcome vocabulary is otherwise the wire's own, and
	// `cancelled` and `failed` are spelt identically by both directions.
	up bool
	// state is the wire's terminal outcome — written/skipped/cancelled/failed
	// for an upload, sent/cancelled/failed for a download. It is the
	// notification's discriminator and it is deliberately not re-derived
	// here: uploadStateOf and downloadStateOf already own that mapping.
	state string
	// name is the file, as the person named it or as the far host spells
	// it. Never a path: a title has room for a file name and the directory
	// is on their screen.
	name string
	// reason is the failure's text, empty on every other outcome. It is the
	// same string files.uploadDone and files.downloadDone carry, and it is
	// carried for a failure ONLY — a cancelled transfer's error is a context
	// cancellation, which is not a fault and must never be shown as one.
	reason string
	// stranded is what an upload left behind on the far host: the upload
	// temp, the backup of the destination it was about to replace, or both.
	// Always empty for a download, which creates nothing there and so can
	// leave nothing behind.
	stranded []string
}

// maxTransferSubjectRunes bounds the file name a title may carry, for
// maxBlockSubjectRunes' reason: a name can be 255 bytes and a toast is one
// line. The bound is on the NAME alone so the verb the title ends with always
// survives — truncating the whole sentence would lose the one word that says
// how it went.
const maxTransferSubjectRunes = 64

// transferFinishedEvent is the event one settled transfer raises.
//
// At is deliberately not stamped, as at the session.ended and block.finished
// raises: ingress is the first nocx-owned stage and stamps it once, so a
// relay replaying a buffered batch keeps its own instants
// (internal/notify/ingress.go).
func transferFinishedEvent(sess session.Session, out transferOutcome) notify.Event {
	return notify.Event{
		SessionID: string(sess.ID()),
		Title:     transferFinishedTitle(out),
		Body:      transferFinishedBody(out),
		Kind:      notify.KindTransferFinished,
		Trust:     notify.TrustAttested,
		Level:     transferFinishedLevel(out),
		Attribution: notify.Attribution{
			// The same three fields the session.ended and block.finished
			// raises stamp, from the same source: the SESSION the backend
			// holds, never the envelope's claim about where it is (AD-7).
			// Backend carries commandnames.LocalRoute because every session
			// this build opens is on this machine (nocx-2gfh6), and the
			// renderer's occurrence lookup compares it.
			Backend: commandnames.LocalRoute,
			Host:    sess.Host(),
			Session: string(sess.ID()),
		},
	}
}

// transferFinishedLevel maps the terminal outcome onto severity, and the map
// is four decisions rather than a good/bad split — because Level now decides
// retention as well as colour. notify.MustAcknowledge makes warning and
// danger survive eviction until the person marks them read, so choosing a
// level is choosing whether this row waits for somebody.
//
//	failed    → danger.  The bytes did not arrive and only the person can
//	            decide what to do about it. Danger and not warning: this is
//	            the level the direct toast this replaces already used
//	            (upload-surface.ts, download-surface.ts, both `danger`), so
//	            the pipeline inherits the severity that shipped rather than
//	            quietly softening it. The contrast with blockFinishedLevel is
//	            the point — a command exiting non-zero is the most ordinary
//	            event a terminal has, and a transfer failing is rare, is
//	            something the person walked away from, and is the one outcome
//	            they came back for. It must wait for them.
//
//	written,
//	sent      → success.  The transfer happened. Informational: the file is
//	            there, so the row has nothing left to ask of anybody and may
//	            be evicted like any other cheap row.
//
//	skipped   → info.  NOT a success: nothing was written. The person was
//	            shown the collision and answered "skip", so the destination
//	            they already had is the one they still have. Info rather than
//	            warning for the reason a requested session close raises
//	            nothing at all (ws.go) — making somebody acknowledge their own
//	            answer teaches them to acknowledge without looking.
//
//	cancelled → info.  NOT a failure. They pressed cancel, or the binding
//	            went away underneath them; either way nothing is wrong and
//	            nothing is owed.
//
// The one crossing case is an upload that SUCCEEDED and left a path behind:
// success plus litter is a warning, because the file on the far host is real,
// nobody asked for it, and it will not explain itself later. That is the
// severity its own direct toast carried too.
func transferFinishedLevel(out transferOutcome) notify.Level {
	switch out.state {
	case uploadStateFailed: // == downloadStateFailed
		return notify.LevelDanger
	case uploadStateWritten, downloadStateSent:
		if len(out.stranded) > 0 {
			return notify.LevelWarning
		}
		return notify.LevelSuccess
	}
	return notify.LevelInfo
}

// transferFinishedTitle says which file and how it went, subject first,
// because the file name is what a person scans a toast for and the verb is
// what they need next.
//
// The outcome vocabulary is the WIRE's, not a second spelling of it
// (frontend/src/ui/operation.ts makes the same argument about the same four
// words): `written`, `skipped`, `cancelled`, `failed` for an upload and
// `sent` for a download's success. The default arm is unreachable from the
// two settle points — uploadStateOf and downloadStateOf between them return
// nothing else — and is a sentence rather than a panic because a title is
// not the place to discover a new enum member.
func transferFinishedTitle(out transferOutcome) string {
	noun := "Download"
	if out.up {
		noun = "Upload"
	}
	subject := transferSubject(out.name)
	switch out.state {
	case uploadStateWritten:
		return "Uploaded " + subject
	case downloadStateSent:
		return "Downloaded " + subject
	case uploadStateSkipped:
		return noun + " of " + subject + " was skipped"
	case uploadStateCancelled: // == downloadStateCancelled
		return noun + " of " + subject + " was cancelled"
	case uploadStateFailed: // == downloadStateFailed
		return noun + " of " + subject + " failed"
	}
	return noun + " of " + subject + " finished"
}

// transferSubject names the file in one line. An empty name is a product
// state rather than a defect — a download cancelled before its handle was
// pinned has none — so it becomes a subject that reads, rather than a title
// with a hole in it.
//
// strings.Fields collapses every run of whitespace, which is what keeps a
// file name containing a newline from becoming three lines of toast.
func transferSubject(name string) string {
	flat := strings.Join(strings.Fields(name), " ")
	if flat == "" {
		return "a file"
	}
	r := []rune(flat)
	if len(r) > maxTransferSubjectRunes {
		return string(r[:maxTransferSubjectRunes-1]) + "…"
	}
	return flat
}

// transferFinishedBody carries what the title had no room for, and it is the
// whole reason the two direct toasts can be deleted rather than kept beside
// this: everything they said, this says.
//
// BOTH halves when there are both, because the removed toasts were two and
// this is one. A failed upload that also stranded a path produced a `danger`
// toast with the reason and a `warning` toast with the paths
// (upload-surface.ts), and folding that into one row must not silently drop
// either — the reason says what went wrong and the paths name files sitting
// on somebody's disk that nobody will ever explain to them. The stranding is
// also orthogonal to the outcome: a `written` transfer whose backup unlink
// failed succeeded AND left litter, and there the paths are the only thing
// there is to say.
func transferFinishedBody(out transferOutcome) string {
	var parts []string
	if out.state == uploadStateFailed && out.reason != "" {
		parts = append(parts, out.reason)
	}
	if len(out.stranded) > 0 {
		parts = append(parts, "left behind on the server: "+strings.Join(out.stranded, ", "))
	}
	return strings.Join(parts, "; ")
}

// raiseTransferFinished puts one settled transfer's outcome into the
// notification pipeline. Both settle points call it and nothing else does:
// the guard below is one decision, and two copies of it would be two.
//
// THE SESSION IS READ HERE AND FROM NOWHERE ELSE. A notification's
// attribution may only come from the session the backend holds (AD-7), and a
// transfer carries a session id rather than a session — it outlives the
// WebSocket that started it and is bounded by the session instead (spec
// §5.1). A session already gone from the registry raises NOTHING, which is
// the same answer deliverTransferDone gives the *Done notification a line
// later ("the session is gone") and the same one handleHistoryRecord gives
// when its pane resolves to no live session. It is not a loss: the only way
// to reach this state is cancelSessionTransfers, which cancels every
// transfer of a session being torn down and forgets its retained outcomes —
// so the outcome would be `cancelled`, on a tab the person just closed, and
// session.ended already argues that an end somebody asked for must not
// become an event in the first place.
//
// Background context, deliberately, for the reason the session.ended and
// block.finished raises give at their seams. A transfer's own ctx is
// cancelled on exactly the paths that produce a terminal outcome, and Admit
// refuses a cancelled context outright — so binding it here would drop every
// cancelled transfer's record and, worse, would make the record depend on
// which way the transfer ended.
//
// Owner: the transfer's own goroutine, which reaches settle exactly once.
// Closing event: the return of Raise, which records the occurrence in the
// feed and hands the event to the policy synchronously — delivery past the
// debounce window is the policy's to own, not this call's. So the span has
// both ends: the context exists from this line until Raise returns, and
// nothing holds it afterwards.
func (s *WSServer) raiseTransferFinished(rt *runningTransfer, out transferOutcome) {
	if s.notifyRaiser == nil {
		return
	}
	sess, err := s.registry.Get(rt.sessionID)
	if err != nil {
		s.log.Debug("transfer outcome not notified: the session is gone",
			"transfer_id", rt.id, "session_id", rt.sessionID)
		return
	}
	// Owner: the transfer's own goroutine, which reaches settle exactly once.
	// Closing event: the return of Raise, which records the occurrence in the
	// feed and hands the event to the policy synchronously. Background and not
	// the transfer's ctx, which is CANCELLED on exactly the paths that produce
	// a terminal outcome — see the paragraph above.
	s.notifyRaiser.Raise(context.Background(), transferFinishedEvent(sess, out))
}
