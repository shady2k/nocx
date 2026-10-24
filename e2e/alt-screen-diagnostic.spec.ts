import { test, expect, promptReady, type Page } from './harness'
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

type TraceEvent = Record<string, unknown> & { at: number; kind: string }
interface DiagnosticHost {
  __altTrace?: TraceEvent[]
  __altArm?: boolean
  __altProbe?: (target: { x: number; y: number }) => Promise<void>
  __nocxPaneCellToPixel?: (x: number, y: number) => { x: number; y: number } | null
}

async function installTrace(
  page: Page,
  probe: (target: { x: number; y: number }) => Promise<void>,
) {
  await page.exposeFunction('__altProbe', probe)
  await page.addInitScript(() => {
    const host = window as unknown as DiagnosticHost
    const trace: TraceEvent[] = []
    host.__altTrace = trace
    const record = (kind: string, data: Record<string, unknown>) =>
      trace.push({ at: Date.now(), kind, ...data })
    const send = WebSocket.prototype.send
    WebSocket.prototype.send = function (data) {
      if (typeof data === 'string') {
        try {
          const msg = JSON.parse(data) as { method?: string; params?: Record<string, unknown> }
          if (msg.method === 'resize' || msg.method === 'session.intent')
            record('send', { method: msg.method, params: msg.params })
        } catch {
          /* binary and unrelated traffic are not this trace */
        }
      }
      return send.call(this, data)
    }
    const add = WebSocket.prototype.addEventListener
    const observed = new WeakSet<WebSocket>()
    WebSocket.prototype.addEventListener = function (
      this: WebSocket,
      type: string,
      listener: EventListenerOrEventListenerObject,
      options?: boolean | AddEventListenerOptions,
    ) {
      if (!observed.has(this)) {
        observed.add(this)
        add.call(this, 'message', (event: Event) => {
          const raw = (event as MessageEvent).data
          if (!(raw instanceof ArrayBuffer)) return
          const bytes = new Uint8Array(raw)
          if (bytes[0] !== 1 || bytes[1] !== 2) return
          try {
            const frame = JSON.parse(new TextDecoder().decode(bytes.subarray(18))) as {
              revision?: number
              geometry?: Record<string, unknown>
              state?: { bufferKind?: string; mouseTracking?: boolean }
            }
            record('frame', {
              revision: frame.revision,
              geometry: frame.geometry,
              state: frame.state,
            })
          } catch {
            /* the product owns validation */
          }
        })
      }
      return add.call(this, type, listener, options)
    }
    let previous = ''
    let probing = false
    const sample = () => {
      const grid = document.querySelector<HTMLElement>('.pane.active .terminal-live-grid')
      const area = document.querySelector<HTMLElement>('.pane.active .scrollback-area')
      const live = document.querySelector<HTMLElement>('.pane.active .terminal-live-container')
      const inner = document.querySelector<HTMLElement>('.pane.active .scrollback-inner')
      if (grid && area && live && inner) {
        const cols = Number(grid.getAttribute('aria-colcount'))
        const cell = parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-width'))
        const fullscreen = live.classList.contains('live-fullscreen')
        const value = JSON.stringify({
          cols,
          cell,
          width: area.clientWidth,
          fullscreen,
          focus: document.activeElement?.className,
        })
        if (value !== previous) {
          previous = value
          record('dom', JSON.parse(value))
        }
        if (host.__altArm && fullscreen && !probing) {
          const pos = host.__nocxPaneCellToPixel?.(10, 10)
          if (pos) {
            probing = true
            const rect = grid.getBoundingClientRect()
            const target = { x: rect.left + pos.x + 2, y: rect.top + pos.y + 2 }
            record('probe-trigger', { cols, width: area.clientWidth, cell, target })
            void host.__altProbe?.(target)
          }
        }
      }
      requestAnimationFrame(sample)
    }
    requestAnimationFrame(sample)
  })
}

