// Submit orchestration for the command editor (ADR-0004 §2, nocx-4ff.12).
// The ordering is the fix: tell the state machine we left the prompt so
// ownership clears, refocus the grid so PS2 continuation + running-program
// keys reach the PTY, THEN send the renderer-owned paste handoff.
//
// The resolution half (planSubmit) is the renderer side of ADR-0021's
// "a line may reference a vault secret by name; the backend resolves it at
// submit": the RESOLVED line goes to the PTY and nowhere else, while the
// reference INTACT line goes to lifecycle history. A sealed vault or an
// unresolved name must never silently send a broken line — the caller
// surfaces the verdict and keeps the draft.
import type { VaultResolveLine, ResolveRef } from './vault-client'
import { findReferences } from './secret-reference'

export interface SubmitDeps {
  focusGrid(): void
  sendDoc(doc: string): void
}

export function submitCommand(doc: string, deps: SubmitDeps): void {
  deps.focusGrid()
  deps.sendDoc(doc)
}

// ── planSubmit: resolve references before the atomic handoff ───────────────

/** What a submit sends vs. what it records. The split is the invariant the
 *  whole epic exists for: a command carrying a reference moves to another
 *  machine and resolves that machine's secret, while a command carrying a
 *  pasted key is both dead and dangerous. */
export interface SubmitPlan {
  /** The line to write to the PTY — every reference resolved. May carry
   *  secret values: never persisted, never logged, never in a model
   *  context (ADR-0021). */
  readonly sendLine: string
  /** The reference-intact line carried in lifecycle.submitAttempt. The
   *  backend persists this text; sendLine is PTY-only. */
  readonly recordLine: string
  /** One entry per reference, first-occurrence order. */
  readonly refs: ReadonlyArray<ResolveRef>
}
/** A line that must not be sent as-is. The caller shows the reason where
 *  the user is looking and keeps the draft. */
export interface SubmitFailure {
  /** 'unresolved' — names the vault does not hold. 'sealed' — the vault is
   *  locked (the dispatcher seam may already have raised the unlock prompt;
   *  reaching here means it was cancelled or absent). 'error' — the resolve
   *  call failed for another reason, with `message`. */
  readonly reason: 'unresolved' | 'sealed' | 'error'
  /** The names that did not resolve, first-occurrence, deduplicated. Only
   *  for reason 'unresolved'. */
  readonly names?: ReadonlyArray<string>
  /** A human-readable detail. Only for reason 'error'. */
  readonly message?: string
}

export type SubmitVerdict = SubmitPlan | SubmitFailure

export function isSubmitFailure(verdict: SubmitVerdict): verdict is SubmitFailure {
  return 'reason' in verdict
}

/** The resolve seam: vault.resolveLine over whatever RPC the host has. */
export interface ResolveLineFn {
  (line: string): Promise<VaultResolveLine>
}

/**
 * Decide what a submit sends and what it records. A plain line (no
 * {{secret:…}} span) costs NOTHING — no wire call, no await of substance —
 * so an ordinary Enter is not a round trip. A line with references resolves
 * through the seam; a sealed vault or an unresolved name is a FAILURE the
 * caller must surface, never a silent send of a broken line.
 */
export async function planSubmit(doc: string, resolveLine: ResolveLineFn): Promise<SubmitVerdict> {
  const refs = findReferences(doc)
  if (refs.length === 0) {
    return { sendLine: doc, recordLine: doc, refs: [] }
  }
  let resolved: VaultResolveLine
  try {
    resolved = await resolveLine(doc)
  } catch (err) {
    if (isSealedError(err)) return { reason: 'sealed' }
    return { reason: 'error', message: err instanceof Error ? err.message : String(err) }
  }
  const unresolved = resolved.refs
    .filter((r) => !r.resolved)
    .map((r) => r.name)
    .filter((name, i, all) => all.indexOf(name) === i)
  if (unresolved.length > 0) {
    return { reason: 'unresolved', names: unresolved }
  }
  return { sendLine: resolved.line, recordLine: doc, refs: resolved.refs }
}

/**
 * The SYNCHRONOUS fast path: a line with no reference span resolves to
 * itself — no wire call, no promise, so the atomic handoff keeps its
 * no-gap semantics for an ordinary Enter (a microtask gap between Enter and
 * the commit would let a fast-typed key drop the submission). Null when the
 * line HAS references and the async resolve is required.
 */
export function planSubmitSync(doc: string): SubmitPlan | null {
  if (findReferences(doc).length > 0) return null
  return { sendLine: doc, recordLine: doc, refs: [] }
}

/** The sealed-vault discriminator, read the way the dispatcher reads it:
 *  code -32001 or data.reason 'vault-sealed' (contracts/vault.status). */
export function isSealedError(err: unknown): boolean {
  if (!err || typeof err !== 'object') return false
  const e = err as { code?: unknown; data?: unknown }
  if (e.code === -32001) return true
  const d = e.data as { reason?: unknown } | undefined
  return d?.reason === 'vault-sealed'
}
