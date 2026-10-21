/**
 * e2e: a restored tab refits the program that was running in it (nocx-1ebrs.42).
 *
 * FOUND BY THE OWNER on the shipped 0.6.1: a herdr tab taken back by a fresh
 * window came back with the missing-output card and the TUI painted at about
 * HALF the pane width, and nothing but closing the tab helped.
 *
 * The journey here is the owner's, through the real restore path: a window
 * runs a full-screen program, the window goes away the way a quit takes it
 * (the session stays live on the coordinator), and a genuinely fresh window —
 * a wider one, because a restored window is never guaranteed the size of the
 * one it replaces — takes the session back through the layout restore.
 *
 * WHAT A PERSON GETS: the program's own view of its size is the PTY's
 * `stty size`, and what the painter shows is that program's repaint — the
 * fixture repaints on SIGWINCH, exactly what a real TUI does. Healthy
 * behaviour is the reclaim refitting the program to the pane it came back
 * into — the same resize the pane sends on a window resize — so within one
 * cell, the painted grid is as wide as the pane is, and the fixture's
 * reported PTY size agrees with what it repainted.
 *
 * Every wait observes a DOM state or a fixture file, never a duration.
 */
import { expect, type Browser, type Page } from '@playwright/test'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  openControlPlane,
  standalone as base,
  appReadyForInput,
  bindEndpoint,
  clickIntoEditor,
  promptReady,
  VaultBackend,
  type BackendEndpoint,
} from './harness'
import { BASE_URL } from './base-url'
import { readStand } from './stand'

const serverBin = () => readStand().server

const GRID = '.pane.active .terminal-live-container'
const GRID_ROW = `${GRID} .term-grid-row`
const INPUT = '.pane.active .nocx-editor-input'

const test = base

let backend: VaultBackend
let endpoint: BackendEndpoint
let fixtureDir = ''
let scriptPath = ''
let sizeLogPath = ''
let winchLogPath = ''
let sizeReqPath = ''
let exitPath = ''

/** Complete "rows cols" lines the fixture wrote — a torn append reads as no
 *  answer and lets the poll wait, rather than parsing as a wrong number. */
function sizeAnswers(): string[] {
  try {
    return readFileSync(sizeLogPath, 'utf8')
      .split('\n')
      .filter((l) => /^\d+\s+\d+$/.test(l.trim()))
  } catch {
    return []
  }
}

function winches(): string[] {
  try {
    return readFileSync(winchLogPath, 'utf8')
      .split('\n')
      .filter((l) => /^\d+\s+\d+$/.test(l.trim()))
  } catch {
    return []
  }
}

/** "rows cols" — the PTY's own answer, the program's ground truth. */
function colsOf(line: string): number {
  return Number(line.trim().split(/\s+/)[1])
}

interface PaneMeasure {
  areaWidth: number
  paintedCols: number
  paintedRows: number
  gridWidth: number
}

async function measurePane(page: Page): Promise<PaneMeasure> {
  return page.evaluate(() => {
    const pane = document.querySelector('.pane.active')
    const area = pane?.querySelector<HTMLElement>('.scrollback-area')
    const rows = [...(pane?.querySelectorAll<HTMLElement>('.term-grid-row') ?? [])]
    const widths = rows.map((r) => r.getBoundingClientRect().width)
    return {
      areaWidth: area?.clientWidth ?? 0,
      paintedCols: rows.length > 0 ? (rows[0].textContent?.length ?? 0) : 0,
      paintedRows: rows.length,
      gridWidth: widths.length > 0 ? Math.max(...widths) : 0,
    }
  })
}

/** Wait until the TUI's own drawing is on screen: the marker row is first. */
async function awaitTuiPainted(page: Page, what: string): Promise<void> {
  const probe = () =>
    page.evaluate((selector: string) => {
      const live = document.querySelector('.pane.active .terminal-live-container')
      const rows = [...document.querySelectorAll<HTMLElement>(selector)]
      return {
        rows: rows.length,
        first: (rows[0]?.textContent ?? '').slice(0, 12),
        liveClass: live?.className ?? '(no live container)',
        head: (live?.textContent ?? '').slice(0, 60),
      }
    }, GRID_ROW)
  try {
    await expect
      .poll(
        async () => {
          const p = await probe()
          return p.rows > 0 && p.first.startsWith('R00') ? p.rows : 0
        },
        { timeout: 30_000, message: `${what}: the TUI never painted its marker rows` },
      )
      .toBeGreaterThan(0)
  } catch (err) {
    const p = await probe()
    throw new Error(
      `${what}: the TUI never painted its marker rows; last state ${JSON.stringify(p)}; ` +
        `pty answers ${JSON.stringify(sizeAnswers())}, winches ${winches().length}`,
      { cause: err as Error },
    )
  }
}

