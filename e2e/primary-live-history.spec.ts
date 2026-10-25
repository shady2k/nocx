// A live primary-buffer program remains scrollable without a recording,
// including after the renderer reconnects to the same still-running process.
import { expect } from '@playwright/test'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  standalone as test,
  appReadyForInput,
  bindEndpoint,
  openControlPlane,
  VaultBackend,
  type ControlPlane,
} from './harness'
import { readStand } from './stand'

const ROWS = 240
const READY = 'PRIMARY-TUI-READY'
const SCROLLER = '.pane.active .scrollback-area'
const GRID = '.pane.active .terminal-live-inner'

test('primary history survives disabled recording and a renderer reload', async ({ page }) => {
  test.setTimeout(120_000)
  const root = mkdtempSync(join(tmpdir(), 'nocx-primary-history-'))
  const fixture = mkdtempSync(join(tmpdir(), 'nocx-primary-job-'))
  const script = join(fixture, 'primary.sh')
  const pidFile = join(fixture, 'pid')
  const finish = join(fixture, 'finish')
  writeFileSync(
    script,
    [
      `printf '%s\\n' "$$" > ${JSON.stringify(pidFile)}`,
      'i=1',
      `while [ "$i" -le ${ROWS} ]; do`,
      '  printf \'PRIMARY-TUI-%04d\\n\' "$i"',
      '  i=$((i+1))',
      'done',
      `printf '%s\\n' '${READY}'`,
      `while [ ! -f ${JSON.stringify(finish)} ]; do sleep 1; done`,
      '',
    ].join('\n'),
  )
  const backend = new VaultBackend(readStand().server, { root })
  let wire: ControlPlane | null = null
  try {
    const endpoint = await backend.start()
    wire = await openControlPlane(endpoint.port, endpoint.token)
    await wire.call('settings.set', { key: 'history.enabled', value: false })
    await wire.call('settings.set', { key: 'history.outputEnabled', value: false })
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await appReadyForInput(page)
    const input = page.locator('.pane.active .nocx-editor-input')
    await input.click()
    await page.keyboard.type(`sh ${JSON.stringify(script)}`)
    await page.keyboard.press('Enter')
    await expect(page.locator(GRID)).toContainText(READY)
    const pid = readFileSync(pidFile, 'utf8')

    await page.reload()
    await expect(page.locator(GRID)).toContainText(READY)
    expect(readFileSync(pidFile, 'utf8')).toBe(pid)
    await expect(page.locator('.pane.active .cmd-output .term-grid-row')).toHaveCount(0)
    const scroller = page.locator(SCROLLER)
    await expect.poll(() => scroller.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true)
    await page.locator(GRID).hover()
    await expect
      .poll(async () => {
        await page.mouse.wheel(0, -4000)
        return scroller.evaluate((el) => {
          const first = Array.from(el.querySelectorAll('.live-history .term-grid-row')).find(
            (row) => row.textContent?.includes('PRIMARY-TUI-0001'),
          )
          if (!first) return false
          const clip = el.getBoundingClientRect()
          const rect = first.getBoundingClientRect()
          return rect.bottom > clip.top && rect.top < clip.bottom
        })
      })
      .toBe(true)
    const labels = await scroller.evaluate((el) =>
      Array.from(
        el.querySelectorAll('.live-history .term-grid-row, .terminal-live-inner .term-grid-row'),
      ).flatMap((row) => row.textContent?.match(/PRIMARY-TUI-\d{4}/g) ?? []),
    )
    expect(labels).toEqual(
      Array.from({ length: ROWS }, (_, i) => `PRIMARY-TUI-${String(i + 1).padStart(4, '0')}`),
    )
    await page.mouse.wheel(0, 100_000)
    await expect
      .poll(() => scroller.evaluate((el) => el.scrollTop + el.clientHeight >= el.scrollHeight - 2))
      .toBe(true)
    await expect(page.locator(`${GRID} .term-grid-row`, { hasText: READY })).toBeInViewport()
  } finally {
    writeFileSync(finish, 'finish\n')
    wire?.close()
    backend.stop()
  }
})
