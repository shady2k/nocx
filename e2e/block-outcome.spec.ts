/**
 * A person scanning the ledger sees the failure and not the successes
 * (nocx-9bpeq.6, spec 2026-09-14 §3). Watched through the product: a real shell
 * runs `true` and `false`; every theme is applied the way Settings applies it
 * (the data-theme attribute on the root); contrast is read off computed styles.
 * No wait is on a duration.
 */
import { test, expect, promptReady } from './harness'
import type { Page } from './harness'

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
]

/** The active pane's command blocks, in DOM order. A POSITION in this list is
 *  how the block under test is found, never how it is addressed — see the
 *  identity pin in the test (nocx-zg3k3.14.25). */
const COMMAND_BLOCKS = '.pane.active .scrollback-inner > .cmd-block[data-block-kind="command"]'
/** The settled status word: the header's own meta that is not the duration. */
const STATUS = ':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])'
/** The body a settled command's stored rows are painted into. */
const OUTPUT = ':scope > .cmd-output'

async function run(page: Page, command: string) {
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(0, { timeout: 30_000 })
  await promptReady(page)
}

test('success is silent, failure is legible, in every theme', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  await run(page, 'true')
  // It prints before failing: the failure rail runs beside a command's
  // OUTPUT, so a failure with nothing to show carries only its Exit word.
  await run(page, 'echo failing; false')

  const blocks = page.locator(COMMAND_BLOCKS)
  // `nth(-1)` is documented as the last match; a second negative index is not,
  // so the one before it is addressed by position instead.
  const count = await blocks.count()
  const ok = blocks.nth(count - 2)
  const bad = blocks.nth(-1)
  const statusOf = (b: typeof bad) => b.locator(STATUS)

  await expect(ok).toHaveAttribute('data-outcome', 'success')
  await expect(statusOf(ok)).toHaveCount(0)
  await expect(bad).toHaveAttribute('data-outcome', 'failure')
  await expect(statusOf(bad)).toHaveText('Exit 1')

  // BY ITS OWN IDENTITY from here (nocx-zg3k3.14.25). A position finds the
  // block; it does not name it, and the list it indexes is one the renderer
  // replaces elements in — `freezeBlock` swaps a settled block's element into
  // the DOM, `_freezeCard` and `paintStoredRows` go on mutating the block after
  // that, and this test reads thirteen themes while all of it is in flight. The
  // entry id the block carries is what the test means, so every read below
  // names it and re-resolves to whatever element carries it now.
  const entryId = await bad.getAttribute('data-entry-id')
  expect(entryId, 'the failure block carries no data-entry-id to address it by').not.toBeNull()
  const failing = page.locator(`${COMMAND_BLOCKS}[data-entry-id="${entryId}"]`)
  await expect(failing).toHaveCount(1)

  for (const theme of THEMES) {
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme)
    // A SETTLED BLOCK HAS NO BODY YET, and the gap is one ledger read wide
    // (nocx-zg3k3.14.25). The header — status word, duration, `data-outcome` —
    // is painted from the lifecycle fact that closes the block, and that fact
    // only STARTS the rows read: `onBlockClosed` (terminal-content.ts) asks for
    // the final read and then calls `blockClosed`, which freezes the block on
    // screen; `_freezeCard` builds the element with an empty body (`freezeBlock`
    // takes `outputHtml` and it is always `''` now); the `.cmd-output` a stored
    // block shows is appended by `paintStoredRows` when that read answers, one
    // or two RPC round trips later. So `data-outcome="failure"` and its `Exit 1`
    // are on screen BEFORE the body is, and a read of the body must wait for the
    // body. Both waits are on the block's own state and neither is a duration.
    await expect(statusOf(failing)).toHaveText('Exit 1')
    await expect(failing.locator(OUTPUT)).toHaveCount(1)
    const measured = await failing.evaluate((block, theme) => {
      const parse = (c: string) => (c.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number)
      const lum = ([r, g, b]: number[]) => {
        const f = (v: number) => {
          const s = v / 255
          return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
        }
        return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
      }
      // ASSERTED, NOT ASSERTED AWAY (nocx-zg3k3.14.25). Both of these were
      // non-null assertions, and a missing element then arrived at
      // getComputedStyle as "Argument 1 ('element') to Window.getComputedStyle
      // must be an instance of Element" — a type error standing where the fact
      // was "this block shows no body", which is why that failure cost a CI run
      // to read. What is missing is named here, with the block's own identity
      // and the children it does have, so the next occurrence is one line.
      const status = block.querySelector(
        ':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])',
      )
      const output = block.querySelector(':scope > .cmd-output')
      if (status === null || output === null) {
        const children = Array.from(block.children).map((child) => {
          const classes = Array.from(child.classList).join('.')
          const tag = child.tagName.toLowerCase()
          return classes === '' ? tag : `${tag}.${classes}`
        })
        const missing = [
          ...(status === null
            ? ['status (:scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column]))']
            : []),
          ...(output === null ? ['body (:scope > .cmd-output)'] : []),
        ]
        throw new Error(
          `the ${theme} read found no ${missing.join(' and no ')} on block ` +
            `entry-id=${block.getAttribute('data-entry-id') ?? '(none)'} ` +
            `data-outcome=${block.getAttribute('data-outcome') ?? '(none)'}; ` +
            `its children are ${children.join(', ') || '(none)'}`,
        )
      }
      const fg = lum(parse(getComputedStyle(status).color))
      // The row paints no ground of its own now (the failure is the rail), so
      // the status sits on the nearest ancestor that paints one.
      let ground: Element | null = block
      while (
        ground &&
        /rgba\(0, 0, 0, 0\)|transparent/.test(getComputedStyle(ground).backgroundColor)
      )
        ground = ground.parentElement
      const bg = lum(parse(getComputedStyle(ground ?? document.body).backgroundColor))
      // The failure rail runs beside the OUTPUT only (reference pass, owner
      // review 2026-09-15), so it is the output's pseudo-element, not the row's.
      const bar = getComputedStyle(output, '::before')
      return {
        ratio: (Math.max(fg, bg) + 0.05) / (Math.min(fg, bg) + 0.05),
        barWidth: bar.width,
        barColor: bar.backgroundColor,
        dangerSurfaceSet:
          getComputedStyle(block).getPropertyValue('--color-danger-surface').trim() !== '',
      }
    }, theme)
    expect(measured.dangerSurfaceSet, theme).toBe(true)
    expect(measured.ratio, theme).toBeGreaterThanOrEqual(4.5)
    // Not by colour alone: the rail is drawn.
    expect(measured.barWidth, theme).toBe('4px')
  }
})

