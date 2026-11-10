/**
 * ONE GUTTER, ONE GRID, EVERY MODE (ADR-92zzg2y2, nocx-4y82c.1.14).
 *
 * The owner's decision of 2026-11-10: ordinary output, blocks, a plain ssh
 * shell and a full-screen program all wear the same gutter — one character cell
 * wide on the left and on the right, half a row on top and on bottom — and the
 * terminal's size is the same in every mode, so entering or leaving the
 * alternate screen changes nothing.
 *
 * This spec is the end-to-end witness for the defect it replaces: an inline TUI
 * over ssh finished as a block whose rows started at the left gutter and ran
 * past the pane's right edge, clipped, because the column count was computed
 * for a width that did not subtract the gutters the block body wears.
 *
 * The program prints exactly COLUMNS characters ending in a marker (COLUMNS is
 * the pty's own `stty size`), so the marker sits in the last column. Every
 * marker must be inside the content box — the pane less one cell on each side —
 * and inside the clip that draws it. Locally and over ssh, running and frozen,
 * and an alternate-screen program drawing its last column.
 */
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { test, expect, clickIntoEditor, promptReady, resolveBackend } from './harness'
import type { Page } from '@playwright/test'
import { startSshd, rpc } from './sshd-fixture'
import { readStand } from './stand'

const MARKER = 'LASTCOL'
/** The markerless ssh line uses a marker with no capital C: a per-key ssh
 *  intent has dropped that character from a long typed line (measured).
 *  `nocx-evj4b.1.12` owns the dropped-character defect; this spec works around
 *  it until it is fixed. */
const SSH_MARKER = 'MARK'
/** `C` is the pty's own column count; the row is exactly `C` characters and
 *  ends with the marker, so its last glyph is the last column. */
const PRINT_LAST_COLUMN =
  `C=$(stty size | cut -d' ' -f2); N=$((C-${MARKER.length})); ` +
  `printf '%*s' "$N" '' | tr ' ' '.'; printf '${MARKER}'`
const PRINT_LAST_COLUMN_AND_DONE = `${PRINT_LAST_COLUMN}; printf '\\nDONE\\n'`

/** A command that prints exactly `cols` characters ending in the marker. The
 *  count is this pane's own grid, read from the DOM, so the remote shell needs
 *  no `stty` query — and the command is delivered through the composer's
 *  submit path, one intent, not a key at a time. */
const markerCommand = (cols: number): string =>
  `for i in $(seq 1 ${cols - SSH_MARKER.length}); do printf .; done; printf ${SSH_MARKER}`

/** The active pane's grid columns, once it has a frame. */
async function gridCols(page: Page): Promise<number> {
  let cols = 0
  await expect
    .poll(
      async () =>
        (cols = await page.evaluate(() =>
          Number(
            document.querySelector('.pane.active .term-grid')?.getAttribute('aria-colcount') ?? 0,
          ),
        )),
      { timeout: 20_000 },
    )
    .toBeGreaterThan(0)
  return cols
}

interface MarkerGeometry {
  found: boolean
  /** The painted row's character count — the pty's COLUMNS. */
  textLength?: number
  /** The grid's own column count. */
  cols?: number
  cell?: number
  gutter?: number
  paneLeft?: number
  paneRight?: number
  textLeft?: number
  textRight?: number
  clipRight?: number
  /** The last glyph is inside the pane less one cell on each side. */
  insideContent?: boolean
  /** The last glyph is inside the box that clips it — nothing is cut. */
  insideClip?: boolean
  overflow?: number
}

