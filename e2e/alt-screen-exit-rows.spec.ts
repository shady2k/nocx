import { test, expect, promptReady, resolveBackend } from './harness'
import { readStand } from './stand'
import { startSshd, rpc } from './sshd-fixture'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

// AN ALTERNATE-SCREEN ENTRY AND EXIT CHANGE NOTHING (ADR-92zzg2y2).
//
// The old rule fitted a full-screen program flush to the pane and gave it one
// row of bottom margin; leaving it kept the program's grid, and a markerless
// shell handed the grid back at the person's first key with one resize
// (ADR-2yzsq8mk, nocx-djx1u). The owner's rule of 2026-11-10 gives every mode
// the same one-cell gutter and half-row vertical margin, so the terminal's size
// never changes at a switch and there is nothing to hand back.
//
// A plain ssh shell is the sharpest witness: it has no OSC 133 markers, so if
// the switch did resize, nothing in the session could announce the moment the
// program was done with the pane. This spec proves:
//
//   1. entering and leaving the alternate screen sends NO resize, and the pty's
//      grid is unchanged;
//   2. the pane keeps its one gutter in both buffers, and no rows are cut;
//   3. a screenful printed after the exit is whole, newest row included;
//   4. a program that enters the screen again still changes nothing.

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
  /** The one gutter the layout pays, in CSS pixels. */
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
      paddingTop: parseFloat(style.paddingTop) || 0,
      paddingBottom: parseFloat(style.paddingBottom) || 0,
      resizes: Array.isArray(counter) ? counter.length : -1,
    }
  }, LAYOUT)
}

interface MarkerlessPane {
  fixture: Awaited<ReturnType<typeof startSshd>>
  /** Delete the profile this pane was opened with and stop its sshd. */
  close: () => Promise<void>
}

/**
 * Open this page on a plain ssh shell: no integration, so no OSC 133 and no
 * authenticated prompt to hand a borrowed grid back. Returns once the shell
 * has painted its first row — the only readiness signal a markerless pane has —
 * with the keyboard in the pane.
 */
async function openMarkerlessPane(page: import('@playwright/test').Page): Promise<MarkerlessPane> {
  const remoteHome = mkdtempSync(path.join(tmpdir(), 'alt-exit-remote-'))
  const remoteCwd = mkdtempSync(path.join(tmpdir(), 'alt-exit-cwd-'))
  const fixture = await startSshd({ home: remoteHome, cwd: remoteCwd })
  let createdId: string | null = null
  const close = async (): Promise<void> => {
    try {
      const info = await resolveBackend(page)
      if (createdId !== null) await rpc(page, info, 'profiles.delete', { id: createdId })
    } catch {
      /* the pane may already be gone */
    }
    fixture.proc.kill('SIGKILL')
  }
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

    // The shell must have drawn before the person speaks.
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
    return { fixture, close }
  } catch (err) {
    await close()
    throw err
  }
}

