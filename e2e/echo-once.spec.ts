import { expect, test, promptReady, clickIntoEditor } from './harness'

/**
 * nocx-4y82c.1.11 — the command echo must appear once in a frozen block's body.
 *
 * The command has exactly two `printf` tokens, so a body that echoes it once
 * carries two; a duplicate carries four. Two runs — one that finishes inside
 * the composer's grace and one that outlives it — separate the tty's echo from
 * a redraw that follows the composer hiding.
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
  // The command has exactly two `printf` tokens. A body that echoes the typed
  // line once carries two; the block below carries FOUR, because the rows
  // artifact starts at the interval start (the prompt row) when the command
  // finishes before the backend's output-start mark lands. The same command
  // with a trailing sleep -- which gives the mark time -- carries two; that is
  // the control in the next test.
  await page.setViewportSize({ width: 460, height: 360 })
  await page.goto('/')
  await promptReady(page)
  const command = `printf "ECHO-ONCE\\n"; printf '%s\\n' ${WORDS.join(' ')}`
  const counts = await countEcho(page, command, 'w-40')
  console.log('FAST ' + JSON.stringify(counts))
  expect(counts.printfs, 'the command echo is not stored as output').toBe(2)
})

test('echo count: a command that outlives it (composer hides)', async ({ page }) => {
  await page.setViewportSize({ width: 460, height: 360 })
  await page.goto('/')
  await promptReady(page)
  const command = `printf "ECHO-ONCE\\n"; printf '%s\\n' ${WORDS.join(' ')}; sleep 1; true`
  console.log('SLOW ' + JSON.stringify(await countEcho(page, command, 'w-40')))
})
