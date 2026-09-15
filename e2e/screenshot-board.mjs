// The owner's review board for nocx-9bpeq: one scripted session, three themes,
// one image. NOT a test — it asserts nothing and check-coverage.mjs does not
// collect it. Run through e2e/screenshot-board.sh.
//
// It drives the DEV-WEB stand (make dev-web, 127.0.0.1:5180), not the e2e stand:
// the e2e stand exists only inside a Playwright run (e2e/stand.ts), and a second
// launcher for it is the drift that file was written to end. The dev-web stand
// is somebody's own session, so this script opens its OWN tab and closes it,
// and switches themes with the attribute only — the persisted ui.theme setting
// is theirs and is never written.
// The callbacks handed to page.evaluate / waitForFunction run in the page, not in
// Node, so these two browser globals are real there.
/* global document, getComputedStyle */
import { chromium } from 'playwright'
import { mkdirSync, writeFileSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

const BASE = process.env.BOARD_URL ?? 'http://127.0.0.1:5180/'
const OUT = process.env.BOARD_OUT ?? '/out'
const THEMES = ['tokyo-night', 'light', 'solarized-light']
const SESSION = ['true #board-ok', 'false #board-fail', "printf '%s\\n' one two three", 'ls /']

mkdirSync(OUT, { recursive: true })
const browser = await chromium.launch()
try {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 2,
  })
  await page.goto(BASE)
  await page.waitForSelector('.pane.active .nocx-editor-input', { timeout: 30_000 })
  const before = await page.locator('.nocx-tab').count()
  await page.keyboard.press('Meta+t')
  await page.waitForFunction((n) => document.querySelectorAll('.nocx-tab').length === n + 1, before)
  await page.waitForSelector('.pane.active .nocx-editor-input')
  for (const command of SESSION) {
    await page.locator('.pane.active .nocx-editor-input').fill(command)
    await page.keyboard.press('Enter')
    await page
      .locator('.pane.active .cmd-block:not(.cmd-block-running)', { hasText: command })
      .waitFor({ timeout: 15_000 })
    await page.waitForSelector('.pane.active .nocx-editor-input')
  }
  await page.mouse.move(1, 1)
  const shots = []
  for (const theme of THEMES) {
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme)
    await page.waitForFunction(
      () =>
        getComputedStyle(document.documentElement).getPropertyValue('--terminal-background') !== '',
    )
    const file = join(OUT, `${theme}.png`)
    await page.screenshot({ path: file })
    shots.push({ theme, file })
  }
  await page.keyboard.press('Meta+w') // close the tab this script opened
  const board = await browser.newPage({ viewport: { width: 3 * 800 + 80, height: 620 } })
  const cells = shots
    .map(
      ({ theme, file }) =>
        `<figure><img src="data:image/png;base64,${readFileSync(file).toString('base64')}"><figcaption>${theme}</figcaption></figure>`,
    )
    .join('')
  await board.setContent(
    `<style>body{margin:0;padding:20px;display:flex;gap:20px;background:#888;font:14px system-ui}figure{margin:0;width:800px}img{width:800px;display:block}figcaption{padding:6px 0;color:#fff}</style>${cells}`,
  )
  await board.screenshot({ path: join(OUT, 'board.png'), fullPage: true })
  writeFileSync(
    join(OUT, 'README.txt'),
    `nocx-9bpeq screenshot board\nsource: ${BASE}\nthemes: ${THEMES.join(', ')}\n`,
  )
  console.log(`board written to ${OUT}`)
} finally {
  await browser.close()
}
