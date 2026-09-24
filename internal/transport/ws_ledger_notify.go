package transport

// block.finished — the notification a closed block raises (nocx-n3nfg,
// design §3).
//
// WHY HERE AND NOT ANYWHERE ELSE. The design names this source "block ledger
// (ADR-0024) — exit code and duration", and §2.2 closes ingress authority:
// `block.finished` "originates only at the lifecycle publication boundary".
// That boundary is where a command's end becomes a durable fact of nocx's own
// ledger, which is what lets the event be stamped `attested`.
//
// The event has two backend lifecycle producers: ledger.close raises it for
// an applied close, while authenticated lifecycle completion raises it after
// the lifecycle-owned history row is closed. Neither path trusts renderer
// claims or reads the byte stream.
//
// Nothing here looks at the byte stream: the outcome arrives as a typed
// lifecycle fact, and the backend owns both the durable row and notification.
// This keeps AD-6 untouched and preserves AD-1 as amended by nocx-m64b.
//
// It sits in its own file rather than inside handleClose because the WORDING
// is a product decision with one owner, the way sessionEndedTitle is
// (nocx-lmmi5): two places deciding how a finished command reads would part
// company the first time either changed.

import (
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/notify"
	"github.com/shady2k/nocx/internal/session"
)

// maxBlockSubjectRunes bounds the command text a title may carry. The intent
// column takes maxLedgerIntentRunes (16 384) and a banner is one line: a
// title that long is not a longer title, it is an unreadable one. The bound
// is on the SUBJECT alone so the verb the title ends with always survives —
// truncating the whole sentence would produce "make te…" and lose the one
// word that says how it went.
const maxBlockSubjectRunes = 96

// blockFinishedEvent is the event one closed block raises.
//
// intent is the MASKED intent — the same text the lifecycle writers store,
// screened by the shared masking owner. It must never be the raw submitted
// text: a title is presentation data that reaches a banner, a toast and
// (once targets land) a network sink, so a secret escaping here would escape
// further than one escaping into the database.
//
// At is deliberately not stamped, as at ws.go's session.ended raise: ingress
// is the first nocx-owned stage and stamps it once, so a helper replaying a
// buffered batch keeps its own instants (internal/notify/ingress.go).
//
// status and facts are the lifecycle closing facts, shared by ledger.close
// and authenticated lifecycle completion.
func blockFinishedEvent(sess session.Session, intent string, status content.EntryStatus, facts ledgerCloseFacts) notify.Event {
	return notify.Event{
		SessionID: string(sess.ID()),
		Title:     blockFinishedTitle(intent, status),
		Body:      blockFinishedBody(facts),
		Kind:      notify.KindBlockFinished,
		Trust:     notify.TrustAttested,
		Level:     blockFinishedLevel(status),
		Attribution: notify.Attribution{
			// The same three fields the session.ended raise stamps, from the
			// same source: the SESSION the backend holds, never the
			// envelope's claim about where it is (AD-7). Backend carries
			// commandnames.LocalRoute because every session this build opens
			// is on this machine (nocx-2gfh6), and the renderer's occurrence
			// lookup compares it.
			Backend: commandnames.LocalRoute,
			Host:    sess.Host(),
			Session: string(sess.ID()),
		},
	}
}

// blockFinishedLevel maps the entry's final status onto severity, by the same
// rule ws.go's session.ended raise uses: the ordinary good outcome is a
// success and everything else is a warning. Warning and not danger,
// deliberately — a command exiting non-zero is a normal event in a terminal,
// and a `danger` for `grep` finding nothing would spend the loudest level the
// pipeline has on the least alarming thing it sees.
func blockFinishedLevel(status content.EntryStatus) notify.Level {
	if status == content.EntrySuccess {
		return notify.LevelSuccess
	}
	return notify.LevelWarning
}

// blockFinishedTitle says what finished and how it went, in that order,
// because the subject is what the user scans a banner for and the verb is
// what they need next. The accepted lifecycle status vocabulary is closed by
// the ledger-close validator, and the default arm covers statuses that say
// the run ended without saying how.
func blockFinishedTitle(intent string, status content.EntryStatus) string {
	subject := blockSubject(intent)
	switch status {
	case content.EntrySuccess:
		return subject + " succeeded"
	case content.EntryFailure:
		return subject + " failed"
	case content.EntryInterrupted:
		return subject + " was interrupted"
	}
	return subject + " finished"
}

// blockSubject names the command in one line. An EMPTY intent is a product
// state and not a defect — an orphan OSC 133 C is an entry with no intent
// (design §4.4) — so it becomes a subject that reads, rather than a title
// that opens with a space.
//
// strings.Fields collapses every run of whitespace, which is what makes a
// multi-line heredoc a title instead of three lines of banner.
func blockSubject(intent string) string {
	flat := strings.Join(strings.Fields(intent), " ")
	if flat == "" {
		return "A command"
	}
	r := []rune(flat)
	if len(r) > maxBlockSubjectRunes {
		return string(r[:maxBlockSubjectRunes-1]) + "…"
	}
	return flat
}

// blockFinishedBody carries the detail the title had no room for, and its
// whole job is that a command which FAILED is distinguishable from one that
// succeeded even where a surface renders body and title together.
//
// The exit code wins when there is one, because it is the shell's own answer
// and the number the user will search for. A zero is omitted: it says nothing
// the title has not already said, exactly as the session.ended raise omits
// "exit status 0". With no code — an interrupted command has none, and only
// the shell arm carries one at all — the termination reason speaks, in words
// rather than in the column's vocabulary, and only where it adds something:
// `completed` and `failed` are what the title already reported.
func blockFinishedBody(f ledgerCloseFacts) string {
	if f.ExitCode != nil && *f.ExitCode != 0 {
		return fmt.Sprintf("exit status %d", *f.ExitCode)
	}
	switch content.TerminationReason(f.TerminationReason) {
	case content.TermTimeout:
		return "timed out"
	case content.TermTransportGone:
		return "the connection was lost"
	case content.TermUserKilled:
		return "stopped from nocx"
	case content.TermAgentDeclined:
		return "the agent declined it"
	case content.TermInterrupted:
		return "interrupted"
	}
	return ""
}
