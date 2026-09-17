/**
 * Reduce one control-plane WebSocket frame to what a failure report may
 * print (nocx-n14oo.11): method, id and error — never params, never result.
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
  /** Present on a request or a notification; absent on a bare response. */
  method?: string
  /** Present on anything but a notification. `null` is a legal JSON-RPC id
   *  and is kept distinct from "absent". */
  id?: number | string | null
  /** The error object's `code` and `message` only — never `data`, which on
   *  at least one contract (control.saturated) nests operational detail that
   *  is fine to log but is not this function's to decide is safe. */
  error?: { code?: unknown; message?: unknown }
}

/** Parse one frame's text and keep only the fields declared above. Returns
 *  null for anything that is not a JSON object — a ping/pong control frame or
 *  a payload this suite does not recognise as JSON-RPC is not reported as a
 *  frame at all, rather than reported empty. */
export function redactFrame(direction: FrameDirection, raw: string): RedactedFrame | null {
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
  return out
}

/** One line per frame, for printing. Bounded by the caller — this only knows
 *  how to render a frame it is given, not how many to keep. */
export function formatFrame(f: RedactedFrame): string {
  const arrow = f.direction === 'sent' ? '->' : '<-'
  const parts = [arrow]
  if (f.method) parts.push(f.method)
  if ('id' in f) parts.push(`id=${JSON.stringify(f.id)}`)
  if (f.error) parts.push(`error=${JSON.stringify(f.error)}`)
  return parts.join(' ')
}
