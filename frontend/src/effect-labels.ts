/**
 * The product's words for the effect lattice (ADR-0020 decision 6).
 *
 * ONE owner. The approval prompt states it as a row ("read and inspect")
 * and the Assistant permissions page carries the standing answer
 * ("read and inspect — Allowed"); a person must meet the same words in both
 * places, and two surfaces inventing their own wording for one state is the
 * defect AGENTS.md names under "Look for the existing answer before you write
 * a second one".
 *
 * The form is the MID-SENTENCE one, and it is the only one. Both surfaces read
 * a label inside a sentence — "wants to read and inspect", "may the assistant
 * read and inspect?" — so a capitalised variant had exactly one caller, the
 * matrix editor's row heading, and went with it (nocx-hvb3r). If a heading
 * form is ever wanted again it derives from this one and not the reverse:
 * lower-casing a heading would maul any label that begins with a proper noun.
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
