import { test, expect, promptReady, resolveBackend } from './harness'
import { readStand } from './stand'
import { startSshd, rpc } from './sshd-fixture'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

// A FULL-SCREEN PROGRAM MUST NOT LEAVE THE PANE ROWS IT CANNOT SHOW (nocx-djx1u).
//
// Leaving the alternate screen sends no resize: a resize there is the SIGWINCH
// that starts the next borrow (ADR-2yzsq8mk decision 4). So the pty keeps the
// grid the program was given — including its ROWS. A plain ssh shell has no OSC
// 133, so no authenticated prompt ever comes to release that ownership, and the
// ordinary layout's four-side margin (one top, one bottom) is two rows shorter
// than the full-screen box was. The borrowed grid therefore did not fit the box
// it was drawn in, and the rows that fell off the bottom were the newest ones —
// the prompt a person was about to type at.
//
// The assertion is a shape, not a number: every painted row of the grid lies
// inside the pane's visible box, and the last row of a screenful is one of
// them. It holds at any pane height and any cell metric.

const GRID = '.pane.active .terminal-live-grid'
const ROW = '.term-grid-row'

interface Frame {
  /** The pane's visible box: the scroller the grid is drawn in. */
  area: { top: number; bottom: number }
  /** The grid's own rectangle, and the rows painted inside it. */
  grid: { top: number; bottom: number; rows: number }
  painted: { text: string; top: number; bottom: number }[]
  /** True when the grid box is inside the pane's box: nothing is cut. */
  fits: boolean
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

test('the rows a full-screen program leaves behind are all visible (nocx-djx1u)', async ({
  page,
}) => {
  test.setTimeout(240_000)
  const remoteHome = mkdtempSync(path.join(tmpdir(), 'alt-exit-remote-'))
  const remoteCwd = mkdtempSync(path.join(tmpdir(), 'alt-exit-cwd-'))
  const fixture = await startSshd({ home: remoteHome, cwd: remoteCwd })
  let createdId: string | null = null
  try {
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
        // no prompt marker to release a borrowed grid.
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

    // The shell must have drawn before the person speaks (the keystroke rule
    // the layout branch's own pane-margins case records): the first painted
    // row is the only honest readiness signal a markerless pane has.
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

    // The screenful's last row is the witness: it is the newest row, and the
    // one the clip took.
    await expect
      .poll(
        async () => {
          const f = await frame(page)
          return f?.painted.some((row) => row.text === 'EXITROW-40') ?? false
        },
        { timeout: 40_000 },
      )
      .toBe(true)

    const measured = await frame(page)
    const spoken = `alt-screen exit: ${JSON.stringify(measured)}`
    expect(measured, spoken).not.toBeNull()
    // NOTHING IS CUT: the grid's box is the pane's box.
    expect(measured!.fits, spoken).toBe(true)
    // AND THE NEWEST ROW IS INSIDE IT — a visible row of the grid, not one
    // painted past the pane's bottom edge.
    const last = measured!.painted.find((row) => row.text === 'EXITROW-40')
    expect(last, spoken).toBeDefined()
    expect(last!.bottom, spoken).toBeLessThanOrEqual(measured!.area.bottom + 1)
    expect(last!.top, spoken).toBeGreaterThanOrEqual(measured!.area.top - 1)
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