async function markerGeometry(page: Page, marker: string): Promise<MarkerGeometry> {
  return page.evaluate((marker) => {
    const pane = document.querySelector<HTMLElement>('.pane.active')
    const inner = pane?.querySelector<HTMLElement>('.scrollback-inner')
    if (!pane || !inner) return { found: false }
    const cell = parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-width')) || 0
    const gutter = parseFloat(getComputedStyle(pane).getPropertyValue('--pane-inline-padding')) || 0
    const pr = pane.getBoundingClientRect()
    const rows = [...pane.querySelectorAll<HTMLElement>('.term-grid-row')].filter(
      (row) =>
        (row.textContent ?? '').trimEnd().endsWith(marker) &&
        row.getBoundingClientRect().height > 0,
    )
    const row = rows[rows.length - 1]
    if (!row) return { found: false }
    const clipEl = row.closest<HTMLElement>('.terminal-live-container, .cmd-output') ?? pane
    const cs = getComputedStyle(clipEl)
    const cr = clipEl.getBoundingClientRect()
    const clipRight = cr.right - parseFloat(cs.paddingRight)
    const range = document.createRange()
    range.selectNodeContents(row)
    const rr = range.getBoundingClientRect()
    const grid = pane.querySelector<HTMLElement>('.term-grid')
    const text = (row.textContent ?? '').trimEnd()
    const r2 = (v: number): number => Math.round(v * 100) / 100
    return {
      found: true,
      textLength: text.length,
      cols: Number(grid?.getAttribute('aria-colcount') ?? text.length),
      cell: r2(cell),
      gutter: r2(gutter),
      paneLeft: r2(pr.left),
      paneRight: r2(pr.right),
      textLeft: r2(rr.left),
      textRight: r2(rr.right),
      clipRight: r2(clipRight),
      insideContent: rr.left >= pr.left + gutter - 0.6 && rr.right <= pr.right - gutter + 0.6,
      insideClip: rr.right <= clipRight + 0.6,
      overflow: r2(rr.right - clipRight),
    }
  }, marker)
}

/** The one assertion this spec exists for: the marker is the last column and
 *  it is inside the content box, with no clipping. */
function assertMarkerInside(g: MarkerGeometry, spoken: string): void {
  expect(g.found, spoken).toBe(true)
  expect(g.cell!, `${spoken} — a cell width is published`).toBeGreaterThan(0)
  expect(g.gutter!, `${spoken} — the gutter is one cell`).toBeCloseTo(g.cell!, 1)
  expect(g.textLength, `${spoken} — the program printed the pty's columns`).toBe(g.cols)
  expect(g.insideContent, `${spoken} — the marker is inside the content box`).toBe(true)
}

/** Run a command that prints the last column, and wait for its marker. */
async function runMarker(page: Page, command: string, waitForDone = false): Promise<void> {
  await clickIntoEditor(page)
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  await expect
    .poll(
      async () => {
        const g = await markerGeometry(page, MARKER)
        if (!g.found || !g.insideContent) return false
        return !waitForDone || (await page.locator('.pane.active').innerText()).includes('DONE')
      },
      { timeout: 40_000 },
    )
    .toBe(true)
}

test('a local command that prints its last column is not clipped, running and frozen', async ({
  page,
}) => {
  test.setTimeout(180_000)
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)

  // RUNNING: the marker row is on the live grid, held by the sleep.
  await runMarker(page, `${PRINT_LAST_COLUMN}; printf '\\n'; sleep 300`)
  const running = await markerGeometry(page, MARKER)
  assertMarkerInside(running, `local, running: ${JSON.stringify(running)}`)
  await page.keyboard.press('Control+C')

  // FROZEN: the same row, now in the block's own output.
  await promptReady(page)
  await runMarker(page, PRINT_LAST_COLUMN_AND_DONE, true)
  await promptReady(page)
  const frozen = await markerGeometry(page, MARKER)
  assertMarkerInside(frozen, `local, frozen: ${JSON.stringify(frozen)}`)
})

/** Open this page on a plain ssh shell: no integration, no composer, the
 *  terminal is the pane. Returns once the shell has painted its first row. */
