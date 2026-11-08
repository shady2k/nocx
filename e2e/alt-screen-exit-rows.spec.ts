import { test, expect, promptReady, resolveBackend } from './harness'
import { readStand } from './stand'
import { startSshd, rpc } from './sshd-fixture'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

// A FULL-SCREEN PROGRAM MUST NOT LEAVE THE PANE ROWS IT CANNOT SHOW, AND ITS
// EXIT MUST NOT BE THE RESIZE (nocx-djx1u).
//
// Leaving the alternate screen sends no resize: a resize there is the SIGWINCH
// that starts the next borrow (ADR-2yzsq8mk decision 4). So the pty keeps the
// grid the program was given — its ROWS included — and the four-side margin
// (decision 5) is two rows shorter than the box that program had. A plain ssh
// shell has no OSC 133, so no authenticated prompt ever comes to hand the grid
// back; the rows that used to fall off the bottom were the newest ones, the
// prompt included.
//
// The owner's answer, and this is what the spec proves in order:
//
//   1. until something hands the grid back, the pane IS the program's box — rows
//      all visible, no vertical margin — because the alternative is clipping;
//   2. the hand-back moment is the person's FIRST KEY: the shell is what reads a
//      key, so that key is the prompt a markerless session can observe, and the
//      exit itself stays untouched;
//   3. at that key the ledger's column and its four margins return, with exactly
//      ONE resize — never one the program's own exit caused, and never two.

const GRID = '.pane.active .terminal-live-grid'
const ROW = '.term-grid-row'
const LAYOUT = '.pane.active .scrollback-layout'

interface Frame {
  /** The pane's visible box: the scroller the grid is drawn in. */
  area: { top: number; bottom: number }
  /** The grid's own rectangle, and the rows painted inside it. */
  grid: { top: number; bottom: number; rows: number }
  painted: { text: string; top: number; bottom: number }[]
  /** True when the grid box is inside the pane's box: nothing is cut. */
  fits: boolean
}

interface Layout {
  /** The layout class that gives the vertical margin up for a borrowed grid. */
  borrowed: boolean
  /** The margin the layout is paying right now, in CSS pixels. */
  paddingTop: number
  paddingBottom: number
  /** How many resize RPCs this page has sent since it loaded. */
  resizes: number
}

async function frame(page: import('@playwright/test').Page): Promise<Frame | null> {
  return page.evaluate(
    ({ gridSelector, rowSelector }) => {
      const area = document.querySelector('.pane.active .scrollback-area')
      const grid = document.querySelector(gridSelector)
      if (!(area instanceof HTMLElement) || !(grid instanceof HTMLElement)) return null
      const box = area.getBoundingClientRect()
      const g = grid.getBoundingClientRect()
      const painted = Array.from(grid.querySelectorAll(rowSelector)).map((row) => {
        const r = row.getBoundingClientRect()
        return { text: (row.textContent ?? '').trim(), top: r.top, bottom: r.bottom }
      })
      return {
        area: { top: box.top, bottom: box.bottom },
        grid: { top: g.top, bottom: g.bottom, rows: painted.length },
        painted,
        fits: g.top >= box.top - 1 && g.bottom <= box.bottom + 1,
      }
    },
    { gridSelector: GRID, rowSelector: ROW },
  )
}

async function layout(page: import('@playwright/test').Page): Promise<Layout | null> {
  return page.evaluate((layoutSelector) => {
    const el = document.querySelector(layoutSelector)
    if (!(el instanceof HTMLElement)) return null
    const style = getComputedStyle(el)
    const counter = (window as unknown as { __nocxResizes?: unknown[] }).__nocxResizes
    return {
      borrowed: el.classList.contains('inner-borrowed-grid'),
      paddingTop: parseFloat(style.paddingTop) || 0,
      paddingBottom: parseFloat(style.paddingBottom) || 0,
      resizes: Array.isArray(counter) ? counter.length : -1,
    }
  }, LAYOUT)
}

