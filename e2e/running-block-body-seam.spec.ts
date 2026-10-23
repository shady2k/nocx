/**
 * A running block's own rows are ONE BODY, in ONE BOX (nocx-zg3k3.14.26,
 * ADR-0091).
 *
 * With shell integration on the normal screen every row of a running command's
 * output is inside its block: the block's surface runs unbroken from its header,
 * through its status and Stop, through the rows the durable record holds and the
 * rows still live, to its last row and its trailing space. A separate live grid
 * exists only where there are NO blocks — an alternate-screen program, or a
 * session with no shell integration — and both of those are asserted here too,
 * because they are the test of the rule rather than an exception to it.
 *
 * The owner, 2026-10-19: "Нет, так не должно быть. Нет никакой живой
 * поверхности, у нас блок. Вот если блоков нет (полноэкранный режим или без
 * интеграции) тогда да."
 *
 *
 * The owner saw this twice on the stand while a command ran, and described it
 * twice: the running block's background ended after one row of its output and
 * the rest was painted below it, with an extra half-row of space exactly at
 * that boundary; then, with plain `seq`, the same extra half-row at the same
 * place with no block edge in view.
 *
 * The rows come from two owners and always will: the block paints the rows the
 * durable record has already taken — the ones that left the screen — into its
 * own `.cmd-output`, and the controller's live region paints the screen the
 * command is still writing (ADR-0078's two tiers). What changed is WHERE the
 * live region sits: it is the running block's body, inside the block's own box,
 * not a sibling under it (ADR-0091, which supersedes the mockup decision's
 * sentence that fixed it as a sibling). The seam the owner saw was the first
 * half of that: each box wore the WHOLE body padding, so the joint between two
 * consecutive rows of one body carried both — 6px + 2px = 8px. Measured on this
 * spec's own scenario at the default pane, in that state:
 * `{"blockRows":30,"blockPitch":17,"blockTail":6,"jointGap":8,"jointStep":25}`.
 *
 * The scenario is the owner's own: sixty rows of `printf`, then a wait long
 * enough to look at them.
 */

import type { Page } from '@playwright/test'
import { test, expect, promptReady, clickIntoEditor } from './harness'

const SCENARIO =
  'w=$(tput cols); for i in $(seq 1 60); do printf \'%*s|\\n\' $((w-1)) "$i"; done; sleep 60'

interface Geometry {
  /** How many rows the block paints in its own body: the ones the durable
   *  record has taken, i.e. the rows that left the screen. */
  blockRows: number
  /** The pitch of those rows, from their own boxes. */
  blockPitch: number
  /** Space between the block's last own row and the block's own box end. */
  blockTail: number
  /** Space between the block's last own row and the live grid's first row. */
  jointGap: number
  /** The same joint as a distance between row TOPS: equal to `blockPitch`
   *  when the two boxes are one body at one pitch. */
  jointStep: number | null
  /** Both rows of the joint are inside the block's own box. */
  jointInBlock: boolean
  /** What the block pays below its last live row: the body's trailing space. */
  bodyTrailing: number
  /** The row either side of the joint, so a failure says which rows. */
  lastBlockRow: string
  firstLiveRow: string
  /** Computed line-height/height and viewport geometry for the two seam rows,
   *  including their containing elements up to the running block. */
  jointRows: {
    lastBlock: RowLayout | null
    firstLive: RowLayout | null
  }
  /** The pitch every painted row must use, read from the owning grid metric. */
  expectedPitch: number
  /** Every saved and live row has the shared terminal pitch. */
  rowsSharePitch: boolean
  rowsOffPitch: string[]
}

interface RowLayout {
  text: string
  lineHeight: string
  height: number
  top: number
  chain: Array<{ className: string; lineHeight: string; height: number; top: number }>
}

/** Read after the painter and the stack's observable glide completion. WebKit
 *  can advance an ancestor's compositor transform between rectangle reads in
 *  one callback, manufacturing a different row pitch from equal-height rows. */