async function openSshPane(page: Page): Promise<{ close: () => Promise<void> }> {
  const remoteHome = mkdtempSync(path.join(tmpdir(), 'one-gutter-remote-'))
  const remoteCwd = mkdtempSync(path.join(tmpdir(), 'one-gutter-cwd-'))
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
    const sshDir = path.join(readStand().home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), fixture.knownHosts + '\n')
    await page.setViewportSize({ width: 1280, height: 800 })
    await page.goto('/')
    await promptReady(page)
    const wsInfo = await resolveBackend(page)
    const profileName = `one-gutter-${Date.now()}`
    const created = await rpc<{ id: string }>(page, wsInfo, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: fixture.user,
        keyPath: fixture.userKey,
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
    await expect
      .poll(
        async () =>
          page.evaluate(
            () =>
              document
                .querySelector('.pane.active .terminal-live-container')
                ?.classList.contains('live-unstructured') ?? false,
          ),
        { timeout: 90_000 },
      )
      .toBe(true)
    // The shell must have drawn before the person speaks: its first painted
    // row is the only readiness signal a markerless pane has.
    await expect
      .poll(
        async () =>
          page.evaluate(() => {
            const rows = Array.from(
              document.querySelectorAll('.pane.active .terminal-live-grid .term-grid-row'),
            )
            return rows.some((row) => (row.textContent ?? '').trim() !== '')
          }),
        { timeout: 90_000 },
      )
      .toBe(true)
    // THE PROMPT, not merely a painted row: a keystroke before the shell is
    // reading is an intent with nowhere to land, and it is gone.
    await expect
      .poll(
        async () =>
          page.evaluate(() =>
            Array.from(
              document.querySelectorAll('.pane.active .terminal-live-grid .term-grid-row'),
            ).some((row) => /[#$]$/.test((row.textContent ?? '').trimEnd())),
          ),
        { timeout: 90_000 },
      )
      .toBe(true)
    await page.locator('.pane.active .terminal-live-container').click()
    // A COLD FIRST SSH SESSION IS SLOW TO READ. One short command proves the
    // helper and the shell are both reading before the marker line goes out;
    // without it the first marker of a fresh container is lost and the spec
    // waits out its own timeout.
    await page.keyboard.type('echo READY', { delay: 20 })
    await page.keyboard.press('Enter')
    await expect
      .poll(
        async () =>
          page.evaluate(() =>
            Array.from(
              document.querySelectorAll('.pane.active .terminal-live-grid .term-grid-row'),
            ).some((row) => (row.textContent ?? '').trim() === 'READY'),
          ),
        { timeout: 90_000 },
      )
      .toBe(true)
    return { close }
  } catch (err) {
    await close()
    throw err
  }
}

/**
 * Type a line at a markerless shell. Each keystroke is an intent, and the pty
 * outruns a browser typing at full speed; the pace is what keeps every
 * character, and the shell's own echo is the proof the line arrived before
 * Enter. The wait is the echo, never a duration.
 *
 * The pace and the quote-free command are a workaround for `nocx-evj4b.1.12`
 * (a per-key intent on a markerless ssh pane has dropped characters from a
 * long line); the spec keeps them until that bead is fixed.
 */
async function typeAtShell(page: Page, text: string): Promise<void> {
  await page.keyboard.type(text, { delay: 20 })
  const flat = text.replace(/\s+/g, '')
  await expect
    .poll(
      async () =>
        page.evaluate(() =>
          (document.querySelector('.pane.active .terminal-live-grid')?.textContent ?? '').replace(
            /\s+/g,
            '',
          ),
        ),
      { timeout: 90_000 },
    )
    .toContain(flat)
  await page.keyboard.press('Enter')
  // A COLD SESSION CAN LOSE THE FIRST ENTER. A second one runs the same line
  // when the first was lost and is an empty command when it was not; a
  // program that reads its stdin sees a harmless newline.
  await page.keyboard.press('Enter')
}

// THE FIRST SSH FIXTURE OF A FRESH CONTAINER IS COLD: the helper is installed
// and started for the first time, and a long marker line sent into that first
// session is not read. This short line pays that cost and asserts the shell
// read it, so the gutter cases below run against a warm stand.
test('the ssh stand reads a line', async ({ page }) => {
  test.setTimeout(300_000)
  const ssh = await openSshPane(page)
  try {
    await expect(page.locator('.pane.active')).toContainText('READY')
  } finally {
    await ssh.close()
  }
})

test('a plain ssh command that prints its last column is not clipped while it runs', async ({
  page,
}) => {
  test.setTimeout(300_000)
  const ssh = await openSshPane(page)
  try {
    const cols = await gridCols(page)
    await typeAtShell(page, `${markerCommand(cols)}; sleep 300`)
    await expect
      .poll(
        async () => {
          const g = await markerGeometry(page, SSH_MARKER)
          return g.found && g.insideContent
        },
        { timeout: 90_000 },
      )
      .toBe(true)
    const running = await markerGeometry(page, SSH_MARKER)
    assertMarkerInside(running, `ssh, running: ${JSON.stringify(running)}`)
    await page.keyboard.press('Control+C')
  } finally {
    await ssh.close()
  }
})

test('a plain ssh command that prints its last column is not clipped once it is frozen', async ({
  page,
}) => {
  test.setTimeout(300_000)
  const ssh = await openSshPane(page)
  try {
    const cols = await gridCols(page)
    await typeAtShell(page, `${markerCommand(cols)}; echo; echo DONE`)
    await expect
      .poll(
        async () =>
          page.evaluate((marker) => {
            const rows = Array.from(document.querySelectorAll('.pane.active .term-grid-row'))
            return (
              rows.some((row) => (row.textContent ?? '').trim() === 'DONE') &&
              rows.some((row) => (row.textContent ?? '').trimEnd().endsWith(marker))
            )
          }, SSH_MARKER),
        { timeout: 90_000 },
      )
      .toBe(true)
    const frozen = await markerGeometry(page, SSH_MARKER)
    assertMarkerInside(frozen, `ssh, frozen: ${JSON.stringify(frozen)}`)
  } finally {
    await ssh.close()
  }
})

test('an alternate-screen program draws its last column inside the one gutter', async ({
  page,
}) => {
  test.setTimeout(180_000)
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)
  await clickIntoEditor(page)
  await page.keyboard.type(`printf '\\033[?1049h\\033[H'; ` + `${PRINT_LAST_COLUMN}; sleep 300`)
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .terminal-live-container')).toHaveClass(
    /live-fullscreen/,
    { timeout: 30_000 },
  )
  await expect
    .poll(
      async () => {
        const g = await markerGeometry(page, MARKER)
        return g.found && g.insideContent
      },
      { timeout: 30_000 },
    )
    .toBe(true)
  const alt = await markerGeometry(page, MARKER)
  assertMarkerInside(alt, `alternate screen: ${JSON.stringify(alt)}`)
  await page.keyboard.press('Control+C')
})