for (const timing of ['before', 'during', 'after']) {
  test.describe(`timestamped alternate-screen diagnosis: ${timing}`, () => {
    test.use({ deviceScaleFactor: 2 })
    test('size and input at the switch', async ({ page }, testInfo) => {
      const started = Date.now()
      const root = mkdtempSync(join(tmpdir(), 'nocx-alt-timed-'))
      const gate = join(root, 'switch'),
        exit = join(root, 'exit')
      const sample = join(root, 'size'),
        events = join(root, 'events')
      const script = join(root, 'fixture.sh')
      const observations: Record<string, unknown> = {}
      let error = ''
      let record: Record<string, unknown> | null = null
      let probeDone: Promise<void> | null = null
      await installTrace(page, (target) => {
        probeDone = (async () => {
          observations.probeStarted = Date.now()
          await page.keyboard.type('earlyQ7')
          await page.keyboard.press('ArrowDown')
          await page.mouse.click(target.x, target.y)
          observations.probeFinished = Date.now()
        })()
        return probeDone
      })
      writeFileSync(
        script,
        [
          '#!/bin/bash',
          'old=$(stty -g)',
          `cleanup() { stty "$old"; printf '\\033[?1000l\\033[?1006l\\033[?1049l'; }`,
          'trap cleanup EXIT',
          `log() { printf '%s %s %s %s\\n' "$EPOCHREALTIME" "$1" "$(stty size)" "\${2-}" >> '${events}'; }`,
          `printf 'WAITING-FOR-SWITCH\\n'`,
          `while [ ! -e '${gate}' ] && [ ! -e '${exit}' ]; do stty size > '${sample}.tmp'; mv '${sample}.tmp' '${sample}'; sleep 0.01; done`,
          'stty raw -echo',
          'log SWITCH',
          `printf '\\033[?1049h\\033[?1000h\\033[?1006h\\033[2J\\033[5;1HALT-READY'`,
          'last=',
          `while [ ! -e '${exit}' ]; do`,
          '  size=$(stty size)',
          `  if [ "$size" != "$last" ]; then log SIZE; last="$size"; fi`,
          `  printf '%s\\n' "$size" > '${sample}.tmp'; mv '${sample}.tmp' '${sample}'`,
          `  if IFS= read -r -N 1 -t 0.005 key; then hex=$(printf '%s' "$key" | od -An -tx1 | tr -d ' \\n'); log BYTE "$hex"; fi`,
          'done',
          '',
        ]
          .join('\n')
          .replaceAll('\\\\033', '\\033')
          .replaceAll('\\\\n', '\\n'),
      )
      try {
        await page.setViewportSize({ width: 850, height: 800 })
        await page.goto('/')
        await promptReady(page)
        await page.keyboard.type(`bash '${script}'`)
        await page.keyboard.press('Enter')
        await expect(page.locator('.pane.active .terminal-live-container')).toContainText(
          'WAITING-FOR-SWITCH',
        )
        if (timing === 'before') {
          await page.setViewportSize({ width: 1700, height: 800 })
          // A real prerequisite, not the queued resize the first diagnostic
          // accidentally called 'before': the program confirms the wide PTY.
          await expect
            .poll(async () => {
              const cols = Number(read(sample).trim().split(/\s+/)[1])
              return page.evaluate((ptyCols) => {
                const area = document.querySelector<HTMLElement>('.pane.active .scrollback-area')!
                const inner = document.querySelector<HTMLElement>('.pane.active .scrollback-inner')!
                const cell = parseFloat(
                  getComputedStyle(inner).getPropertyValue('--term-cell-width'),
                )
                // Normal-buffer live rows carry the 48px prose inset.
                return ptyCols === Math.floor((area.clientWidth - 48) / cell)
              }, cols)
            })
            .toBe(true)
        }
        await page.evaluate(() => {
          ;(window as unknown as DiagnosticHost).__altArm = true
        })
        observations.gateAt = Date.now()
        if (timing === 'during') {
          await Promise.all([
            page.setViewportSize({ width: 1700, height: 800 }),
            Promise.resolve().then(() => writeFileSync(gate, 'switch')),
          ])
        } else {
          writeFileSync(gate, 'switch')
          if (timing === 'after') {
            await expect(page.locator('.pane.active .terminal-live-container')).toHaveClass(
              /live-fullscreen/,
            )
            observations.resizeAfterAt = Date.now()
            await page.setViewportSize({ width: 1700, height: 800 })
          }
        }
        await expect.poll(() => read(events)).toContain('BYTE')
        await expect
          .poll(async () => {
            const ptyCols = Number(read(sample).trim().split(/\s+/)[1])
            return page.evaluate((cols) => {
              const grid = document.querySelector<HTMLElement>('.pane.active .terminal-live-grid')!
              const area = document.querySelector<HTMLElement>('.pane.active .scrollback-area')!
              const inner = document.querySelector<HTMLElement>('.pane.active .scrollback-inner')!
              const cell = parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-width'))
              return (
                cols === Number(grid.getAttribute('aria-colcount')) &&
                cols === Math.floor(area.clientWidth / cell)
              )
            }, ptyCols)
          })
          .toBe(true)
        if (probeDone) await probeDone
        const expected = [...Buffer.from('earlyQ7\x1b[B\x1b[<0;11;11M\x1b[<0;11;11m')].map((byte) =>
          byte.toString(16).padStart(2, '0'),
        )
        await expect
          .poll(() =>
            read(events)
              .split('\n')
              .filter((line) => line.includes(' BYTE '))
              .map((line) => line.trim().split(/\s+/).at(-1)),
          )
          .toEqual(expected)
        observations.receiptsComplete = true
      } catch (err) {
        error = String(err)
      } finally {
        record = {
          project: testInfo.project.name,
          timing,
          seconds: (Date.now() - started) / 1000,
          observations,
          events: read(events),
          size: read(sample),
          error,
          trace: await page.evaluate(() => (window as unknown as DiagnosticHost).__altTrace ?? []),
        }
        console.log('ALT-TIMED', JSON.stringify(record))
        await testInfo.attach('alt-timed.json', {
          body: JSON.stringify(record, null, 2),
          contentType: 'application/json',
        })
        writeFileSync(exit, 'exit')
      }
      throw new Error(`INTENTIONAL TIMED DIAGNOSTIC — NOT FOR MERGE: ${JSON.stringify(record)}`)
    })
  })
}