async function geometry(page: Page): Promise<Geometry> {
  return page.evaluate(async () => {
    const pane = document.querySelector<HTMLElement>('.pane.active')!
    do {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
    } while ((pane.querySelector('.scrollback-inner')?.getAnimations().length ?? 0) > 0)
    const running = pane.querySelector<HTMLElement>('.cmd-block.cmd-block-running')
    const body = running?.querySelector<HTMLElement>(':scope > .cmd-output') ?? null
    const grid = pane.querySelector<HTMLElement>('.terminal-live-grid')
    // Not painted yet: the poll below waits for this state rather than for a
    // duration, so an absent block or grid is a zero, not a throw.
    if (!running || !grid) {
      return {
        blockRows: 0,
        blockPitch: 0,
        blockTail: 0,
        jointGap: 0,
        jointStep: null,
        jointInBlock: false,
        bodyTrailing: 0,
        lastBlockRow: '',
        firstLiveRow: '',
        jointRows: { lastBlock: null, firstLive: null },
        expectedPitch: 0,
        rowsSharePitch: true,
        rowsOffPitch: [],
      }
    }
    const bodyRows = [...(body?.querySelectorAll<HTMLElement>('.term-grid-row') ?? [])]
    const liveRows = [...grid.querySelectorAll<HTMLElement>('.term-grid-row')]
    const box = (el: Element): DOMRect => el.getBoundingClientRect()
    const text = (el: Element | undefined): string =>
      (el?.textContent ?? '').trim().replace(/\s+/g, ' ')
    const rowLayout = (row: HTMLElement | undefined): RowLayout | null => {
      if (!row) return null
      const chain: RowLayout['chain'] = []
      for (let el: HTMLElement | null = row; el; el = el.parentElement) {
        const rect = box(el)
        const style = getComputedStyle(el)
        chain.push({
          className: el.className.toString(),
          lineHeight: style.lineHeight,
          height: rect.height,
          top: rect.top,
        })
        if (el === running) break
      }
      const rect = box(row)
      const style = getComputedStyle(row)
      return {
        text: text(row),
        lineHeight: style.lineHeight,
        height: rect.height,
        top: rect.top,
        chain,
      }
    }
    const lastBody = bodyRows[bodyRows.length - 1]
    const firstLive = liveRows[0]
    const expectedPitch = parseFloat(
      getComputedStyle(running).getPropertyValue('--term-cell-height'),
    )
    const rowsOffPitch = [...bodyRows, ...liveRows].flatMap((row) => {
      const lineHeight = parseFloat(getComputedStyle(row).lineHeight)
      return Math.abs(lineHeight - expectedPitch) < 0.01
        ? []
        : [`${text(row)}: ${lineHeight}px != ${expectedPitch}px`]
    })
    return {
      blockRows: bodyRows.length,
      blockPitch: bodyRows.length > 1 ? box(bodyRows[1]).top - box(bodyRows[0]).top : 0,
      blockTail: lastBody ? +(box(running).bottom - box(lastBody).bottom).toFixed(2) : 0,
      jointGap: lastBody && firstLive ? +(box(firstLive).top - box(lastBody).bottom).toFixed(2) : 0,
      jointStep:
        lastBody && firstLive ? +(box(firstLive).top - box(lastBody).top).toFixed(2) : null,
      /** Both rows of the joint are inside the block's own box — the block's
       *  ground is the one surface they are painted on. */
      jointInBlock:
        lastBody !== undefined &&
        firstLive !== undefined &&
        box(lastBody).top >= box(running).top - 0.5 &&
        box(lastBody).bottom <= box(running).bottom + 0.5 &&
        box(firstLive).top >= box(running).top - 0.5 &&
        box(firstLive).bottom <= box(running).bottom + 0.5,
      /** What the block pays below its last row: the body's own trailing
       *  space, paid once, at the body's end. */
      bodyTrailing:
        liveRows.length > 0
          ? +(box(running).bottom - box(liveRows[liveRows.length - 1]).bottom).toFixed(2)
          : 0,
      lastBlockRow: text(lastBody),
      firstLiveRow: text(firstLive),
      jointRows: { lastBlock: rowLayout(lastBody), firstLive: rowLayout(firstLive) },
      expectedPitch,
      rowsSharePitch: rowsOffPitch.length === 0,
      rowsOffPitch: rowsOffPitch.slice(0, 8),
    }
  })
}

