// The terminal screen speaks the kit, in every theme (nocx-9bpeq, spec
// .internal/specs/2026-09-14-terminal-screen-visual-register-design.md).
//
// COMMITTED RED (nocx-9bpeq.9). Six tests below are marked `test.fail()` and name
// the child whose work turns them green. The child that makes one pass deletes
// its marker in the same commit; if it forgets, Playwright reports "Expected to
// fail, but passed" and the merged-tree gate says which one.
//
// The first test is NOT marked. It proves the probes the other six stand on, so
// a red marked test cannot be a broken harness wearing an expected failure.
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { openControlPlane, promptReady, test, expect, type Page } from './harness'
import { readStand } from './stand'

const REPO = path.resolve(__dirname, '..')
const INPUT = '.pane.active .nocx-editor-input'
const SETTLED = '.pane.active .cmd-block:not(.cmd-block-running)'
const COMPOSER = '.pane.active .nocx-editor'
const COMPOSER_CHROME = '.pane.active .nocx-editor-chrome'
const LIVE = '.pane.active .xterm-live-container'
const THEME_SELECT = '.ui-settings-row[data-key="ui.theme"] select'
// The names later tasks produce (spec §3.1, §3.3). One line each, so a rename is one edit.
const FAILED_ROW = '.cmd-block[data-outcome="failure"]'
const STATUS = '.cmd-header .ui-meta[data-tone="danger"]'
const OK = 'true #t9-ok'
// Prints before failing, so the failed block has output for its rail to run beside.
const FAIL = 'echo failing; false #t9-fail'

/** A mirror of KNOWN_THEME_IDS (frontend/src/renderers/theme-bootstrap.ts:48).
 *  Mirrored rather than imported: importing renderer source would pull the
 *  frontend's module graph into Playwright's Node process (harness.ts says why).
 *  The sanity test reads the source and fails if the two drift. */
const THEMES = [
  'graphite',
  'tokyo-night',
  'light',
  'ayu-dark',
  'catppuccin-latte',
  'catppuccin-mocha',
  'dracula',
  'gruvbox-dark',
  'nord',
  'one-dark',
  'rose-pine',
  'solarized-dark',
  'solarized-light',
] as const

type RGB = [number, number, number]
type Probes = {
  ground(selector: string): RGB
  tokenColour(name: string): RGB
  textContrast(selector: string): number
  contrastOf(fg: string, bg: string): number
}

/** Installed before the app loads. Colours are resolved by PAINTING them on a
 *  1×1 canvas rather than by parsing computed strings: engines serialise
 *  color-mix() differently, and the canvas is the one parser both agree on. */
