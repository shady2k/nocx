// The fire adapter — the composition root's half of the palette contract.
// The palette is a view: it asks this adapter to fire one snippet and
// renders the outcome. The adapter is where the design's fire-time rules
// live, so they are testable without the app:
//
//   - Session facts are read AT THE CALL (design §8), never captured when
//     the palette opened: the composition root hands it a facts() that
//     reads the ACTIVE pane, so a tab switch between choosing a snippet and
//     confirming the fire targets the pane in front.
//   - The body resolves ONCE, here — env/ask substituted, {{secret:…}} left
//     intact for the delivery policy to handle (§7.3, §11.1).
//   - Delivery goes through the pane's insertSnippet — the one insertion
//     policy (design §9.2) — or the clipboard, which is an explicit
//     destination, never a derivation (§9.2).
//   - An ask value never reaches a log: the only thing logged is the
//     snippet's TITLE (design §7.5), never a resolved body.
import { log } from '../log'
import { findReferences } from '../secret-reference'
import type { SnippetFire } from '../terminal-content'
import { resolveBody, type SessionFacts } from './resolve'
import type { Snippet } from './snippets-store'

export type SnippetDestination = 'input' | 'clipboard'

/** Why a fire was refused — structured so the palette owns the sentences.
 *  The kinds mirror the design's refusal points (§9.4, §11.1, §11.2):
 *  an env key that cannot be answered, a destination that cannot honour
 *  what the text means, a name the vault cannot resolve. */
export type SnippetRefusalReason =
  | { kind: 'no-owner' }
  | { kind: 'env-unavailable'; keys: string[] }
  | { kind: 'multi-line-no-bracketed-paste' }
  | { kind: 'unresolved-secret'; name?: string }
  | { kind: 'write-failed' }
  | { kind: 'secret-to-clipboard'; name: string }

export type SnippetFireOutcome =
  | { kind: 'delivered'; where: 'editor' | 'pty' | 'clipboard' }
  | { kind: 'refused'; reason: SnippetRefusalReason }

export interface SnippetFireRequest {
  snippet: Snippet
  /** The ask answers, keyed by field name — collected in the palette's
   *  form moments ago, living only for this one call. */
  answers: ReadonlyMap<string, string>
  destination: SnippetDestination
}

export interface SnippetFireDeps {
  /** Session facts read AT FIRE TIME — the composition root reads the
   *  active pane on every call, never a captured snapshot. */
  facts(): Promise<SessionFacts>
  /** The pane to deliver into — resolved at fire time. Null → no-owner. */
  activeInsert(): { insertSnippet(text: string): Promise<SnippetFire> } | null
  clipboard: { writeText(text: string): Promise<void> }
}

export function createSnippetFireAdapter(
  deps: SnippetFireDeps,
): (req: SnippetFireRequest) => Promise<SnippetFireOutcome> {
  return async (req): Promise<SnippetFireOutcome> => {
    // env/ask resolve NOW, against the LIVE pane. The ask answers came from
    // the palette's form; the facts are fresh — the interval between
    // choosing a snippet and confirming may have moved the pane, and the
    // fire must name where the user is looking now (design §8, §9.5).
    const facts = await deps.facts()
    const outcome = resolveBody(req.snippet.body, facts, req.answers)
    if (outcome.kind === 'refused') {
      return { kind: 'refused', reason: { kind: 'env-unavailable', keys: outcome.keys } }
    }
    if (outcome.kind === 'needs-fields') {
      // The palette asked for every field before firing; reaching this is a
      // palette bug, and refusing beats firing a body that still carries
      // an unanswered parameter as literal text.
      return { kind: 'refused', reason: { kind: 'no-owner' } }
    }
    const text = outcome.text
    const target = deps.activeInsert()
    if (target === null) return { kind: 'refused', reason: { kind: 'no-owner' } }

    if (req.destination === 'clipboard') {
      // The clipboard outlives the fire and is read by everything on the
      // machine: a resolved secret there is a different exposure from
      // handing it to the program the user is looking at (§11.1) — refused,
      // naming the reference.
      const secret = findReferences(text)[0]
      if (secret !== undefined) {
        return { kind: 'refused', reason: { kind: 'secret-to-clipboard', name: secret.name } }
      }
      try {
        await deps.clipboard.writeText(text)
      } catch {
        return { kind: 'refused', reason: { kind: 'write-failed' } }
      }
      log.info('snippet fired', { title: req.snippet.title, destination: 'clipboard' })
      return { kind: 'delivered', where: 'clipboard' }
    }

    const fire = await target.insertSnippet(text)
    if (fire.ok) {
      log.info('snippet fired', {
        title: req.snippet.title,
        destination: 'input',
        where: fire.where,
      })
      return { kind: 'delivered', where: fire.where }
    }
    switch (fire.reason) {
      case 'no-owner':
        return { kind: 'refused', reason: { kind: 'no-owner' } }
      case 'multi-line-no-bracketed-paste':
        return { kind: 'refused', reason: { kind: 'multi-line-no-bracketed-paste' } }
      case 'unresolved-secret':
        return { kind: 'refused', reason: { kind: 'unresolved-secret', name: fire.name } }
      case 'write-failed':
        return { kind: 'refused', reason: { kind: 'write-failed' } }
    }
  }
}
