// Agent status derived from the terminal title.
//
// A coding agent runs as one long shell command, so OSC 133 — which marks
// shell command boundaries (nocx-5mn.4) — cannot see inside it. What agents do
// expose is their state in the title they set via OSC 0/2, and we already
// receive that through TerminalRenderer.onTitle. Orca reads the same signal
// (src/shared/terminal-title-status.ts); this is the same idea, kept to the
// markers we can actually verify.
//
// Two facets, deliberately not merged — a lesson taken from Orca's own
// comments: evidence that *something* is working is not proof of *who* is
// working. This module answers only the first question.

export type AgentStatus = 'working' | 'idle'

// The braille spinner frames every TUI animates while busy. Presence of any of
// them in a title means work is in progress, whichever agent it is.
const BRAILLE_SPINNER = /[⠀-⣿]/

// Claude Code prefixes its title with ✳ when it is waiting on you, and swaps
// in a spinner frame while it works. The title text after the marker is the
// task description, not the agent's name, so the marker is all we can key on.
const CLAUDE_IDLE = '✳' // ✳

/**
 * Classifies a terminal title into agent activity, or null when the title
 * carries no agent signal at all (a plain shell, a path, a program name).
 *
 * Returning null is meaningful: it means "this title says nothing about an
 * agent", and callers must fall back to their own rules rather than assuming
 * idle. A title that never mentions an agent is not an idle agent.
 */
export function detectAgentStatus(title: string): AgentStatus | null {
  const t = title.trim()
  if (!t) return null

  // Working beats idle: a spinner frame is live evidence, while the ✳ can
  // linger in a title that has not been repainted yet.
  if (BRAILLE_SPINNER.test(t)) return 'working'
  if (t.startsWith(`${CLAUDE_IDLE} `) || t === CLAUDE_IDLE) return 'idle'

  return null
}