function installProbes(): void {
  const canvas = document.createElement('canvas')
  canvas.width = 1
  canvas.height = 1
  const ctx = canvas.getContext('2d', { willReadFrequently: true })!
  const SENTINEL = 'rgba(1, 2, 3, 0)'
  const paint = (layers: readonly string[]): [number, number, number] => {
    ctx.globalCompositeOperation = 'copy'
    ctx.fillStyle = '#ffffff'
    ctx.fillRect(0, 0, 1, 1)
    ctx.globalCompositeOperation = 'source-over'
    for (const layer of layers) {
      ctx.fillStyle = SENTINEL
      ctx.fillStyle = layer
      if (ctx.fillStyle === SENTINEL) continue // unparseable: the engine kept the sentinel
      ctx.fillRect(0, 0, 1, 1)
    }
    const d = ctx.getImageData(0, 0, 1, 1).data
    return [d[0], d[1], d[2]]
  }
  const layersOf = (el: Element): string[] => {
    const out: string[] = []
    for (let n: Element | null = el; n; n = n.parentElement) {
      out.unshift(getComputedStyle(n).backgroundColor)
    }
    return out
  }
  const channel = (v: number): number => {
    const s = v / 255
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  const luminance = ([r, g, b]: [number, number, number]): number =>
    0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
  const contrast = (a: [number, number, number], b: [number, number, number]): number => {
    const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
    return (hi + 0.05) / (lo + 0.05)
  }
  const one = (selector: string): Element => {
    const el = document.querySelector(selector)
    if (!el) throw new Error(`probe: nothing matches ${selector}`)
    return el
  }
  const probes = {
    ground: (selector: string) => paint(layersOf(one(selector))),
    tokenColour: (name: string) =>
      paint([getComputedStyle(document.documentElement).getPropertyValue(name).trim()]),
    textContrast: (selector: string) => {
      const el = one(selector)
      const layers = layersOf(el)
      return contrast(paint([...layers, getComputedStyle(el).color]), paint(layers))
    },
    contrastOf: (fg: string, bg: string) => contrast(paint([bg, fg]), paint([bg])),
  }
  ;(window as unknown as { __t9: typeof probes }).__t9 = probes
}

const probe = <K extends keyof Probes>(page: Page, name: K, ...args: Parameters<Probes[K]>) =>
  page.evaluate(
    ([n, a]) => {
      const p = (window as unknown as { __t9: Record<string, (...x: unknown[]) => unknown> }).__t9
      return p[n as string](...(a as unknown[]))
    },
    [name, args] as const,
  ) as Promise<ReturnType<Probes[K]>>

const near = (a: RGB, b: RGB): boolean => a.every((v, i) => Math.abs(v - b[i]) <= 1)

async function setTheme(page: Page, id: string): Promise<void> {
  if ((await page.evaluate(() => document.documentElement.getAttribute('data-theme'))) === id)
    return
  await page.keyboard.press('Meta+,')
  await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
  await expect(page.locator(THEME_SELECT)).toBeVisible()
  await page.selectOption(THEME_SELECT, id)
  await page.waitForFunction((t) => document.documentElement.getAttribute('data-theme') === t, id)
  await page.keyboard.press('Meta+w')
  await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('')
  // The Settings click leaves the pointer wherever the nav item was, which is over
  // the terminal once Settings closes — and a hovered row takes the hover tint as
  // its ground (spec §3.3). Park it on the tab strip again, as twoBlocks does.
  await page.mouse.move(1, 1)
}

/** Open the app and leave one successful and one failed block settled above an idle composer. */
async function twoBlocks(page: Page): Promise<void> {
  await page.addInitScript(installProbes)
  await page.goto('/')
  await promptReady(page)
  for (const command of [OK, FAIL]) {
    await page.locator(INPUT).fill(command)
    await page.keyboard.press('Enter')
    await expect(page.locator(SETTLED, { hasText: command })).toHaveCount(1, { timeout: 15_000 })
    await promptReady(page)
  }
  // Hover tints a row; the pointer rests on the tab strip, not on a block.
  await page.mouse.move(1, 1)
}

function kitIdentities(): Set<string> {
  const out = execFileSync(
    process.execPath,
    [
      '--input-type=module',
      '-e',
      "import { scanKitIdentities } from './frontend/lint-fixtures/scan-kit-identities.mjs';" +
        "process.stdout.write(JSON.stringify([...scanKitIdentities('frontend/src/ui').byClass.keys()]))",
    ],
    { cwd: REPO, encoding: 'utf8' },
  )
  return new Set(JSON.parse(out) as string[])
}

test.afterEach(async () => {
  // Settings are not part of resetStand, and theme-switch.spec.ts asserts Tokyo
  // Night on entry. Over the wire, so a broken page cannot skip it.
  const stand = readStand()
  const wire = await openControlPlane(stand.port, stand.token)
  try {
    await wire.call('settings.set', { key: 'ui.theme', value: 'tokyo-night' })
  } finally {
    wire.close()
  }
})

test('the probes this spec stands on measure what they claim', async ({ page }) => {
  await twoBlocks(page)

  expect(await probe(page, 'contrastOf', '#000000', '#ffffff')).toBeCloseTo(21, 1)
  expect(await probe(page, 'contrastOf', '#777777', '#777777')).toBeCloseTo(1, 5)

  // Half-transparent black over whatever the body paints: compositing, not parsing.
  const body = await probe(page, 'ground', 'body')
  await page.evaluate(() => {
    const d = document.createElement('div')
    d.id = 't9-half'
    d.style.background = 'rgba(0, 0, 0, 0.5)'
    document.body.append(d)
  })
  const half = await probe(page, 'ground', '#t9-half')
  expect(near(half, body.map((v) => Math.round(v * 0.5)) as RGB)).toBe(true)

  // The mirror has not drifted from the source.
  const source = readFileSync(path.join(REPO, 'frontend/src/renderers/theme-bootstrap.ts'), 'utf8')
  const declared = [
    ...(source.match(/KNOWN_THEME_IDS[^[]*\[([^\]]*)\]/)?.[1] ?? '').matchAll(/'([^']+)'/g),
  ].map((m) => m[1])
  expect(declared).toEqual([...THEMES])

  // The theme path changes a resolved token, not only the attribute.
  await setTheme(page, 'dracula')
  expect(await probe(page, 'tokenColour', '--terminal-background')).toEqual([40, 42, 54])

  // The identity scan found the kit.
  expect(kitIdentities().has('ui-button')).toBe(true)
})

test('a successful command shows a check without an extra status word', async ({ page }) => {
  await twoBlocks(page)
  const header = page.locator(SETTLED, { hasText: OK }).locator('.cmd-header')
  await expect(header.locator('[data-tone="danger"], [data-tone="dim"]')).toHaveCount(0)
  await expect(header.locator('[aria-label="Succeeded"]')).toBeVisible()
  const words = await header.evaluate((h) =>
    [...h.querySelectorAll('*')]
      .filter((el) => el.children.length === 0)
      .map((el) => el.textContent?.trim() ?? ''),
  )
  expect(words.filter((w) => /^(ok|Exit \d+|completed)$/.test(w))).toEqual([])
})