test('a borrowed grid is shown whole, and one key gives the pane its margins back (nocx-djx1u)', async ({
  page,
}) => {
  test.setTimeout(240_000)
  const remoteHome = mkdtempSync(path.join(tmpdir(), 'alt-exit-remote-'))
  const remoteCwd = mkdtempSync(path.join(tmpdir(), 'alt-exit-cwd-'))
  const fixture = await startSshd({ home: remoteHome, cwd: remoteCwd })
  let createdId: string | null = null
  try {
    // Every resize this page SENDS is counted, in the browser, so "exactly one"
    // is a fact about the wire and not about what the pane drew.
    await page.addInitScript(() => {
      const counter: string[] = []
      ;(window as unknown as { __nocxResizes?: unknown[] }).__nocxResizes = counter
      const Native = window.WebSocket
      window.WebSocket = class extends Native {
        override send(data: Parameters<WebSocket['send']>[0]): void {
          if (typeof data === 'string' && data.includes('"method":"resize"')) counter.push(data)
          super.send(data)
        }
      }
    })
    const sshDir = path.join(readStand().home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), fixture.knownHosts + '\n')
    await page.setViewportSize({ width: 1280, height: 800 })
    await page.goto('/')
    await promptReady(page)
    const wsInfo = await resolveBackend(page)
    const profileName = `alt-exit-${Date.now()}`
    const created = await rpc<{ id: string }>(page, wsInfo, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: fixture.user,
        keyPath: fixture.userKey,
        // No integration at all: a conventional shell, which is the mode with
        // no prompt marker to hand a borrowed grid back.
        desiredMode: 'raw',
      },
    })
    createdId = created.id
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible()
    await search.fill(profileName)
    await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
      timeout: 10_000,
    })
    await page.keyboard.press('Enter')

    // The shell must have drawn before the person speaks: the first painted row
    // is the only honest readiness signal a markerless pane has.
    await expect
      .poll(
        async () =>
          page.evaluate(
            () =>
              document
                .querySelector('.pane.active .terminal-live-container')
                ?.classList.contains('live-unstructured') ?? false,
          ),
        { timeout: 30_000 },
      )
      .toBe(true)
    await expect
      .poll(
        async () =>
          page.evaluate(() => {
            const rows = Array.from(document.querySelectorAll('.pane.active .term-grid-row'))
            return rows.some((row) => (row.textContent ?? '').trim() !== '')
          }),
        { timeout: 30_000 },
      )
      .toBe(true)
    await page.locator('.pane.active .terminal-live-container').click()

    // ONE COMMAND, because the shell is markerless: enter the alternate screen,
    // hold it long enough for the switch's resize to apply and settle, leave it,
    // then print a screenful on the primary buffer.
    await page.keyboard.type(
      "printf '\\033[?1049h\\033[H'; sleep 0.8; printf '\\033[?1049l'; " +
        'i=1; while [ $i -le 40 ]; do echo EXITROW-$i; i=$((i+1)); done',
    )
    await page.keyboard.press('Enter')

    // 1. THE SCREENFUL IS WHOLE. The last row is the witness: it is the newest
    //    row, and the one a two-row-short box used to cut.
    await expect
      .poll(
        async () => {
          const f = await frame(page)
          return f?.painted.some((row) => row.text === 'EXITROW-40') ?? false
        },
        { timeout: 40_000 },
      )
      .toBe(true)
    const before = await frame(page)
    const beforeLayout = await layout(page)
    const spoken = `borrowed: ${JSON.stringify(before)} ${JSON.stringify(beforeLayout)}`
    expect(before, spoken).not.toBeNull()
    expect(beforeLayout, spoken).not.toBeNull()
    // NOTHING IS CUT: the grid's box is the pane's box…
    expect(before!.fits, spoken).toBe(true)
    const newest = before!.painted.find((row) => row.text === 'EXITROW-40')
    expect(newest, spoken).toBeDefined()
    expect(newest!.bottom, spoken).toBeLessThanOrEqual(before!.area.bottom + 1)
    // …and that is the program's own box: the pane has given up the vertical
    // margin to hold those rows, which is the state the exit leaves behind.
    expect(beforeLayout!.borrowed, spoken).toBe(true)
    expect(beforeLayout!.paddingTop, spoken).toBe(0)
    expect(beforeLayout!.paddingBottom, spoken).toBe(0)

    // 2. THE FIRST KEY IS THE PROMPT A MARKERLESS SESSION HAS. One character at
    //    the shell; it is read, and it is what hands the grid back.
    await page.keyboard.type('x')
    await expect.poll(async () => (await layout(page))?.borrowed, { timeout: 20_000 }).toBe(false)

    // 3. THE MARGINS ARE BACK, and the rows are the ordinary box's.
    const after = await frame(page)
    const afterLayout = await layout(page)
    const spokenAfter = `released: ${JSON.stringify(after)} ${JSON.stringify(afterLayout)}`
    expect(after, spokenAfter).not.toBeNull()
    expect(afterLayout, spokenAfter).not.toBeNull()
    expect(afterLayout!.paddingTop, spokenAfter).toBeGreaterThan(0)
    expect(afterLayout!.paddingBottom, spokenAfter).toBeGreaterThan(0)
    expect(after!.fits, spokenAfter).toBe(true)
    // The grid came down to the box the margin leaves: the borrowed grid's rows
    // were the full-screen count, and this one is the pane's own.
    expect(after!.grid.rows, spokenAfter).toBeLessThan(before!.grid.rows)
    // AND EXACTLY ONE RESIZE CAME OF IT — the one the hand-back owes, never one
    // the exit caused, and never a second from the key or the settle.
    await page.waitForTimeout(500)
    const settled = await layout(page)
    expect(settled!.resizes - beforeLayout!.resizes, spokenAfter).toBe(1)
  } finally {
    try {
      const info = await resolveBackend(page)
      if (createdId !== null) await rpc(page, info, 'profiles.delete', { id: createdId })
    } catch {
      /* the pane may already be gone */
    }
    fixture.proc.kill('SIGKILL')
  }
})
