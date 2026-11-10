import { expect, test, promptReady, clickIntoEditor } from './harness'

/**
 * nocx-4y82c.1.11 — a frozen block's body is its OUTPUT, never the echo of the
 * command that wrote it.
 *
 * The rows before a block's output start are the typed command's echo, and
 * whatever the terminal's reflow leaves behind when the composer hides: the
 * screen's own rendering of a line that is already in the block's header. The
 * artifact begins at the helper's ordered output mark, so a command that
 * finishes before that mark lands and one that outlives it carry the SAME
 * body — this spec asserts both, with the same numbers, which is the rule.
 *
 * The command has exactly two `printf` tokens, so a body that still held the
 * echo would carry two and one that held a redraw of it four. The body must
 * carry neither: the count is zero, and the rows are the command's own output.
 */
async function countEcho(
  page: import('@playwright/test').Page,
  command: string,
  marker: string,
): Promise<{ printfs: number; rows: number }> {
  await clickIntoEditor(page)
  await page.locator('.pane.active .nocx-editor-input').fill(command)
  await page.keyboard.press('Enter')
  await expect
    .poll(
      async () =>
        page.evaluate(
          (m) => (document.querySelector('.pane.active')?.textContent ?? '').includes(m),
          marker,
        ),
      { timeout: 30_000 },
    )
    .toBe(true)
  await promptReady(page)
  let previous = ''
  await expect
    .poll(
      async () => {
        const signature = await page.evaluate((m) => {
          const block = [
            ...document.querySelectorAll<HTMLElement>(
              '.pane.active .scrollback-inner > .cmd-block',
            ),
          ].find((candidate) =>
            (candidate.querySelector('.cmd-header-text')?.textContent ?? '').includes(m),
          )
          const body = block?.querySelector<HTMLElement>('.cmd-output')
          return body
            ? `${body.querySelectorAll('.term-grid-row').length}:${Math.round(body.getBoundingClientRect().height)}`
            : 'absent'
        }, marker)
        const stable = signature === previous && signature !== 'absent'
        previous = signature
        return stable
      },
      { timeout: 15_000 },
    )
    .toBe(true)
  return page.evaluate((m) => {
    const blocks = [
      ...document.querySelectorAll<HTMLElement>('.pane.active .scrollback-inner > .cmd-block'),
    ]
    const block = blocks.find((candidate) =>
      (candidate.querySelector('.cmd-header-text')?.textContent ?? '').includes(m),
    )!
    const body = block.querySelector<HTMLElement>('.cmd-output')!
    const text = [...body.querySelectorAll<HTMLElement>('.term-grid-row')].map((row) =>
      (row.textContent ?? '').trimEnd(),
    )
    return { printfs: (text.join('\n').match(/printf/g) ?? []).length, rows: text.length }
  }, marker)
}

const WORDS = Array.from({ length: 40 }, (_, i) => `w-${String(i + 1).padStart(2, '0')}`)

test('a command that outruns its output-start mark does not store its own echo', async ({
  page,
}) => {
  // The command finishes inside the composer's grace, so its echo has already
  // left the screen, and a reflow redraws its head, before the backend's
  // output-start mark lands. All of that is screen, not output: the artifact
  // begins at the mark.
  await page.setViewportSize({ width: 460, height: 360 })
  await page.goto('/')
  await promptReady(page)
  const command = `printf "ECHO-ONCE\\n"; printf '%s\\n' ${WORDS.join(' ')}`
  const counts = await countEcho(page, command, 'w-40')
  console.log('FAST ' + JSON.stringify(counts))
  expect(counts.printfs, 'the command echo is not stored as output').toBe(0)
  expect(counts.rows, 'the body is the command output, one row per line').toBe(41)
})

test('the same command that outlives the grace carries the same body', async ({ page }) => {
  // The control: the trailing sleep gives the mark all the time it needs. The
  // body must be the same one — a frozen block is its output, not a record of
  // how the echo got there.
  await page.setViewportSize({ width: 460, height: 360 })
  await page.goto('/')
  await promptReady(page)
  const command = `printf "ECHO-ONCE\\n"; printf '%s\\n' ${WORDS.join(' ')}; sleep 1; true`
  const counts = await countEcho(page, command, 'w-40')
  console.log('SLOW ' + JSON.stringify(counts))
  expect(counts.printfs, 'the command echo is not stored as output').toBe(0)
  expect(counts.rows, 'the body is the command output, one row per line').toBe(41)
})
