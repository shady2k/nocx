/** Sparse text updates must not rebuild the unchanged procedural prefix (nocx-4y82c.1.7). */
import { standalone as test, test as appTest, expect, promptReady } from './harness'
import {
  judgeFrames,
  percentile,
  FRAME_TOLERANCE,
  P95_TOLERANCE,
  FLOAT_NOISE_MS,
} from './frame-budget.mts'

const COLS = 120
const ROWS = 40
const SAMPLES = 60

// Tracing a 9,000-node surface between actions is not part of painter cost.
test.use({ trace: { mode: 'retain-on-failure', snapshots: false, screenshots: false } })

test.describe('procedural suffix frame budget', () => {
  test('retains unchanged ink, exact pixels and the idle display cadence', async ({
    page,
  }, info) => {
    await page.setViewportSize({ width: 1100, height: 950 })
    // Keep the Vite origin without starting the app or opening a PTY.
    await page.route('**/__painter_probe', (route) =>
      route.fulfill({
        contentType: 'text/html',
        body: '<!doctype html><html><head><link rel="stylesheet" href="/src/style.css"></head><body></body></html>',
      }),
    )
    await page.goto('/__painter_probe')
    const probe = await page.evaluateHandle(
      async ({ cols, rows, samples }) => {
        const painterUrl = '/src/painter/painter.ts'
        const fitUrl = '/src/scrollback/cell-fit.ts'
        const fixtureUrl = '/src/painter/fixtures.ts'
        const serializerUrl = '/src/scrollback/serializer.ts'
        const { createCellPainter, metricOf } = (await import(
          painterUrl
        )) as typeof import('../frontend/src/painter/painter')
        const { createCellFit } = (await import(
          fitUrl
        )) as typeof import('../frontend/src/scrollback/cell-fit')
        const { frameOf, snapshotOf } = (await import(
          fixtureUrl
        )) as typeof import('../frontend/src/painter/fixtures')
        const { DEFAULT_SNAPSHOT } = (await import(
          serializerUrl
        )) as typeof import('../frontend/src/scrollback/serializer')
        await document.fonts.ready
        const host = document.createElement('div')
        host.id = 'painter-probe'
        // Geometry is the diagnosis's published 8 x 20 cell metric, not a font fallback.
        host.style.cssText = `position:relative;width:${cols * 8}px;height:${rows * 20}px;--term-cell-width:8px;--term-cell-height:20px;--term-cell-delta:0px`
        document.body.append(host)
        const surface = document.createElement('div')
        host.append(surface)
        const fit = createCellFit(() => host)
        if (!fit.begin()) throw new Error('the mounted fit has no geometry')
        await document.fonts.ready
        if (!fit.begin()) throw new Error('the loaded font has no geometry')
        const options = {
          surface,
          metric: () => metricOf(fit),
          warm: fit.warm,
          palette: () => DEFAULT_SNAPSHOT,
        }
        let painter = createCellPainter(options)
        const freeze = <T>(value: T): T => {
          if (value !== null && typeof value === 'object') {
            for (const child of Object.values(value)) freeze(child)
            Object.freeze(value)
          }
          return value
        }
        const snapshots = (ink: string) =>
          [0, 1].map((suffix) =>
            freeze(
              snapshotOf(
                frameOf(
                  suffix + 1,
                  Array.from({ length: rows }, () =>
                    Array.from(
                      { length: cols },
                      (_, col): import('../frontend/src/painter/fixtures').CellSpec => [
                        col === cols - 1 ? String(suffix) : ink,
                        1,
                        true,
                      ],
                    ),
                  ),
                ),
              ),
            ),
          )
        // Construction, model decoding and freezing are outside all timed samples.
        const procedural = snapshots('─')
        const ascii = snapshots('x')
        const immutableBefore = JSON.stringify(procedural)
        const sample = async (frames: typeof procedural, change: boolean) => {
          painter.apply(frames[0]!)
          const apply: number[] = []
          const raf: number[] = []
          await new Promise<void>((resolve) => {
            let previous: number | null = null
            let index = 0
            const tick = (timestamp: number) => {
              // Include the interval AFTER every apply, including the last one.
              if (previous !== null) raf.push(timestamp - previous)
              if (index === samples) {
                resolve()
                return
              }
              previous = timestamp
              const start = performance.now()
              painter.apply(frames[change ? (index + 1) % 2 : 0]!)
              apply.push(performance.now() - start)
              index++
              requestAnimationFrame(tick)
            }
            requestAnimationFrame(tick)
          })
          return { apply, raf }
        }
        painter.apply(procedural[0]!)
        const idle: number[] = []
        await new Promise<void>((resolve) => {
          let previous: number | null = null
          const tick = (timestamp: number) => {
            if (previous !== null) idle.push(timestamp - previous)
            previous = timestamp
            if (idle.length === samples) resolve()
            else requestAnimationFrame(tick)
          }
          requestAnimationFrame(tick)
        })
        const unchanged = await sample(procedural, false)
        const first = await sample(procedural, true)
        const plain = await sample(ascii, true)
        const second = await sample(procedural, true)
        painter.apply(procedural[0]!)
        const ink = [...surface.querySelectorAll('svg')]
        painter.apply(procedural[1]!)
        const nextInk = [...surface.querySelectorAll('svg')]
        const retained =
          ink.length > 0 &&
          ink.length === nextInk.length &&
          ink.every((node, index) => node === nextInk[index])
        const expectedText = '─'.repeat(cols - 1) + '1'
        const texts = [...surface.querySelectorAll('.term-grid-row')].map((row) => row.textContent)
        const selection = getSelection()!
        const range = document.createRange()
        range.selectNodeContents(surface.querySelector('.term-grid-row')!)
        selection.removeAllRanges()
        selection.addRange(range)
        const copied = selection.toString()
        selection.removeAllRanges()
        return {
          idle,
          metrics: { unchanged, first, plain, second },
          retained,
          inkCount: ink.length,
          texts,
          expectedText,
          copied,
          immutable: JSON.stringify(procedural) === immutableBefore,
          fresh: () => {
            painter.dispose()
            surface.replaceChildren()
            painter = createCellPainter(options)
            painter.apply(procedural[1]!)
          },
        }
      },
      { cols: COLS, rows: ROWS, samples: SAMPLES },
    )
    const result = await probe.evaluate((state) => ({
      idle: state.idle,
      metrics: state.metrics,
      retained: state.retained,
      inkCount: state.inkCount,
      texts: state.texts,
      expectedText: state.expectedText,
      copied: state.copied,
      immutable: state.immutable,
    }))
    await info.attach('procedural-suffix-metrics', {
      body: JSON.stringify(result, null, 2),
      contentType: 'application/json',
    })
    console.log(
      'procedural-suffix-measurements',
      JSON.stringify({
        browser: info.project.name,
        idle: percentile(result.idle, 0.5),
        runs: Object.fromEntries(
          Object.entries(result.metrics).map(([name, run]) => [
            name,
            {
              applyP50: percentile(run.apply, 0.5),
              applyP95: percentile(run.apply, 0.95),
              rafP50: percentile(run.raf, 0.5),
              rafP95: percentile(run.raf, 0.95),
            },
          ]),
        ),
      }),
    )
    expect(result.immutable).toBe(true)
    expect(result.inkCount).toBe(ROWS)
    expect(result.retained, 'every unchanged SVG survives the suffix update').toBe(true)
    expect(result.texts).toEqual(Array.from({ length: ROWS }, () => result.expectedText))
    expect(result.copied).toBe(result.expectedText)
    const incremental = await page.locator('#painter-probe').screenshot()
    await probe.evaluate((state) => state.fresh())
    const fresh = await page.locator('#painter-probe').screenshot()
    expect(
      incremental.equals(fresh),
      'incremental pixels equal a fresh paint of the same snapshot',
    ).toBe(true)
    for (const [name, run] of Object.entries(result.metrics)) {
      expect(run.apply).toHaveLength(SAMPLES)
      expect(run.raf).toHaveLength(SAMPLES)
      const verdict = judgeFrames(result.idle, run.raf)
      expect(verdict.failures, `${name}: ${JSON.stringify(verdict)}`).toEqual([])
      // Synchronous work must also fit inside the independently measured display budget.
      expect(percentile(run.apply, 0.5), `${name} apply median`).toBeLessThanOrEqual(
        verdict.baselineMs * FRAME_TOLERANCE + FLOAT_NOISE_MS,
      )
      expect(percentile(run.apply, 0.95), `${name} apply p95`).toBeLessThanOrEqual(
        verdict.baselineMs * P95_TOLERANCE + FLOAT_NOISE_MS,
      )
    }
    await probe.dispose()
  })
})