/** The TUI's painted width, from the marker row's own text. */
async function paintedCols(page: Page): Promise<number> {
  const rows = await page.locator(GRID_ROW).allTextContents()
  return rows.length > 0 ? rows[0].length : 0
}

async function freshClient(
  browser: Browser,
  ep: BackendEndpoint,
  viewport?: { width: number; height: number },
): Promise<Page> {
  const context = await browser.newContext({
    baseURL: BASE_URL,
    ...(viewport ? { viewport } : {}),
  })
  const page = await context.newPage()
  await bindEndpoint(page, ep)
  await page.goto('/')
  return page
}

/** The live sessions the coordinator holds, asked from the test's own socket. */
async function liveSessions(
  ep: BackendEndpoint,
): Promise<{ sessionId: string; attached: boolean; paneId: string | null }[]> {
  const wire = await openControlPlane(ep.port, ep.token)
  try {
    const result = (await wire.call('sessions.live', {})) as {
      sessions: { sessionId: string; attached: boolean; paneId: string | null }[]
    }
    return result.sessions
  } finally {
    wire.close()
  }
}

test.describe('a restored tab refits its program (nocx-1ebrs.42)', () => {
  test.beforeAll(async () => {
    const root = mkdtempSync(join(tmpdir(), 'nocx-1ebrs42-e2e-'))
    backend = new VaultBackend(serverBin(), { root })
    fixtureDir = mkdtempSync(join(tmpdir(), 'nocx-1ebrs42-tui-'))
    endpoint = await backend.start()
    scriptPath = join(fixtureDir, 'herdr.sh')
    sizeLogPath = join(fixtureDir, 'size.log')
    winchLogPath = join(fixtureDir, 'winch.log')
    sizeReqPath = join(fixtureDir, 'size-request')
    exitPath = join(fixtureDir, 'exit')

    // A small herdr: it owns the alternate screen, paints rows as wide as its
    // PTY says it is, repaints at that width on every SIGWINCH, and answers a
    // size question from a file for as long as it runs. It records WINCH so a
    // resize cannot hide in a screenshot.
    writeFileSync(
      scriptPath,
      [
        `SIZELOG=${sizeLogPath}`,
        `WINCHLOG=${winchLogPath}`,
        `SIZEREQ=${sizeReqPath}`,
        `EXIT=${exitPath}`,
        'draw() {',
        "  W=$(stty size | cut -d' ' -f2)",
        "  FILL=$(printf 'X%.0s' $(seq 1 $((W-3))))",
        "  printf '\\033[2J\\033[H'",
        '  i=0',
        '  while [ "$i" -lt 28 ]; do',
        '    printf "R%02d%s\\r\\n" "$i" "$FILL"',
        '    i=$((i+1))',
        '  done',
        '}',
        `trap 'printf "%s\\n" "$(stty size)" >> "$WINCHLOG"; draw' WINCH`,
        "printf '\\033[?1049h'",
        'printf \'%s\\n\' "$(stty size)" >> "$SIZELOG"',
        'draw',
        "printf '\\033[?25l'",
        'while [ ! -e "$EXIT" ]; do',
        '  if [ -e "$SIZEREQ" ]; then',
        '    rm -f "$SIZEREQ"',
        '    printf \'%s\\n\' "$(stty size)" >> "$SIZELOG"',
        '  fi',
        '  sleep 0.1',
        'done',
        "printf '\\033[?1049l'",
        'exit 0',
        '',
      ].join('\n'),
    )
  })

  test.afterAll(async () => {
    try {
      writeFileSync(exitPath, 'release\n')
    } catch {
      /* the fixture may already be gone with its tmpdir */
    }
    backend?.stop()
  })

  test('the program comes back at the pane it was restored into', async ({ browser }) => {
    const ep = endpoint

    // ── window 1: herdr runs at this window's size ────────────────────────
    const first = await freshClient(browser, ep)
    await appReadyForInput(first)
    await promptReady(first)
    await clickIntoEditor(first)
    await first.keyboard.type(`sh '${scriptPath}'`)
    await first.keyboard.press('Enter')
    await expect(first.locator(INPUT)).toBeHidden({ timeout: 30_000 })
    await expect(first.locator(GRID)).toHaveClass(/live-fullscreen/, { timeout: 30_000 })
    await awaitTuiPainted(first, 'window 1')
    // The TUI and its PTY agree in window 1: the fixture repaints on WINCH,
    // so whichever size the pane's own settle fit leaves the session at, the
    // painted rows answer it. Both halves are read in the same pass, so a
    // settle resize that lands under the poll is waited out, not raced.
    await expect
      .poll(
        async () => {
          writeFileSync(sizeReqPath, 'ask\n')
          const answers = sizeAnswers()
          const pty = answers.length > 0 ? colsOf(answers[answers.length - 1]) : -1
          const painted = await paintedCols(first)
          return pty > 0 && painted === pty
        },
        { timeout: 30_000, message: 'the TUI never repainted at its PTY width' },
      )
      .toBe(true)

    const sessionsBefore = await liveSessions(ep)
    expect(sessionsBefore).toHaveLength(1)
    expect(sessionsBefore[0].attached).toBe(true)
    const sessionId = sessionsBefore[0].sessionId
    const winchesBefore = winches().length

    // ── the window goes the way a quit takes it; the session lives ───────
    await first.goto('about:blank')

    // ── window 2: a wider window takes the session back ──────────────────
    const second = await freshClient(browser, ep, { width: 2400, height: 1000 })
    await appReadyForInput(second)

    // The SAME session, reclaimed by the restored pane.
    await expect
      .poll(async () => (await liveSessions(ep))[0], {
        timeout: 60_000,
        message: 'the restored pane never claimed the session',
      })
      .toMatchObject({ sessionId, attached: true })

    // herdr owns the restored pane again, and has repainted into it: the
    // fixture redraws on WINCH, so a first marker row at the top of the
    // screen is its own word that the repaint landed.
    await expect(second.locator(GRID)).toHaveClass(/live-fullscreen/, { timeout: 60_000 })
    await awaitTuiPainted(second, 'restored pane')

    // The program's own answer, asked AFTER the reclaim settled: wait until
    // the fixture answers with the width its repainted screen shows. The
    // fixture answers on its own loop, so one ask can still read the window-1
    // answer — the poll waits the redraw out.
    let restoredCols = -1
    await expect
      .poll(
        async () => {
          writeFileSync(sizeReqPath, 'ask\n')
          const answers = sizeAnswers()
          restoredCols = answers.length > 1 ? colsOf(answers[answers.length - 1]) : -1
          const painted = await paintedCols(second)
          return painted > 0 && restoredCols === painted ? restoredCols : -1
        },
        { timeout: 30_000, message: 'the fixture never answered with its repainted width' },
      )
      .toBeGreaterThan(0)

    const restored = await measurePane(second)
    const cellPx = restored.gridWidth / restored.paintedCols
    expect(cellPx).toBeGreaterThan(0)
    const label =
      `restored pane ${restored.areaWidth}px, cell ${cellPx.toFixed(2)}px, ` +
      `painted ${restored.paintedCols} cols (${restored.gridWidth}px), ` +
      `pty cols ${restoredCols}, winches ${winches().length} (was ${winchesBefore})`

    // THE BEAD: the restored pane refits the program to the pane it came back
    // into. The fixture's PTY size is the program's ground truth; the painted
    // grid is what a person sees.
    expect(restoredCols * cellPx, label).toBeGreaterThanOrEqual(restored.areaWidth - cellPx * 2)
    expect(restoredCols * cellPx, label).toBeLessThanOrEqual(restored.areaWidth)

    // …and the repaint followed the resize: what the painter shows is what
    // the program repainted.
    expect(Math.abs(restored.gridWidth - restoredCols * cellPx), label).toBeLessThanOrEqual(
      cellPx * 1.5,
    )

    // The resize reached the program as a WINCH — the mechanism a real TUI
    // repaints on. Window 1's own settle fit already produced some; the
    // reclaim must have added at least one more.
    expect(winches().length, label).toBeGreaterThan(winchesBefore)
  })
})