async function run(page: Page, command: string): Promise<void> {
  await clickIntoEditor(page)
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

test.describe('the body of a running block that has outgrown its screen', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await promptReady(page)
  })

  test('keeps one row pitch across the rows the record holds and the rows still live', async ({
    page,
  }) => {
    await run(page, SCENARIO)
    // The state this is about: the command has printed past the screen, so the
    // block paints rows of its own body and the live region paints the rest.
    // Waited on the state, never on a duration.
    await expect
      .poll(async () => (await geometry(page)).blockRows, { timeout: 20_000 })
      .toBeGreaterThan(0)
    const g = await geometry(page)
    const said = `running block body: ${JSON.stringify(g)}`
    // The rows either side of the joint are consecutive rows of ONE body in ONE
    // box: the same pitch as every other pair, no padding at the joint itself,
    // and both of them inside the block's own box (ADR-0091).
    expect(Math.abs(g.jointGap), said).toBeLessThan(0.5)
    expect(g.jointStep, said).not.toBeNull()
    expect(Math.abs(g.jointStep! - g.blockPitch), said).toBeLessThan(0.5)
    expect(g.rowsSharePitch, said).toBe(true)
    expect(g.jointInBlock, said).toBe(true)
    // And the body's trailing space is paid ONCE, at the body's end — the same
    // total a frozen block pays, so the pane does not move at the freeze.
    const trailing = await page.evaluate(() => {
      const cs = getComputedStyle(document.documentElement)
      const px = (name: string): number => parseFloat(cs.getPropertyValue(name))
      return px('--cmd-output-pad-bottom') + px('--terminal-block-trailing-space')
    })
    // Once, and at the end: at least the body's own trailing space and never a
    // second pad stacked onto it (a row's worth of slack is the cap rounding the
    // region mid-row, which is the region's own business).
    expect(g.bodyTrailing, `${said} trailing: ${trailing}`).toBeGreaterThanOrEqual(trailing - 0.5)
    expect(g.bodyTrailing, `${said} trailing: ${trailing}`).toBeLessThan(trailing + g.blockPitch)
    await page.keyboard.press('Control+C')
  })

  test('still pays the body top padding once when the block has no rows of its own yet', async ({
    page,
  }) => {
    // The paired half, so the fix cannot be "the live region never pads": a
    // command that has printed nothing past its own screen has its whole body
    // in the live region, and that body's first row sits below the header by
    // the body's own top padding — the same offset the frozen body it becomes
    // has (`.cmd-output`'s own pad, which is where the number lives).
    await run(page, "printf 'FIRST\\n'; sleep 60")
    await expect(page.locator('.pane.active .terminal-live-grid')).toContainText('FIRST', {
      timeout: 20_000,
    })
    const g = await geometry(page)
    expect(g.blockRows, `block rows: ${JSON.stringify(g)}`).toBe(0)
    const top = await page.evaluate(async () => {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const pane = document.querySelector<HTMLElement>('.pane.active')!
      const live = pane.querySelector<HTMLElement>('.terminal-live-container')!
      const grid = pane.querySelector<HTMLElement>('.terminal-live-grid')!
      // The first VISIBLE row: the echo row above the body's start is
      // translated out of the clip, so the first row a person sees is the
      // body's own first row.
      let clipTop = -Infinity
      for (let n: HTMLElement | null = grid.parentElement; n; n = n.parentElement) {
        if (getComputedStyle(n).overflowY !== 'visible') {
          clipTop = Math.max(clipTop, n.getBoundingClientRect().top)
        }
      }
      const first = [...grid.querySelectorAll<HTMLElement>('.term-grid-row')].find(
        (row) => row.getBoundingClientRect().top >= clipTop - 0.5,
      )
      return {
        padTop: parseFloat(getComputedStyle(live).paddingTop),
        token: parseFloat(
          getComputedStyle(document.documentElement).getPropertyValue('--cmd-output-pad-top'),
        ),
        offset: +(first!.getBoundingClientRect().top - live.getBoundingClientRect().top).toFixed(2),
      }
    })
    const said = `live body top: ${JSON.stringify(top)}`
    expect(top.padTop, said).toBe(top.token)
    expect(Math.abs(top.offset - top.padTop), said).toBeLessThan(0.5)
    await page.keyboard.press('Control+C')
  })

  test('keeps every row it has printed inside the block, past the first screenful (ADR-0091)', async ({
    page,
  }) => {
    await run(page, SCENARIO)
    // The painted state this is about, waited on and never timed: the command
    // has printed past the screen, so the block holds rows of its own AND the
    // live grid holds the newest ones. Row 45 is one the block did NOT paint
    // before this change (it painted 1..30 at the default pane, the live grid
    // had 31..60).
    await expect
      .poll(async () => (await geometry(page)).blockRows, { timeout: 20_000 })
      .toBeGreaterThan(0)
    const liveRow = page
      .locator('.pane.active .terminal-live-grid .term-grid-row')
      .filter({ hasText: /^\s*45\|\s*$/ })
      .first()
    await expect(liveRow).toBeVisible()
    const where = await page.evaluate(async () => {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const pane = document.querySelector<HTMLElement>('.pane.active')!
      const block = pane.querySelector<HTMLElement>('.cmd-block.cmd-block-running')!
      const header = block.querySelector<HTMLElement>('.cmd-header')!
      const live = [...pane.querySelectorAll<HTMLElement>('.terminal-live-grid .term-grid-row')]
      const printed = (el: HTMLElement): string => (el.textContent ?? '').trim()
      const target = live.find((row) => /^\s*45\|\s*$/.test(printed(row)))
      const last = [...block.querySelectorAll<HTMLElement>('.term-grid-row')].pop()
      const box = block.getBoundingClientRect()
      const rect = target?.getBoundingClientRect() ?? null
      return {
        found: target !== undefined,
        inBlockTree: target !== undefined && block.contains(target),
        // The box that paints the block's ground is the block itself, so "the
        // background covers the row" is this containment.
        insideBox:
          rect !== null &&
          rect.top >= box.top - 0.5 &&
          rect.bottom <= box.bottom + 0.5 &&
          rect.left >= box.left - 0.5 &&
          rect.right <= box.right + 0.5,
        boxTop: +box.top.toFixed(2),
        headerTop: +header.getBoundingClientRect().top.toFixed(2),
        boxBottom: +box.bottom.toFixed(2),
        lastRow: last ? printed(last) : null,
        lastRowBottom: last ? +last.getBoundingClientRect().bottom.toFixed(2) : null,
      }
    })
    const said = `row 45 in its block: ${JSON.stringify(where)}`
    // The row is in the block's own subtree, inside the box the block's
    // background paints ...
    expect(where.found, said).toBe(true)
    expect(where.inBlockTree, said).toBe(true)
    expect(where.insideBox, said).toBe(true)
    // ... and that box starts at the block's header and reaches its last
    // painted row: ONE surface, header to last row, no second box under it.
    expect(Math.abs(where.boxTop - where.headerTop), said).toBeLessThan(0.5)
    expect(where.lastRowBottom, said).not.toBeNull()
    expect(where.boxBottom, said).toBeGreaterThanOrEqual(where.lastRowBottom! - 0.5)
    // Hovering a LIVE row is hovering the block, which is the same fact said
    // the way a person meets it: the pointer over the newest output paints the
    // block's own hover ground, which a sibling surface could not reach.
    await liveRow.hover()
    const ground = await page.evaluate(
      () =>
        getComputedStyle(document.querySelector<HTMLElement>('.pane.active .cmd-block-running')!)
          .backgroundColor,
    )
    const alpha = (value: string): number => {
      const m = value.match(/rgba?\(([^)]+)\)/)
      if (!m) return 1
      const parts = m[1].split(/[,/]/).map((s) => s.trim())
      return parts.length >= 4 ? Number(parts[3]) : 1
    }
    expect(alpha(ground), `hover ground on the block: ${ground}`).toBeGreaterThan(0)
    await page.keyboard.press('Control+C')
  })

  test('keeps a separate live grid where there are no blocks: an alternate-screen program', async ({
    page,
  }) => {
    // The other half of ADR-0091. A program that takes the alternate screen
    // owns the pane, so the live grid must be the stack's child and belong to
    // no block at all — nesting it inside the (hidden) command block would take
    // the whole terminal down with it.
    await run(page, "printf '\\033[?1049h'; sleep 60")
    const live = page.locator('.pane.active .terminal-live-container')
    await expect(live).toHaveClass(/live-fullscreen/, { timeout: 20_000 })
    const grid = await page.evaluate(() => {
      const pane = document.querySelector<HTMLElement>('.pane.active')!
      const container = pane.querySelector<HTMLElement>('.terminal-live-container')!
      const grid = pane.querySelector<HTMLElement>('.terminal-live-grid')
      return {
        inBlock: grid?.closest('.cmd-block') !== null,
        parentIsStack: container.parentElement?.classList.contains('scrollback-inner') === true,
        visible: grid !== null && grid.getBoundingClientRect().height > 0,
      }
    })
    const said = `alternate-screen live grid: ${JSON.stringify(grid)}`
    expect(grid.inBlock, said).toBe(false)
    expect(grid.parentIsStack, said).toBe(true)
    expect(grid.visible, said).toBe(true)
    await page.keyboard.press('Control+C')
  })
})