appTest(
  'a full-screen application updates a counter beside intact procedural borders',
  async ({ page }) => {
    await page.goto('/')
    await promptReady(page)
    // A terminal application driven by input, not sleeps: each key changes only
    // the suffix. It uses the real PTY, runtime, transport and live painter.
    const prefix = '─'.repeat(39)
    const command = String.raw`printf '\033[?1049h\033[?25l\033[H${prefix}0'; IFS= read -r answer; printf '\033[1;40H1'; IFS= read -r answer; printf '\033[?25h\033[?1049l'`
    await page.locator('.pane.active .nocx-editor-input').fill(command)
    await page.keyboard.press('Enter')
    const row = page
      .locator('.pane.active .terminal-live-grid .term-grid-row')
      .filter({ hasText: prefix })
    await expect(row).toHaveText(new RegExp(`^${prefix}0 *$`))
    await expect(row.locator('svg')).toHaveCount(1)
    const ink = await row.locator('svg').first().elementHandle()
    await page.keyboard.press('Enter')
    await expect(row).toHaveText(new RegExp(`^${prefix}1 *$`))
    expect(
      await ink!.evaluate((node) => node.isConnected),
      'the application suffix update keeps the original border ink',
    ).toBe(true)
    await page.keyboard.press('Enter')
    await promptReady(page)
  },
)
