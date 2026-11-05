/**
 * Reduce one control-plane WebSocket frame to what a failure report may
 * print (nocx-n14oo.11): method, id and error — never params, never result.
 * The one exception is a `session.intent` answer, whose closed outcome
 * (state, bytes written, refusal cause) is kept and nothing else of it
 * (nocx-atpw5.1.36): a refusal arrives as a result, not an error, so without
 * it a lost keystroke and a dropped one print the same nothing.
 *
 * A JSON-RPC request's params can carry a vault passphrase (vault.unseal),
 * an endpoint API key (endpoints.create), or the text of a command a person
 * typed for the assistant to read (policy.classify) — exactly the class of
 * thing AGENTS.md says a dev build's log already carries and a failure
 * report must not repeat. A result can echo the same material back. Method,
 * id and the error object are the only fields this suite has ever needed to
 * diagnose a control-plane failure from the outside, so those are the only
 * ones kept; everything else in the frame is dropped, not merely hidden.
 *
 * The DATA plane — binary frames carrying raw PTY bytes — is a different
 * message type entirely (AD-1) and is never handed to this function; see
 * failure-context.ts, which only listens for text frames in the first
 * place.
 */

export type FrameDirection = 'sent' | 'received'

export interface RedactedFrame {
  direction: FrameDirection
  /** Present on a request or a notification, and on a response this module
   *  can match to the request that produced it (see the `calls` argument) —
   *  a bare `<- id=50` does not say what was answered. */
  method?: string
  /** Present on anything but a notification. `null` is a legal JSON-RPC id
   *  and is kept distinct from "absent". */
  id?: number | string | null
  /** The error object's `code` and `message` only — never `data`, which on
   *  at least one contract (control.saturated) nests operational detail that
   *  is fine to log but is not this function's to decide is safe. */
  error?: { code?: unknown; message?: unknown }
  /** A `session.intent`'s answer, reduced to its closed outcome — see
   *  [intentOutcome]. Absent for every other method, and for a request no
   *  answer has arrived for yet. */
  intent?: IntentOutcome
}

/** The whole of a `session.intent` result that may be written down: whether
 *  the runtime took it, how many bytes it wrote, and — when it refused — the
 *  closed cause from `contracts/session.intent.schema.json`.
 *
 *  WHY NOTHING ELSE. The result also carries `refusal.regionNow`, which is
 *  the target's region AS IT READS NOW: that is screen text, which is what a
 *  person's program printed, and it does not belong in a report. What is left
 *  is an enum, a count and an enum.
 *
 *  WHY IT IS HERE AT ALL. A refused intent is a RESULT, not an error — so
 *  before this, "the keys never reached the program" and "the keys were
 *  refused with zero bytes written" printed the same thing: nothing
 *  (nocx-atpw5.1.36, nocx-atpw5.1.38). */
export interface IntentOutcome {
  state?: string
  bytesWritten?: number
  refusalCause?: string
}

/** id → method of the requests this page has sent, so a bare response can be
 *  named. Bounded by the caller's own use (one per page, one entry per open
 *  request): a response removes its entry. */
export type PendingCalls = Map<string, string>

/** Parse one frame's text and keep only the fields declared above. Returns
 *  null for anything that is not a JSON object — a ping/pong control frame or
 *  a payload this suite does not recognise as JSON-RPC is not reported as a
 *  frame at all, rather than reported empty. */
export function redactFrame(
  direction: FrameDirection,
  raw: string,
  calls: PendingCalls = new Map(),
): RedactedFrame | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null
  const obj = parsed as Record<string, unknown>
  const out: RedactedFrame = { direction }
  if (typeof obj.method === 'string') out.method = obj.method
  if ('id' in obj) {
    const id = obj.id
    if (typeof id === 'number' || typeof id === 'string' || id === null) out.id = id
  }
  if ('error' in obj && typeof obj.error === 'object' && obj.error !== null) {
    const err = obj.error as Record<string, unknown>
    out.error = { code: err.code, message: err.message }
  }
  // A response says what answered it. The method comes from the request this
  // page sent under the same id, never from the response itself; an id this
  // module never saw a request for keeps answering bare.
  if (out.id !== undefined && out.id !== null) {
    const key = String(out.id)
    if (out.method !== undefined) {
      calls.set(key, out.method)
    } else {
      const method = calls.get(key)
      if (method !== undefined) {
        calls.delete(key)
        out.method = method
        if (method === 'session.intent') out.intent = intentOutcome(obj.result)
      }
    }
  }
  return out
}

/** The closed part of one `session.intent` result (see [IntentOutcome]). An
 *  unknown shape answers nothing rather than guessing. */
function intentOutcome(result: unknown): IntentOutcome | undefined {
  if (typeof result !== 'object' || result === null) return undefined
  const r = result as Record<string, unknown>
  const outcome: IntentOutcome = {}
  if (typeof r.state === 'string') outcome.state = r.state
  if (typeof r.bytesWritten === 'number') outcome.bytesWritten = r.bytesWritten
  const refusal = r.refusal
  if (typeof refusal === 'object' && refusal !== null) {
    const cause = (refusal as Record<string, unknown>).cause
    if (typeof cause === 'string') outcome.refusalCause = cause
  }
  return Object.keys(outcome).length > 0 ? outcome : undefined
}

/** One line per frame, for printing. Bounded by the caller — this only knows
 *  how to render a frame it is given, not how many to keep. */
export function formatFrame(f: RedactedFrame): string {
  const arrow = f.direction === 'sent' ? '->' : '<-'
  const parts = [arrow]
  if (f.method) parts.push(f.method)
  if ('id' in f) parts.push(`id=${JSON.stringify(f.id)}`)
  if (f.error) parts.push(`error=${JSON.stringify(f.error)}`)
  if (f.intent) parts.push(`intent=${JSON.stringify(f.intent)}`)
  return parts.join(' ')
}
