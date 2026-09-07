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
 * skills-section.tsx's own copy folded into this import when the row's card
 * was deleted (nocx-54a2c); every consumer takes this one now.
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
 * ONE OWNER. skills-section.tsx's `fileOutcome` — the modal card's own copy
 * of this switch, wrapped in that file's `FileAsk` union — was deleted with
 * the card (nocx-54a2c): the row no longer reads a file's bytes at all, so
 * there is nothing left there for this mapping to drift from.
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

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

/**
 * `4 Sep` — the short form the skill viewer's design settled on (its own
 * §2 layout diagram), and now the row's too (nocx-54a2c's third evidence
 * line). A fixed short form rather than `toLocaleDateString`, whose
 * day/month ORDER (not only the month's name) varies by locale and would
 * make the row and the check pane read a stored date differently on two
 * machines checking the same skill.
 *
 * ONE OWNER: this used to be a private copy inside skill-view-check.tsx
 * alone; the row growing its own second copy of the same eleven-line
 * function for the same "4 Sep" is exactly the drift `provenanceTone`'s own
 * comment above warns about, so it moved here instead.
 */
export function shortDate(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return `${d.getDate()} ${MONTHS[d.getMonth()]}`
}

/** "used 12 times, last on 3 Mar" — or the honest absence. */
export function usageLine(usage: Skill['usage']): string {
  if (usage.count === 0) return 'never used'
  const lastUsed = usage.lastUsedAt ? `, last on ${shortDate(usage.lastUsedAt)}` : ''
  return `used ${usage.count} ${usage.count === 1 ? 'time' : 'times'}${lastUsed}`
}

/**
 * The words for a refusal's closed reason (nocx-j0lei). The SENTENCE a person
 * acts on is the server's — nocx knows which file and which number — and this
 * is only the short label the row's status cell shows, so a column of them
 * can be scanned for what KIND of problem each directory has.
 *
 * An unrecognised value falls back to a label rather than rendering blank: a
 * status cell with nothing in it reads as "no problem", which is the one
 * thing a refused row must never say.
 */
export function refusalLabel(reason: string): string {
  switch (reason) {
    case 'symlink':
      return 'Symbolic link'
    case 'unreadable':
      return 'Unreadable'
    case 'frontmatter':
      return 'No frontmatter'
    case 'name':
      return 'Unusable name'
    case 'noDescription':
      return 'No description'
    case 'descriptionTooLong':
      return 'Description too long'
    default:
      return 'Not indexed'
  }
}