test('the quiet ⋮ is still reachable from the keyboard', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  await run(page, 'true')
  const block = page.locator(COMMAND_BLOCKS).last()
  // run() can observe an intermediate prompt before the command finishes.
  // The visual freeze replaces the running block, including its ⋮. Wait for
  // this command's settled element before sending the keyboard there.
  await expect(block).toHaveAttribute('data-outcome', 'success')
  const entryId = await block.getAttribute('data-entry-id')
  expect(entryId, 'the settled command carries no entry identity').not.toBeNull()
  const settled = page.locator(`${COMMAND_BLOCKS}[data-entry-id="${entryId}"]`)
  const dots = settled.locator(':scope > .cmd-header .cmd-header-right > .ui-icon-button')
  await page.mouse.move(0, 0)
  await expect(dots).toHaveCSS('opacity', '0')
  const focusedElement = await dots.elementHandle()
  expect(focusedElement, 'the settled block has no actions button').not.toBeNull()
  try {
    await focusedElement!.focus()
    await expect
      .poll(
        () => focusedElement!.evaluate((el) => el.isConnected && document.activeElement === el),
        { message: 'the original settled block actions element must keep focus' },
      )
      .toBe(true)
    await expect(dots).toBeFocused()
    await expect(dots).toHaveCSS('opacity', '1')
  } finally {
    // A locator re-resolves after replacement. It must still name the node
    // that received focus, even if a replacement itself happens to be focused.
    expect(
      await dots.evaluate((current, original) => current === original, focusedElement),
      'the settled block actions element was replaced after focus',
    ).toBe(true)
    await focusedElement?.dispose()
  }
})
