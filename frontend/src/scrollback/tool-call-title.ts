// What a tool call is CALLED on screen (ADR-0040) — one owner, because the
// header of a tool block and anything else that ever has to name a call must
// not each invent their own phrasing.
//
// WHY IT EXISTS. The call announcement used to carry no arguments at all: a
// person read the tool name and the DERIVED RESOURCE, on the argument that
// the resource is the readable half. It is — when it differs between calls.
// For every session-scoped tool it does not: `readScreen`, `blocks.list` and
// `blocks.read` all name the pane, so one turn drew four calls as the same
// three words, two of them `blocks.read` of different finished commands. The
// arguments are what tell two calls apart, so the arguments are what the
// title is built from.
//
// A SESSION IS NAMED, NEVER NUMBERED (nocx-vnzek, kept from the line this
// replaces). The session's id is an internal handle and printing it back
// reads as `blocks.read sessionId=9bb9a7602c27e8ba…`. The pane's own display
// title is what a person calls that session, and it is INJECTED rather than
// derived here: this module owns the paint rule, the pane layer owns the
// name. Which argument holds the session is not guessed from its key either
// — the backend's derived resource says so, and that is the ONE derivation
// (internal/assistant/policy.go namedResource) the policy's scope check and
// the approval ask already share.
//
// NOTHING IS TRUNCATED. The title is the block's header text and the header
// is what the ⋮ menu copies, so a shortened value would put an ellipsis in
// somebody's clipboard. A long header is a CSS problem and is solved where
// the command kind already solves it.

/** What the title needs from outside itself. */
export interface ToolCallTitleDeps {
  /** What a SESSION is called to a person — the pane's own display title,
   *  the same words the tab strip shows. Null when no pane in this window
   *  holds that session, and null is a real answer: the id is then left out
   *  of the title rather than printed, because printing it is the defect. */
  sessionName?: (sessionId: string) => string | null
}

/** The facts a title is built from — the wire's, narrowed. */
export interface ToolCallTitleSpec {
  tool: string
  args?: Record<string, unknown>
  resource?: { kind: string; id: string }
}

/** One argument's value, as a person reads it. A string is itself; anything
 *  else is its compact JSON, which is what the model actually sent. */
function valueText(value: unknown): string {
  if (typeof value === 'string') return value
  if (value === null || value === undefined) return ''
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  try {
    return JSON.stringify(value)
  } catch {
    // A value that will not serialise is not worth failing a title over:
    // the call still happened and the tool name still says what it was.
    return ''
  }
}

/**
 * The header text for one tool call: the tool, then its arguments as
 * `key=value` in the order the model spelled them.
 *
 * The session argument — the one the backend's resource names — is replaced
 * by the pane's display title, and dropped entirely when nothing in this
 * window can name that session. Every other argument is verbatim.
 *
 * A call with no arguments is its tool name alone, and a call whose only
 * argument was an unnameable session is too. Neither invents a placeholder:
 * the tool name is a true and complete sentence about what happened.
 */
/** One `key=value` pair as a person reads it. A value carrying a space is
 *  QUOTED, because the pairs are joined by spaces and an unquoted one stops
 *  being one value: a pane called "* Claude Code" turned
 *  `session.read session=* Claude Code id=att-cf87…` into a line where
 *  nothing says which words belong to `session` (nocx-hp8p2.12). Session
 *  names are the common case — they are tab titles, written by people. */
function pairText(key: string, value: string): string {
  return /\s/.test(value) ? `${key}="${value}"` : `${key}=${value}`
}

export function toolCallTitle(call: ToolCallTitleSpec, deps: ToolCallTitleDeps = {}): string {
  const args = call.args ?? {}
  const sessionId = call.resource?.kind === 'session' ? call.resource.id : null
  const parts: string[] = []
  let sessionCarried = false
  for (const [key, raw] of Object.entries(args)) {
    if (sessionId !== null && raw === sessionId) {
      sessionCarried = true
      const named = deps.sessionName?.(sessionId) ?? null
      if (named) parts.push(pairText(key, named))
      continue
    }
    const text = valueText(raw)
    if (text === '') continue
    parts.push(pairText(key, text))
  }
  // THE RESOURCE NAMES THE PANE, NOT THE ARGUMENT (nocx-i4gg7). The session
  // used to arrive as a parameter the model spelled out, and the title read
  // it from there; the backend supplies it from the transport now, so for a
  // session-scoped tool the arguments carry no session at all. The derived
  // resource was always the authority — the comment above says so — and the
  // argument was only its carrier, so WHERE a call acted is still printed
  // when nothing carried it. Without this a person reads `session.read` and
  // cannot tell which pane was read, which is the defect nocx-vnzek fixed.
  if (sessionId !== null && !sessionCarried) {
    const named = deps.sessionName?.(sessionId) ?? null
    if (named) parts.unshift(pairText('session', named))
  }
  if (parts.length === 0) return call.tool
  return `${call.tool} ${parts.join(' ')}`
}