/**
 * THE SWITCH IS A PRESENTATION CHANGE (ADR-92zzg2y2).
 *
 * A real PTY program enters the alternate screen, holds it, and leaves. The
 * pane never changes size, so the pty must receive no resize and no SIGWINCH,
 * and the transcript's column must be exactly where it was. This is the fact
 * the old machinery (borrowed grid, hand-back on the first key, resize at the
 * switch) existed to work around; with one gutter in every mode there is
 * nothing to work around.
 */
test('entering and leaving the alternate screen resizes nothing and moves no column', async ({
  page,
}) => {
  test.setTimeout(240_000)
  const root = mkdtempSync(path.join(tmpdir(), 'one-gutter-switch-'))
  const log = path.join(root, 'events')
  const gate = path.join(root, 'gate')
  const exit = path.join(root, 'exit')
  const script = path.join(root, 'fixture.cjs')
  await page.addInitScript(() => {
    const host = window as unknown as { __resizeCount: number }
    host.__resizeCount = 0
    const Native = window.WebSocket
    window.WebSocket = class extends Native {
      override send(data: Parameters<WebSocket['send']>[0]): void {
        if (typeof data === 'string' && data.includes('"method":"resize"')) host.__resizeCount += 1
        super.send(data)
      }
    }
  })
  writeFileSync(
    script,
    String.raw`
const { execFileSync } = require('node:child_process')
const { appendFileSync, existsSync } = require('node:fs')
const log = ${JSON.stringify(log)}
const gate = ${JSON.stringify(gate)}
const exit = ${JSON.stringify(exit)}
const size = () =>
  execFileSync('stty', ['size'], { encoding: 'utf8', stdio: ['inherit', 'pipe', 'inherit'] }).trim()
const write = (kind, value = '') => appendFileSync(log, kind + ' ' + value + '\n')
process.stdout.on('resize', () => write('WINCH', size()))
write('READY', size())
let phase = 0
setInterval(() => {
  if (existsSync(exit)) process.exit(0)
  if (phase !== 0 || !existsSync(gate)) return
  phase = 1
  write('ENTER', size())
  process.stdout.write('\x1b[?1049h\x1b[H ALT-READY')
  setTimeout(() => {
    write('LEAVE', size())
    process.stdout.write('\x1b[?1049l')
    setTimeout(() => process.exit(0), 800)
  }, 900)
}, 10)
`,
  )
  try {
    await page.setViewportSize({ width: 1280, height: 800 })
    await page.goto('/')
    await promptReady(page)

    // A frozen block to watch: its column is the transcript's.
    await clickIntoEditor(page)
    await page.keyboard.type('printf "FIRST-BLOCK\\n"')
    await page.keyboard.press('Enter')
    await expect(
      page.locator('.pane.active .scrollback-inner > .cmd-block .term-grid-row').first(),
    ).toContainText('FIRST-BLOCK', { timeout: 30_000 })
    await promptReady(page)
    const column = () =>
      page.evaluate(() => {
        const pane = document.querySelector<HTMLElement>('.pane.active')!
        const row = pane.querySelector<HTMLElement>(
          '.scrollback-inner > .cmd-block .term-grid-row',
        )!
        const range = document.createRange()
        range.selectNodeContents(row)
        const pr = pane.getBoundingClientRect()
        return {
          left: Math.round((range.getBoundingClientRect().left - pr.left) * 100) / 100,
          width: Math.round(pr.width * 100) / 100,
          cols: Number(pane.querySelector('.term-grid')?.getAttribute('aria-colcount') ?? 0),
        }
      })
    const before = await column()

    // Launch the program; it waits for the gate.
    await clickIntoEditor(page)
    await page.keyboard.type(`node '${script}'`)
    await page.keyboard.press('Enter')
    await expect.poll(() => (existsSync(log) ? readFileSync(log, 'utf8') : '')).toContain('READY')
    const resizesBefore = await page.evaluate(
      () => (window as unknown as { __resizeCount: number }).__resizeCount,
    )
    writeFileSync(gate, 'go')
    await expect
      .poll(() => (existsSync(log) ? readFileSync(log, 'utf8') : ''), { timeout: 30_000 })
      .toContain('LEAVE')
    const resizesAfter = await page.evaluate(
      () => (window as unknown as { __resizeCount: number }).__resizeCount,
    )
    const events = readFileSync(log, 'utf8')
    const spoken = `switch events: ${JSON.stringify(events)}`
    // THE PTY HEARD NOTHING: no resize RPC, no SIGWINCH, and the same size on
    // both sides of the switch.
    expect(resizesAfter - resizesBefore, `${spoken} — no resize RPC`).toBe(0)
    expect(events, `${spoken} — no SIGWINCH`).not.toContain('WINCH')
    const enter = /ENTER (\d+ \d+)/.exec(events)?.[1]
    const leave = /LEAVE (\d+ \d+)/.exec(events)?.[1]
    expect(enter, spoken).toBeDefined()
    expect(leave, spoken).toBe(enter)

    // …and the transcript's column is exactly where it was.
    await promptReady(page)
    const after = await column()
    expect(after.left, `${spoken} — the transcript's column`).toBeCloseTo(before.left, 1)
    expect(after.width, `${spoken} — the pane's width`).toBeCloseTo(before.width, 1)
    expect(after.cols, `${spoken} — the grid's columns`).toBe(before.cols)
  } finally {
    writeFileSync(exit, 'exit')
  }
})
