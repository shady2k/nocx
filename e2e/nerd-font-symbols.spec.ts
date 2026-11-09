import { test, expect, promptReady } from './harness'

// A LOCAL PANE KEEPS ITS NERD FONT GLYPHS — BYTES AND INK (nocx-15b13).
//
// The owner's local omp (18.12.6) drew its prompt and status-line icons as
// empty boxes, while the same tool over ssh (18.14.0) drew them in the same
// window. Two readings were on the table and this spec decides them:
//
//   (a) the tool emits Nerd Font private-use codepoints (U+E000-U+F8FF,
//       U+F0000 and up, powerline U+E0A0-E0D7) and no font the pane renders
//       with carries them;
//   (b) UTF-8 sequences are split across frames on the local path and decoded
//       as U+FFFD.
//
// The first half answers (b): the codepoints are written ONE BYTE AT A TIME, so
// every byte is its own PTY write and every frame the emulator publishes lands
// on a partial sequence, and they must still arrive in the cell model whole.
// They do — and did on the shipping tree too, which is what ruled (b) out and
// left (a).
//
// The second half is (a): the shipped private-use fallback puts a real glyph in
// that cell. It is measured as INK, because that is the difference the owner
// saw: the same codepoint drawn with the fallback in the chain against the same
// codepoint drawn without it is a glyph against the .notdef box.

// window.__nocxPaneScreen is declared ONCE for the e2e project, in
// e2e/pane-screen.ts (PaneScreenReading); this spec reads the same seam and
// declares nothing of its own (nocx-15b13).

/** Powerline separator — U+E0B0, three UTF-8 bytes, carried by the fallback. */
const POWERLINE = '\ue0b0'
/** Material Design, supplementary private use — U+F0001, four UTF-8 bytes,
 *  also carried by the fallback. */
const SUPPLEMENTARY = '\u{f0001}'
/** A private-use codepoint the shipped fallback does NOT carry: the control
 *  that says a box means "not in this face", not "the mechanism failed". */
const ABSENT = '\uf600'

const MARKER = 'ICONS-'

/** The codepoint written one byte per write, with a pause so the emulator
 *  really sees them as separate reads. */
function byteByByte(codepoint: string, pause = 0.06): string {
  return Array.from(new TextEncoder().encode(codepoint))
    .map((byte) => `printf '\\x${byte.toString(16).padStart(2, '0')}'`)
    .join(`; sleep ${pause}; `)
}

/** The ink of one codepoint drawn with one font chain: a hash of the canvas's
 *  alpha channel, so two chains that draw the same shape hash the same. */
async function ink(page: import('@playwright/test').Page, family: string, ch: string) {
  return page.evaluate(
    ({ family, ch }) => {
      const canvas = document.createElement('canvas')
      canvas.width = 64
      canvas.height = 64
      const ctx = canvas.getContext('2d')
      if (!ctx) throw new Error('no 2d context')
      ctx.font = `32px ${family}`
      ctx.fillStyle = '#000'
      ctx.fillText(ch, 4, 44)
      const data = ctx.getImageData(0, 0, 64, 64).data
      let hash = 2166136261
      for (let i = 0; i < data.length; i += 4) {
        hash ^= data[i + 3]
        hash = Math.imul(hash, 16777619) >>> 0
      }
      return hash
    },
    { family, ch },
  )
}

test('a local pane keeps Nerd Font private-use codepoints whole, and paints them', async ({
  page,
}) => {
  await page.goto('/')
  await promptReady(page)

  // 1. THE BYTES. Both codepoints, one byte per write, then a newline.
  // The prefix is written as two shell strings, so the text the model must
  // carry exists ONLY in the output: the echoed command line spells it
  // `ICONS''-`, and no contiguous match can come from the echo.
  await page.keyboard.type(
    `printf 'ICONS''-'; ${byteByByte(POWERLINE)}; ${byteByByte(SUPPLEMENTARY)}; printf '\\n'`,
  )
  await page.keyboard.press('Enter')

  const rows = () => page.evaluate(() => window.__nocxPaneScreen?.()?.rows ?? [])
  const iconRow = async (): Promise<string> =>
    (await rows()).find((row) => row.startsWith(MARKER)) ?? ''

  // A split sequence is ONE codepoint in the model, not replacement characters
  // and not a gap: this is (b) answered.
  await expect
    .poll(async () => iconRow(), { timeout: 20_000 })
    .toContain(`${MARKER}${POWERLINE}${SUPPLEMENTARY}`)

  // 2. THE PAINT. The row the painter drew carries both glyphs, and it is drawn
  //    in the terminal's chain — the app's mono stack plus the private-use
  //    fallback.
  const painted = () =>
    page.evaluate((marker) => {
      const rows = Array.from(document.querySelectorAll('.pane.active .term-grid-row'))
      const row = rows.find((candidate) => (candidate.textContent ?? '').startsWith(marker))
      if (!row) return null
      const grid = document.querySelector('.pane.active .term-grid')
      return {
        text: row.textContent ?? '',
        chain: grid === null ? '' : getComputedStyle(grid).fontFamily,
      }
    }, MARKER)
  await expect
    .poll(async () => (await painted())?.text ?? '', { timeout: 20_000 })
    .toContain(`${MARKER}${POWERLINE}${SUPPLEMENTARY}`)
  const shown = await painted()
  expect(shown?.chain ?? '').toContain('Nerd Symbols Mono')

  // 3. THE INK. The chain with the fallback draws a glyph for a codepoint the
  //    plain chain draws as .notdef — and for a codepoint the fallback does not
  //    carry, the two chains agree, which is what makes the first measurement
  //    mean "this face has the glyph" rather than "something moved".
  // A face that cannot load is not an error here — it is the same measurement
  // one step earlier: the chain draws what it has, and the ink says so.
  await page.evaluate(() =>
    document.fonts.load('32px "Nerd Symbols Mono"', '\ue0b0').catch(() => []),
  )
  const withFallback = '"Nordwand Mono", "Nerd Symbols Mono", monospace'
  const withoutFallback = '"Nordwand Mono", monospace'
  for (const codepoint of [POWERLINE, SUPPLEMENTARY]) {
    await expect
      .poll(
        async () =>
          (await ink(page, withFallback, codepoint)) !==
          (await ink(page, withoutFallback, codepoint)),
        {
          timeout: 10_000,
          message: `the fallback draws no glyph for U+${codepoint.codePointAt(0)!.toString(16)}`,
        },
      )
      .toBe(true)
  }
  expect(await ink(page, withFallback, ABSENT)).toBe(await ink(page, withoutFallback, ABSENT))
})
