import { test, expect, promptReady } from './harness'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

function read(path: string): string {
  try {
    return readFileSync(path, 'utf8')
  } catch {
    return ''
  }
}

// Diagnostic only. Files are the real foreground program's receipts, not the
// browser's promise to send a resize or input. Retain every attempt's matrix.
for (const dpr of [2]) {
  test.describe(`alternate-screen diagnosis at DPR ${dpr}`, () => {
    test.use({ deviceScaleFactor: dpr })
    for (const timing of ['before', 'during', 'after']) {
      test(`resize ${timing} the switch`, async ({ page }, testInfo) => {
        const started = Date.now()
        const root = mkdtempSync(join(tmpdir(), 'nocx-alt-diagnosis-'))
        const gate = join(root, 'switch')
        const exit = join(root, 'exit')
        const sample = join(root, 'size')
        const received = join(root, 'received')
        const script = join(root, 'fixture.sh')
        const narrow = { width: 850, height: 800 }
        const wide = { width: 1700, height: 800 }
        const samples: unknown[] = []
        const observations: Record<string, unknown> = {}
        let error = ''
        let record: Record<string, unknown> | null = null
        writeFileSync(
          script,
          [
            '#!/bin/bash',
            `old=$(stty -g)`,
            `cleanup() { stty "$old"; printf '\\033[?1000l\\033[?1006l\\033[?1049l'; }`,
            'trap cleanup EXIT',
            `printf 'WAITING-FOR-SWITCH\\n'`,
            `while [ ! -e '${gate}' ] && [ ! -e '${exit}' ]; do sleep 0.01; done`,
            'stty raw -echo',
            `printf '\\033[?1049h\\033[?1000h\\033[?1006h\\033[2J\\033[5;1HALT-READY'`,
            `while [ ! -e '${exit}' ]; do`,
            '  size=$(stty size)',
            `  printf '%s\\n' "$size" > '${sample}.tmp'`,
            `  mv '${sample}.tmp' '${sample}'`,
            `  if IFS= read -r -N 1 -t 0.05 key; then printf '%s' "$key" >> '${received}'; fi`,
            'done',
            '',
          ].join('\n'),
        )
        try {
          await page.setViewportSize(narrow)
          await page.goto('/')
          await promptReady(page)
          await page.keyboard.type(`bash '${script}'`)
          await page.keyboard.press('Enter')
          await expect(page.locator('.pane.active .terminal-live-container')).toContainText(
            'WAITING-FOR-SWITCH',
          )
          if (timing === 'before') {
            await page.setViewportSize(wide)
            writeFileSync(gate, 'switch')
          } else if (timing === 'during') {
            await Promise.all([
              page.setViewportSize(wide),
              Promise.resolve().then(() => writeFileSync(gate, 'switch')),
            ])
          } else {
            writeFileSync(gate, 'switch')
            await expect(page.locator('.pane.active .terminal-live-container')).toHaveClass(
              /live-fullscreen/,
            )
            await page.setViewportSize(wide)
          }
          await expect(page.locator('.pane.active .terminal-live-container')).toContainText(
            'ALT-READY',
          )
          await expect(page.locator('.pane.active .terminal-live-container')).toHaveClass(
            /live-fullscreen/,
          )
          const measure = async () => {
            const pty = read(sample).trim().split(/\s+/).map(Number)
            const dom = await page.evaluate(() => {
              const pane = document.querySelector('.pane.active')!
              const grid = pane.querySelector<HTMLElement>('.terminal-live-grid')!
              const area = pane.querySelector<HTMLElement>('.scrollback-area')!
              const inner = pane.querySelector<HTMLElement>('.scrollback-inner')!
              const cellWidth = parseFloat(
                getComputedStyle(inner).getPropertyValue('--term-cell-width'),
              )
              const row = grid.querySelector('.term-grid-row')!
              const range = document.createRange()
              range.selectNodeContents(row)
              return {
                areaWidth: area.clientWidth,
                cols: Number(grid.getAttribute('aria-colcount')),
                cellWidth,
                paintedWidth: range.getBoundingClientRect().width,
                focus: document.activeElement?.className ?? '',
                dpr: window.devicePixelRatio,
              }
            })
            const result = {
              ptyRows: pty[0] ?? -1,
              ptyCols: pty[1] ?? -1,
              ...dom,
              fill: dom.paintedWidth / dom.areaWidth,
              shortByCells: (dom.areaWidth - (pty[1] ?? 0) * dom.cellWidth) / dom.cellWidth,
            }
            samples.push(result)
            return result
          }
          const probe = async (name: string, check: () => Promise<void>) => {
            try {
              await check()
              observations[name] = 'received'
            } catch (failure) {
              observations[name] = String(failure)
            }
          }
          // Keep probing even when one fact is wrong. A missing keyboard
          // receipt must not prevent measuring a mouse receipt or geometry.
          await probe('geometrySettled', async () => {
            await expect
              .poll(
                async () => {
                  const s = await measure()
                  return s.ptyCols === s.cols && s.shortByCells >= 0 && s.shortByCells < 1
                },
                { timeout: 3000 },
              )
              .toBe(true)
          })
          observations.geometry = await measure()
          await page.keyboard.type('keyQ7')
          await probe('typedText', async () => {
            await expect.poll(() => read(received), { timeout: 2000 }).toContain('keyQ7')
          })
          const target = await page.evaluate(() => {
            const grid = document.querySelector<HTMLElement>('.pane.active .terminal-live-grid')!
            const host = window as unknown as {
              __nocxPaneCellToPixel?: (x: number, y: number) => { x: number; y: number } | null
            }
            const pos = host.__nocxPaneCellToPixel?.(10, 10)
            if (!pos) return null
            const box = grid.getBoundingClientRect()
            const x = box.left + pos.x + 2,
              y = box.top + pos.y + 2
            const hit = document.elementFromPoint(x, y)
            return {
              x,
              y,
              onGrid: hit !== null && (hit === grid || grid.contains(hit)),
              hit: hit?.className ?? null,
            }
          })
          observations.target = target
          if (target) {
            await page.mouse.click(target.x, target.y)
            await probe('mousePress', async () => {
              await expect.poll(() => read(received), { timeout: 2000 }).toContain('\x1b[<0;11;11M')
            })
            await probe('mouseRelease', async () => {
              await expect.poll(() => read(received), { timeout: 2000 }).toContain('\x1b[<0;11;11m')
            })
          }
          await page.keyboard.press('ArrowDown')
          await probe('physicalKey', async () => {
            await expect.poll(() => read(received), { timeout: 2000 }).toContain('\x1b[B')
          })
        } catch (err) {
          error = String(err)
        } finally {
          record = {
            project: testInfo.project.name,
            timing,
            dpr,
            seconds: (Date.now() - started) / 1000,
            samples,
            observations,
            received: read(received),
            size: read(sample),
            error,
          }
          console.log('ALT-DIAG', JSON.stringify(record))
          await testInfo.attach('alt-diagnostic.json', {
            body: JSON.stringify(record, null, 2),
            contentType: 'application/json',
          })
          writeFileSync(exit, 'exit')
        }
        // This PR is a diagnostic, not a fix. Fail even on healthy cases
        // so CI prints the complete matrix beside its own failure context.
        throw new Error(`INTENTIONAL DIAGNOSTIC — NOT FOR MERGE: ${JSON.stringify(record)}`)
      })
    }
  })
}
