package transport

// The expansion source (nocx-4h0m7.5): when the assistant is about to ask a
// person to approve `rm -rf $HOME/x`, this is what answers "and what IS
// $HOME, right now, in the shell that would run it".
//
// WHERE THE ANSWER MAY COME FROM, and it is one place. ADR-0024 put the
// lifecycle on an AUTHENTICATED channel precisely because OSC 133 is an
// anonymous broadcast that any process with the tty open can write; ADR-0049
// made that channel the carrier for everything of variable size. A value a
// person is about to stake a `rm -rf` on is exactly the class of fact that
// must not come off an anonymous stream — a command's own output can forge
// one. So the query goes over the authenticated channel or it does not go at
// all, and "does not go" is a NAMED product outcome rather than a fallback.
//
// WHAT IS NOT AVAILABLE TODAY, MEASURED AND WRITTEN DOWN. The shell can only
// speak from a PROMPT: `internal/shellintegration/scripts/nocx.bash` records
// the measurement (nocx-z9s9.16) — "a shell idle in readline runs no traps
// at all. Measured on bash 5.2 and 5.3, a SIGUSR1 raised while the user is
// sitting at a prompt does not run its handler until the next command is
// submitted, and SIGWINCH does not flush it either". An approval question is
// asked at exactly that moment: the lane is idle, sitting in readline, and
// there is no way to make it answer before the person decides. The kernel's
// only backend-originated request today, `refresh_request`, is answered at
// the NEXT prompt boundary for that reason, and the same bound applies to
// any request this source could send.
//
// So Expand refuses HONESTLY rather than guessing, and the refusal carries
// which of the two reasons it is: no integration in this session (a remote
// host without our shell bundle, a native prompt, a lane that never
// helloed), or an integrated session whose shell cannot be reached in time.
// The surface renders that as "not asked", which is a different fact from
// "unsafe, left as written" and must never be shown as the same one.
//
// UNBLOCKING IT is a protocol addition and not a change here: a
// kernel-originated request kind alongside `refresh_request`, a shell tier
// that can read the channel while idle in its line editor (zsh's `zle -F`
// can; bash has no equivalent, which is why this is a bead of its own), and
// this method's body then becomes the send-and-await. Nothing else in the
// expansion path changes: the classification, the display, the carrier and
// the re-read fence are all already independent of who answers.

import (
	"context"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/session"
)

// Expand implements assistant.ExpansionSource over the authenticated shell
// integration channel. It NEVER writes to the pty and never reads the OSC
// stream: the two paths that could answer this question dishonestly are the
// two it is forbidden to take (AD-1, AD-6, ADR-0024).
//
// It executes nothing. The query it is handed has already been restricted to
// pure reads by the assistant's syntactic classifier, and this method must
// not evaluate it as a command string even so — the classification is a
// precondition of the query, not a licence to run it.
func (s *WSServer) Expand(_ context.Context, sessionID string, q assistant.ExpansionQuery) (assistant.ExpansionAnswer, error) {
	if len(q.Expressions) == 0 && len(q.Programs) == 0 {
		return assistant.ExpansionAnswer{}, nil
	}
	if !s.runLeaseIntegrationAvailable(session.ID(sessionID)) {
		// The remote host our integration is not deployed on, and every
		// other un-integrated session: expand nothing, mark every variable
		// unresolved. This is the branch the product is in today for every
		// session — see the file header for the measurement that makes it
		// so — and it is the correct answer for an un-integrated session
		// whatever else changes.
		return assistant.ExpansionAnswer{}, &assistant.ExpansionUnavailableError{
			Reason: "nocx's shell integration is not live in this session, so no value was read — every variable below is left exactly as written",
		}
	}
	// An integrated session, and still no answer: the shell owns when it
	// reads the channel and that is a prompt boundary, which an approval
	// question never coincides with (nocx-z9s9.16). Saying so is the honest
	// outcome; inventing a value from the backend's own environment, from
	// /proc/<pid>/environ, or from the byte stream would each be a
	// different way of putting a number in front of a person that is not
	// the number the command will use.
	return assistant.ExpansionAnswer{}, &assistant.ExpansionUnavailableError{
		Reason: "this session's shell answers only at a prompt and is sitting at one, so no value could be read in time — every variable below is left exactly as written",
	}
}
