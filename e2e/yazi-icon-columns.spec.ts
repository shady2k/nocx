/**
 * yazi's columns line up on every row, including rows whose entries carry
 * Nerd Font icon glyphs (nocx-peg8e.1.3).
 *
 * yazi draws its three columns by writing a row's cells CONTIGUOUSLY and
 * positioning only the start of the row: a left-column entry's icon, name,
 * padding and the separator are one run with no cursor move between them, and
 * only a row with no left-column entry gets a CUP to the separator column.
 * So the terminal's width for an icon is part of the row's arithmetic: any
 * cell the row spends on an icon more than one is a cell every later column on
 * that row moves right. That is what the owner saw: rows 1-4 of yazi, exactly
 * the rows whose left column had entries, shifted by about a cell against rows
 * 5+.
 *
 * WHAT WAS WRONG. Not the emulator: it gives every codepoint yazi's theme uses
 * exactly one cell, and Ghostty's own width table is the authority it follows
 * (internal/emulator/ghostty, TestYazisThemeIconsTakeOneCell). Not yazi: one
 * column is what unicode-width reports and what it drew for. The painter laid
 * the row out by the advance it had MEASURED for the glyph — and while the
 * private-use face was still in flight the browser drew the primary font's
 * .notdef, whose advance is exactly one cell. cell-fit verdicts "lands on its
 * own", boxes nothing, and when the symbols face swapped in at 1em the row
 * reflowed 0.4em wider at the icon. The face is now fetched before the first
 * pane (terminal-font.ts), so the first measurement is the glyph that is
 * really painted.
 *
 * The fixture prints the same code points yazi's default theme assigns —
 * U+EB9C (Library), U+E5FF (a directory), U+E5FE (the hovered directory),
 * U+F401/U+F498 (Documents/Downloads), U+F0219 (notes) — and then a separator,
 * exactly as yazi writes them. The separator's column is what is asserted:
 * painted column 21 and painted column 100, on every row, in both browsers.
 */

import { test, expect, promptReady, resolveBackend } from './harness'
import { rpc } from './sshd-fixture'

/** A printf format string whose output is yazi's own row layout:
 *
 *  - rows 1..4: CUP to column 2, then ` icon name ...pad │  icon name ...pad │`
 *    written with no cursor move between the cells (yazi writes it so);
 *  - rows 5..10: CUP to column 21, then the same middle column.
 *
 *  A separator is therefore at painted column 21 and column 100 only if an
 *  icon costs exactly one cell. The icons go in as bash printf escapes, so the
 *  command carries no private-use characters itself.
 *
 *  The rows ride inside a SYNCHRONIZED OUTPUT block (DECSET 2026), as yazi's
 *  own redraw does: one held frame, published once, with no later frame to
 *  re-measure anything. A fixture written row by row would hide the defect,
 *  because the next chunk's paint re-reads the metric after the face landed. */
function fixtureCommand(): string {
  const rows: string[] = []
  const names = ['Library', 'root', 'shady', 'Shared']
  const icons = ['\\uEB9C', '\\uE5FF', '\\uE5FF', '\\uE5FF']
  const middle = ['Applications', 'Documents', 'Downloads', 'Library', 'notes.txt', 'projects']
  const midIcons = ['\\uE5FF', '\\uF401', '\\uF498', '\\uEB9C', '\\U000F0219', '\\uE5FF']
  for (let i = 0; i < 10; i++) {
    const r = i + 1
    let s = ''
    if (i < 4) {
      const name = names[i]
      s += `\\033[${r};2H ${icons[i]} ${name}${' '.repeat(16 - name.length)}\\033[37m\\u2502`
    } else {
      s += `\\033[${r};21H\\033[37m\\u2502`
    }
    const name = middle[i % middle.length]
    s += `\\033[34m ${midIcons[i % midIcons.length]} ${name}${' '.repeat(75 - name.length)}\\033[37m\\u2502`
    rows.push(s)
  }
  return `printf '\\033[?1049h\\033[2J\\033[?2026h${rows.join('')}\\033[?2026l'; read x`
}

interface Separator {
  row: number
  /** 0-based index among this row's separators, left to right. */
  index: number
  /** Painted column, counted from the grid's left edge in cell widths. */
  column: number
}

