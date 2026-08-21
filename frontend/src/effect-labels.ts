/**
 * The product's words for the effect lattice (ADR-0020 decision 6).
 *
 * ONE owner. The approval prompt asks the question ("The assistant wants to
 * read and inspect") and the Agent policy page carries the standing answer
 * ("Read and inspect — Allowed"); a person must meet the same words in both
 * places, and two surfaces inventing their own wording for one state is the
 * defect AGENTS.md names under "Look for the existing answer before you write
 * a second one".
 *
 * The canonical form is the MID-SENTENCE one, and the heading is derived from
 * it rather than the other way round. A heading is a label with its first
 * letter capitalised; going the other way would have to lower-case a heading,
 * which mauls any label that ever starts with a proper noun. So the map holds
 * the form that cannot be reconstructed, and `effectHeading` holds the rule.
 */
import type { EffectKey } from './policy-client'

export const EFFECT_LABEL: Record<EffectKey, string> = {
  observe: 'read and inspect',
  'mutate-reversible': 'make changes that can be undone',
  'mutate-destructive': 'make changes that cannot be undone',
  'privilege-change': 'gain more privilege',
  disclose: 'send information out',
  'cross-boundary': 'reach another host',
  delegate: 'hand work to another agent',
}

/** The same words as a heading: sentence case, no other change. */
export function effectHeading(key: EffectKey): string {
  const label = EFFECT_LABEL[key]
  return label.charAt(0).toUpperCase() + label.slice(1)
}