test('a failed command is marked, and its status is legible in every theme', async ({ page }) => {
  test.setTimeout(120_000)
  await twoBlocks(page)
  const row = page.locator(SETTLED, { hasText: FAIL })
  await expect(row).toHaveAttribute('data-outcome', 'failure')
  for (const theme of THEMES) {
    await setTheme(page, theme)
    const status = `${FAILED_ROW} ${STATUS}`
    await expect(page.locator(status)).toHaveText(/Exit 1/)
    const ratio = await probe(page, 'textContrast', status)
    expect(ratio, `${theme}: status text contrast`).toBeGreaterThanOrEqual(4.5)
    // The row keeps the terminal's ground; the failure is the rail beside its
    // output (reference pass, owner review 2026-09-15).
    const ground = await probe(page, 'ground', FAILED_ROW)
    const terminal = await probe(page, 'tokenColour', '--terminal-background')
    expect(near(ground, terminal), `${theme}: failed row keeps the terminal ground`).toBe(true)
    const rail = await page
      .locator(`${FAILED_ROW} > .cmd-output`)
      .evaluate((el) => getComputedStyle(el, '::before').width)
    expect(rail, `${theme}: failure rail is drawn`).toBe('4px')
  }
})

test('no emoji and no glyph stands in for an icon on the terminal screen', async ({ page }) => {
  await twoBlocks(page)
  const offenders = await page.evaluate(
    ([headers, chrome]) => {
      const bad = /[\p{Extended_Pictographic}⋮×✕⚠]/u
      const found: string[] = []
      for (const root of document.querySelectorAll(`${headers}, ${chrome}`)) {
        const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
        for (let n = walk.nextNode(); n; n = walk.nextNode()) {
          if (bad.test(n.textContent ?? '')) found.push(n.textContent ?? '')
        }
      }
      return found
    },
    ['.pane.active .cmd-header', COMPOSER_CHROME] as const,
  )
  expect(offenders).toEqual([])
})

test('the composer shows no clock', async ({ page }) => {
  await twoBlocks(page)
  await expect(page.locator(`${COMPOSER} .nocx-editor-time`)).toHaveCount(0)
  await expect(page.locator(COMPOSER_CHROME)).not.toHaveText(/\d{1,2}:\d{2}/)
})

test('every control in a block header and the composer is a kit component', async ({ page }) => {
  await twoBlocks(page)
  const identities = kitIdentities()
  const controls = await page.evaluate(
    ([scope]) =>
      [...document.querySelectorAll(scope)]
        .filter((el) => !(el as HTMLElement).isContentEditable)
        .map((el) => ({ html: el.outerHTML.slice(0, 120), classes: [...el.classList] })),
    [
      ['.pane.active .cmd-header', COMPOSER]
        .flatMap((s) =>
          [
            'button',
            'input',
            'select',
            'textarea',
            '[role="button"]',
            '[tabindex]:not([tabindex="-1"])',
          ].map((c) => `${s} ${c}`),
        )
        .join(', '),
    ] as const,
  )
  expect(controls.length).toBeGreaterThan(0)
  const unkitted = controls.filter((c) => !c.classes.some((k) => identities.has(k)))
  expect(unkitted.map((c) => c.html)).toEqual([])
})

test('history, live terminal and composer stand on one ground in every theme', async ({ page }) => {
  test.setTimeout(120_000)
  await twoBlocks(page)
  const okRow = `${SETTLED}:not([data-outcome="failure"])`
  for (const theme of THEMES) {
    await setTheme(page, theme)
    const ground = await probe(page, 'tokenColour', '--terminal-background')
    expect(near(await probe(page, 'ground', okRow), ground), `${theme}: block row`).toBe(true)
    // The composer is a card on the terminal-chrome role, set apart from the
    // history ground (reference pass, owner review 2026-09-15).
    const card = await probe(page, 'tokenColour', '--color-terminal-chrome')
    expect(
      near(await probe(page, 'ground', `${COMPOSER} .ui-composer-frame`), card),
      `${theme}: composer card`,
    ).toBe(true)
  }
  // A builtin that waits: the live region exists only while something runs, and
  // `sleep` is not on every stand's PATH (pets.spec.ts).
  await page.locator(INPUT).fill('read -r _')
  await page.keyboard.press('Enter')
  await expect(page.locator(LIVE)).toBeVisible()
  for (const theme of THEMES) {
    await setTheme(page, theme)
    const ground = await probe(page, 'tokenColour', '--terminal-background')
    expect(near(await probe(page, 'ground', LIVE), ground), `${theme}: live region`).toBe(true)
  }
  await page.locator(LIVE).click()
  await page.keyboard.press('Control+c')
  await promptReady(page)
})
