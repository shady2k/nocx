// Renderer deadlines, armed per failure domain.
//
// ADR-8eej3e66 §3: the renderer's timer exists to detect a lost backend; it is
// the projection of turn + max plus a margin, paused while the backend reports
// the answer phase. The record arms a domain when that domain's backend
// deadlines are enforced, and stage 8 arms them ALL: the last capacity refusal
// went with the lane, and every domain now settles at its declared deadline
// (`-32005` with the declaration's own reason).
//
// The set is DERIVED from the catalogue, never listed here. A domain is a
// field of a declaration, so a domain nobody armed would be a deadline the
// renderer never notices is lost; deriving it means a new domain is armed by
// existing, and the test compares the set with the generated catalogue.
import { callOperationDeclaration, catalogueDomains } from '../generated/operations'

/** The domains whose backend deadlines are enforced: every domain the
 *  catalogue declares. */
const ARMED_DOMAINS: ReadonlySet<string> = new Set(catalogueDomains())

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

/** The armed domains, for the test that compares them with the catalogue. */
export function armedDomains(): ReadonlySet<string> {
  return ARMED_DOMAINS
}