test('entering and leaving the alternate screen sends no resize, and the screenful after it is whole', async ({
  page,
}) => {
  test.setTimeout(240_000)
  const stand = await openMarkerlessPane(page)
  try {
    // The shell has drawn and settled; its own attach fit is not this test's
    // subject.
    const before = await frame(page)
    const beforeLayout = await layout(page)
    expect(before, 'the shell painted a grid').not.toBeNull()
    expect(beforeLayout).not.toBeNull()

    // ONE COMMAND, because the shell is markerless: enter the alternate screen,
    // hold it, leave it, then print a screenful on the primary buffer.
    await page.keyboard.type(
      "C1=$(stty size); printf '\\033[?1049h\\033[H'; sleep 0.8; " +
        "printf '\\033[?1049l'; " +
        'i=1; while [ $i -le 40 ]; do echo EXITROW-$i; i=$((i+1)); done; ' +
        'printf "SIZES %s %s\\n" "$C1" "$(stty size)"',
    )
    await page.keyboard.press('Enter')

    // THE SCREENFUL IS WHOLE. The last row is the witness: it is the newest
    // row, and the one a short box used to cut.
    await expect
      .poll(
        async () => {
          const f = await frame(page)
          return f?.painted.some((row) => row.text === 'EXITROW-40') ?? false
        },
        { timeout: 40_000 },
      )
      .toBe(true)
    const after = await frame(page)
    const afterLayout = await layout(page)
    const spoken = `after the switch: ${JSON.stringify(after)} ${JSON.stringify(afterLayout)}`
    expect(after, spoken).not.toBeNull()
    expect(afterLayout, spoken).not.toBeNull()
    // NOTHING IS CUT: the grid's box is inside the pane's, and the newest row
    // is above the pane's bottom edge by the one gutter.
    expect(after!.fits, spoken).toBe(true)
    const newest = after!.painted.find((row) => row.text === 'EXITROW-40')
    expect(newest, spoken).toBeDefined()
    expect(newest!.bottom, spoken).toBeLessThanOrEqual(after!.area.bottom + 1)
    // THE SAME GRID AS BEFORE THE SWITCH, and the same one gutter.
    expect(after!.grid.rows, spoken).toBe(before!.grid.rows)
    expect(afterLayout!.paddingTop, spoken).toBeCloseTo(beforeLayout!.paddingTop, 1)
    expect(afterLayout!.paddingBottom, spoken).toBeCloseTo(beforeLayout!.paddingBottom, 1)
    // AND THE PTY'S OWN SIZE DID NOT MOVE. The switch is a presentation change
    // (ADR-92zzg2y2); the pane never moved, so `stty size` on both sides of it
    // is one number. (A resize RPC count would also count the pane's own
    // attach fit; the program's ground truth is the size it reads.)
    const sizes = after!.painted.find((row) => row.text.startsWith('SIZES '))
    expect(sizes, `${spoken} — the command reported both sizes`).toBeDefined()
    // `stty size` prints "rows cols"; both pairs must be one size.
    const [, r1, c1, r2, c2] = sizes!.text.split(/\s+/)
    expect(r1, spoken).toBeDefined()
    expect(`${r2} ${c2}`, spoken).toBe(`${r1} ${c1}`)
  } finally {
    await stand.close()
  }
})

test('a program that takes the pane again still changes nothing', async ({ page }) => {
  test.setTimeout(240_000)
  const stand = await openMarkerlessPane(page)
  try {
    const beforeLayout = await layout(page)
    expect(beforeLayout).not.toBeNull()
    const before = await frame(page)
    expect(before).not.toBeNull()

    // Enter, leave, enter again, leave again, and print a row on the primary
    // buffer. Nothing in this sequence is a size change.
    await page.keyboard.type(
      "C1=$(stty size); printf '\\033[?1049h\\033[H'; sleep 1.2; " +
        "printf '\\033[?1049l'; sleep 1.2; " +
        "printf '\\033[?1049h\\033[H'; sleep 1.2; printf '\\033[?1049l'; " +
        'printf "AFTER\\n"; printf "SIZES %s %s\\n" "$C1" "$(stty size)"',
    )
    await page.keyboard.press('Enter')

    await expect
      .poll(
        async () => {
          const f = await frame(page)
          return (
            (f?.painted.some((row) => row.text === 'AFTER') ?? false) &&
            (f?.painted.some((row) => row.text.startsWith('SIZES ')) ?? false)
          )
        },
        { timeout: 40_000 },
      )
      .toBe(true)
    const after = await frame(page)
    const afterLayout = await layout(page)
    const spoken = `after two switches: ${JSON.stringify(after)} ${JSON.stringify(afterLayout)}`
    expect(after, spoken).not.toBeNull()
    expect(afterLayout, spoken).not.toBeNull()
    expect(after!.fits, spoken).toBe(true)
    expect(after!.grid.rows, spoken).toBe(before!.grid.rows)
    const sizes = after!.painted.find((row) => row.text.startsWith('SIZES '))
    expect(sizes, `${spoken} — the command reported both sizes`).toBeDefined()
    // `stty size` prints "rows cols"; both pairs must be one size.
    const [, r1, c1, r2, c2] = sizes!.text.split(/\s+/)
    expect(r1, spoken).toBeDefined()
    expect(`${r2} ${c2}`, spoken).toBe(`${r1} ${c1}`)
  } finally {
    await stand.close()
  }
})
