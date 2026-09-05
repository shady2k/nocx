// ═══════════════════════════════════════════════════════════════════════════
// Skill presentation — judgements about how a skill's own fields render,
// shared by every surface that draws one (nocx-btg7d review). Lives beside
// the store rather than inside it: skills-store.ts must not import BadgeTone
// from the kit, and a presentation module must not import SkillsStore.
// ═══════════════════════════════════════════════════════════════════════════

import type { BadgeTone } from './ui'
import type { Skill } from './skills-store'

/**
 * Provenance as a badge tone: `builtin` is neutral because it is the state
 * nobody chose, `authored` is what the person wrote, `managed` what the
 * assistant wrote after they approved it, and `installed` warning because it
 * is the one provenance whose bytes a stranger wrote — the row should say so
 * before the person reads the description as though it were their own.
 *
 * ONE OWNER. This used to be copied into skill-view's header, on the
 * argument that a closed union with no default case fails the compile in
 * both files the day a fifth provenance is added. That argument is true and
 * beside the point: the drift that actually happens is someone changing
 * `installed` from warning to danger in one copy and not the other, which
 * compiles clean in both files and disagrees silently — two copies of one
 * judgement agree everywhere you look and disagree where you did not.
 * skills-section.tsx keeps its own copy for now (it is being rewritten by a
 * later task, which drops it for this import in one line); every new
 * consumer takes this one.
 */
export function provenanceTone(provenance: Skill['provenance']): BadgeTone {
  switch (provenance) {
    case 'builtin':
      return 'neutral'
    case 'authored':
      return 'info'
    case 'managed':
      return 'success'
    case 'installed':
      return 'warning'
  }
}
