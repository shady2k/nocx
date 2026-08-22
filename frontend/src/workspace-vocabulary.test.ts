import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

// THIS EPIC SHIPS MEMBERSHIP, NOT A FENCE (workspaces UX design §5.5, and the
// last of nocx-isoph.5's acceptance criteria — automated, not by eye).
//
// A workspace groups tabs. It enforces nothing: two tabs from different
// workspaces on one host share a user and a filesystem, and `kill` works. The
// control-plane boundary is nocx-mp2vd's, and until it exists the UI may say
// only navigational things. The first draft of the design said the chip made
// the fence visible — a security promise with no mechanism behind it, which
// the adversarial review called its strongest finding.
//
// So this test greps what ships. It is deliberately cheap and deliberately
// mechanical: prose about safety is exactly the kind of thing that arrives in
// a hurry, from someone who has not read a design document from 2026-08-15.
//
// WHAT IT DOES NOT CLAIM. It cannot know that a sentence is about a
// workspace; it fires when a forbidden word and one of workspace/tab/pane
// share a line of shipped source. When the fence lands, this test is edited
// deliberately — as an `AD` is — rather than deleted by whoever it annoys.

const SRC = resolve(__dirname)

/** The words that describe a fence, and would be a lie today. */
const FORBIDDEN = /\b(safe|safely|isolated|isolation|contained|containment)\b/i

/** What they may not be said about. */
const SUBJECT = /\b(workspace|workspaces|tab|tabs|pane|panes)\b/i

/**
 * The glyphs a fence is drawn with, in any file the workspace chrome owns.
 *
 * Three spellings, because a glyph reaches the screen three ways: the
 * character itself, a CSS escape for its codepoint (`content: '\\1F512'` — the
 * form that slipped past the first version of this regex), and one of the
 * kit's own lock or shield icon components.
 */
const FENCE_GLYPH =
  /\u{1F512}|\u{1F510}|\u{1F50F}|\u{1F6E1}|\u{26E8}|\\0*(1f512|1f510|1f50f|1f6e1|26e8)\b|\b(Lock|LockOpen|Shield)[A-Za-z]*Icon\b/iu

/** Modules owned exclusively by workspace chrome. Generic tab-strip/tab
 *  modules are deliberately absent: they now carry a truthful per-pane
 *  sandbox marker and a named sandbox-launch action, neither of which says a
 *  workspace is a fence. A workspace shield would have to enter the
 *  workspace-owned chip/group modules below and still fails this guard. */
const WORKSPACE_CHROME = [
  'workspace-chip.tsx',
  'layout/strip-groups.ts',
  'layout/strip-tree.ts',
  'styles/components/workspace-chip.css',
]

/** Whether a line is a comment. A rule about SHIPPED STRINGS may not fire on
 *  a comment explaining the rule — this file's own header would be the first
 *  false positive, and the chip's doc comment the second. */
function isComment(line: string): boolean {
  const t = line.trim()
  return t.startsWith('//') || t.startsWith('*') || t.startsWith('/*')
}

function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      if (entry === 'test-support' || entry === 'generated') continue
      out.push(...sourceFiles(path))
      continue
    }
    if (!/\.(ts|tsx|css)$/.test(entry)) continue
    if (/\.(test|spec)\.(ts|tsx)$/.test(entry)) continue
    out.push(path)
  }
  return out
}

describe('a workspace is a group of tabs, and the product says nothing more (§5.5)', () => {
  it('describes no workspace, tab or pane as safe, isolated or contained', () => {
    const offences: string[] = []
    for (const file of sourceFiles(SRC)) {
      const lines = readFileSync(file, 'utf8').split('\n')
      lines.forEach((line, i) => {
        if (isComment(line)) return
        if (!FORBIDDEN.test(line) || !SUBJECT.test(line)) return
        offences.push(`${relative(SRC, file)}:${i + 1}: ${line.trim()}`)
      })
    }

    expect(offences, 'epic B ships membership, not a fence — see workspaces-ux §5.5').toEqual([])
  })

  it('draws no shield and no lock in the workspace chrome', () => {
    const offences: string[] = []
    for (const name of WORKSPACE_CHROME) {
      const lines = readFileSync(join(SRC, name), 'utf8').split('\n')
      lines.forEach((line, i) => {
        if (isComment(line)) return
        if (!FENCE_GLYPH.test(line)) return
        offences.push(`${name}:${i + 1}: ${line.trim()}`)
      })
    }

    expect(offences, 'a lock over a workspace advertises machinery that is absent').toEqual([])
  })

  it('fires on the copy it exists to refuse', () => {
    // The check above is only worth having if it would catch the sentence the
    // review found. Both halves, on the strings themselves.
    expect(FORBIDDEN.test('Tabs in this workspace are isolated from the rest')).toBe(true)
    expect(SUBJECT.test('Tabs in this workspace are isolated from the rest')).toBe(true)
    expect(FORBIDDEN.test('Everything here is safe')).toBe(true)
    expect(FENCE_GLYPH.test('<LockIcon />')).toBe(true)
    expect(FENCE_GLYPH.test("content: '\u{1F6E1}'")).toBe(true)
    expect(FENCE_GLYPH.test("content: '\\1F512'")).toBe(true)
    // And is not so eager that it forbids the words in their ordinary senses.
    expect(SUBJECT.test("scrollMode: 'contained'")).toBe(false)
    expect(FORBIDDEN.test('the safety of the connection')).toBe(false)
  })
})
