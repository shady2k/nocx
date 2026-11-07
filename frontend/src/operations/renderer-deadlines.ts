// Renderer deadlines, armed per failure domain.
//
// ADR-8eej3e66 §3: the renderer's timer exists to detect a lost backend; it is
// the projection of turn + max plus a margin, paused while the backend reports
// the answer phase. A domain is armed when that domain's backend deadlines are
// enforced (§ Migration: "Renderer timers exist but are armed per domain, as
// it migrates: an unarmed call has none, because backend work is still allowed
// to run past its declared deadline"). No domain is armed at this stage.
import { callOperationDeclaration } from '../generated/operations'

/** The domains whose backend deadlines are enforced. Empty: nothing is armed
 *  in this stage, so no call gets a deadline timer. */
const ARMED_DOMAINS: ReadonlySet<string> = new Set()

/** Margin over turn+max for the round trip itself. */
export const DEADLINE_MARGIN_MS = 5_000

/** The renderer timer for a method, or null when its domain is not armed. */
export function rendererDeadlineMs(
  method: string,
  armed: ReadonlySet<string> = ARMED_DOMAINS,
): number | null {
  const declaration = callOperationDeclaration(method)
  if (declaration === undefined || declaration.domain === '') return null
  if (!armed.has(declaration.domain)) return null
  return declaration.turnMs + declaration.maxMs + DEADLINE_MARGIN_MS
}
