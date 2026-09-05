// ═══════════════════════════════════════════════════════════════════════════
// Skill presentation — judgements about how a skill's own fields render,
// shared by every surface that draws one (nocx-btg7d review). Lives beside
// the store rather than inside it: skills-store.ts must not import BadgeTone
// from the kit, and a presentation module must not import SkillsStore.
// ═══════════════════════════════════════════════════════════════════════════

import type { BadgeTone, FileReadoutOutcome } from './ui'
import type { Skill } from './skills-store'
import type { SkillsFile } from './generated/skills.file'
import { scanPatternWords } from './scan-pattern-words'

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

/**
 * The wire's refusal as the reader's outcome — what a person's file view
 * draws for one already-read file (nocx-872jc.4's findings-travel-with-the-
 * bytes reasoning): the bytes with the scan's own matches marked in place,
 * or one of two reasons there are no bytes to show. Total over
 * SkillsFile['refusal']'s closed union, so a fifth wire value fails this
 * switch's compile rather than rendering nothing.
 *
 * ONE OWNER, PARTIALLY. skills-section.tsx's `fileOutcome` still carries its
 * own copy of exactly this switch — wrapped in that file's own `FileAsk`
 * union, which this function does not know about — because that file is
 * being rewritten whole by a later task and is out of scope to edit here
 * (nocx-4m1n1's own review). Moving skill-view-body.tsx's copy here is still
 * worth doing now: it is one fewer place the mapping can drift, and it is
 * where skills-section.tsx's own copy folds into an import once that
 * rewrite lands, the way its `provenanceTone` copy already did above.
 */
export function skillFileOutcome(result: SkillsFile): FileReadoutOutcome {
  switch (result.refusal) {
    case '':
      return {
        kind: 'text',
        text: result.text,
        marks: result.findings.map((finding) => ({
          lineNumber: finding.lineNumber,
          label: scanPatternWords(finding.patternId),
        })),
      }
    case 'not-text':
      return { kind: 'not-text' }
    case 'too-large':
      return { kind: 'too-large', maxBytes: result.maxBytes }
  }
}