/**
 * Every painted column separator, read off the grid the person is looking at.
 *
 * A `│` is found through a DOM Range over its own character, so its rectangle
 * is where the browser PAINTED it, not where the model says it belongs: a run
 * whose glyph advance is wider than its cells lays the following cells out to
 * the right, and that is exactly the defect.
 */
async function separators(page: import('@playwright/test').Page): Promise<Separator[]> {
  return page.evaluate(() => {
    const grid = document.querySelector<HTMLElement>('.pane.active .terminal-live-grid')
    if (grid === null) return []
    const gridLeft = grid.getBoundingClientRect().left
    const cellWidth = Number.parseFloat(
      getComputedStyle(grid).getPropertyValue('--term-cell-width'),
    )
    if (!Number.isFinite(cellWidth) || cellWidth <= 0) return []
    const out: Separator[] = []
    const rows = [...grid.querySelectorAll<HTMLElement>('.term-grid-row')]
    rows.forEach((row, rowIndex) => {
      let index = 0
      const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT)
      let node = walker.nextNode()
      while (node !== null) {
        const value = node.textContent ?? ''
        for (let i = 0; i < value.length; i++) {
          if (value[i] !== '\u2502') continue
          const range = document.createRange()
          range.setStart(node, i)
          range.setEnd(node, i + 1)
          const rect = range.getBoundingClientRect()
          out.push({
            row: rowIndex + 1,
            index,
            column: Math.round((rect.left - gridLeft) / cellWidth),
          })
          index += 1
        }
        node = walker.nextNode()
      }
    })
    return out
  })
}

test('yazi’s columns keep their painted place on rows that carry icons', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  // THE SHIPPED FONT THIS NEEDS. The defect is visible only when the advance
  // measured BEFORE the private-use face lands is exactly one cell: that is the
  // primary font's .notdef, so cell-fit verdicts "lands on its own" and boxes
  // nothing. The container's default theme stack resolves to a face whose
  // .notdef is NOT one cell (a 9px cell against 8.4px for Nordwand at 14px), so
  // a box appears regardless and the defect hides. Nordwand Mono — the shipped
  // font, chosen through the shipped setting — has a 0.6em .notdef, which is
  // exactly the condition the owner's Mac was in.
  //
  // THE SETTING IS APPLIED BY A RELOAD, not after it, so the precondition is
  // the startup path: the first pane must open on a face the product has
  // already fetched. Setting it live would load the face through the settings
  // change and let this pass without any of the startup ordering.
  const shipped = await page.evaluate(
    () => document.documentElement.dataset.terminalFont ?? 'system',
  )
  const info = await resolveBackend(page)
  try {
    await rpc(page, info, 'settings.set', { key: 'terminal.fontFamily', value: 'nordwand-mono' })
    await page.reload()
    await promptReady(page)
    await expect
      .poll(() => page.evaluate(() => document.documentElement.dataset.terminalFont ?? null), {
        timeout: 10_000,
      })
      .toBe('nordwand-mono')

    await page.keyboard.type(fixtureCommand())
    await page.keyboard.press('Enter')
    // TEN ROWS, TWO SEPARATORS EACH: the fixture is on the screen when all
    // twenty are painted.
    await expect
      .poll(async () => (await separators(page)).filter((s) => s.row <= 10).length, {
        timeout: 20_000,
      })
      .toBe(20)
    // Every row of the fixture holds the same two separators, at model columns
    // 20 and 99 (yazi's 21 and 100, one-based). If the private-use face is
    // measured only after it lands, the verdict taken against its .notdef is
    // stale and the separators on every row with an icon sit one column right.
    // A waiting matcher on what the product paints, never a duration.
    await expect
      .poll(
        async () =>
          (await separators(page))
            .filter((s) => s.row <= 10)
            .map((s) => [s.row, s.index, s.column]),
        { timeout: 15_000 },
      )
      .toEqual(
        Array.from({ length: 10 }, (_, row) => [
          [row + 1, 0, 20],
          [row + 1, 1, 99],
        ]).flat(),
      )
  } finally {
    // The stand is shared by every spec in the shard: put the font back.
    await rpc(page, info, 'settings.set', { key: 'terminal.fontFamily', value: shipped })
  }

  await page.keyboard.press('Enter')
  await page.keyboard.type("printf '\\033[?1049l'")
  await page.keyboard.press('Enter')
})
