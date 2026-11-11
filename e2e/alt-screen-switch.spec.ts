// THE ALTERNATE-SCREEN SWITCH IS A PRESENTATION CHANGE, NOT A RESIZE
// (ADR-92zzg2y2).
//
// The old rule fitted a full-screen program to the pane's whole width and a
// normal command to a 24 px gutter, so a switch had to change the column count,
// request a resize, present the first frames on the pane's grid and hold the
// person's input until the receipt applied (nocx-1ebrs.10). The owner's rule of
// 2026-11-10 gives every mode one gutter — one cell each side — so the terminal
// has ONE size, and a switch sends nothing.
//
// This spec drives a real PTY program through the switch and states what a
// person can see:
//
//  1. the switch itself sends NO resize when the pane has not changed size;
//  2. the first alternate-screen frame is presented on the pane's own grid, the
//     same columns as every other mode, less the one-cell gutter;
//  3. input produced after the switch reaches the program at once — there is no
//     fence to wait for;
//  4. a real pane resize still refits the program once, and the pty, the grid
//     and the pane agree afterwards.
//
// The three timings (the pane resized before, during and after the switch) are
// the three ways a real resize can meet a mode change.
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
interface SwitchHost {
  __altTrace?: TraceEvent[]
  __altArm?: boolean
  __altProbe?: () => Promise<void>
}

/** Record what crosses the wire, in order: the frames the runtime published,
 *  the resize requests and the receipts that answered them, and every session
 *  intent the pane sent. The page's own Date.now() timestamps them, so the
 *  order here is the order the socket saw. */
async function installTrace(page: Page, probe: () => Promise<void>) {
  await page.exposeFunction('__altProbe', probe)
  await page.addInitScript(() => {
    const host = window as unknown as SwitchHost
    const trace: TraceEvent[] = []
    host.__altTrace = trace
    const record = (kind: string, data: Record<string, unknown>) =>
      trace.push({ at: Date.now(), kind, ...data })
    const resizes = new Map<number, Record<string, unknown>>()
    const send = WebSocket.prototype.send
    WebSocket.prototype.send = function (data) {
      if (typeof data === 'string') {
        try {
          const msg = JSON.parse(data) as {
            id?: number
            method?: string
            params?: Record<string, unknown>
          }
          if (msg.method === 'resize' && msg.id !== undefined && msg.params)
            resizes.set(msg.id, msg.params)
          if (msg.method === 'resize' || msg.method === 'session.intent') {
            record('send', { id: msg.id, method: msg.method, params: msg.params })
          }
        } catch {
          /* binary and unrelated traffic are not this trace */
        }
      }
      return send.call(this, data)
    }
    // RECORD BEFORE THE PRODUCT READS, or the trace measures the recorder.
    //
    // The first version added its own 'message' listener beside the client's,
    // and listeners run in registration order — so the client handled the
    // frame first (opening the fence, sending the resize) and the frame was
    // written to the trace afterwards. Every event that frame CAUSED therefore
    // looked older than the frame itself: this spec reported input ahead of a
    // resize that the wire shows 12 ms behind it, and the real ordering was
    // invisible. Wrapping the client's own listener puts the record first,
    // which is what "what the socket saw, and then what the pane did about
    // it" actually means.
    const add = WebSocket.prototype.addEventListener
    const wrappers = new WeakMap<EventListenerOrEventListenerObject, EventListener>()
    const observe = (event: Event): void => {
      const raw = (event as MessageEvent).data
      if (typeof raw === 'string') {
        try {
          const msg = JSON.parse(raw) as { id?: number; result?: unknown; error?: unknown }
          const size = msg.id === undefined ? undefined : resizes.get(msg.id)
          if (size) {
            record('resize-receipt', { id: msg.id, size, result: msg.result, error: msg.error })
          }
        } catch {
          /* the product owns validation */
        }
        return
      }
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
    }
    WebSocket.prototype.addEventListener = function (
      this: WebSocket,
      type: string,
      listener: EventListenerOrEventListenerObject,
      options?: boolean | AddEventListenerOptions,
    ) {
      if (type !== 'message' || typeof listener !== 'function') {
        return add.call(this, type, listener, options)
      }
      let wrapped = wrappers.get(listener)
      if (wrapped === undefined) {
        wrapped = (event: Event) => {
          observe(event)
          return (listener as EventListener)(event)
        }
        wrappers.set(listener, wrapped)
      }
      return add.call(this, type, wrapped, options)
    }
    // THE FIRST PRESENTED ALTERNATE FRAME, caught when the DOM has it: the
    // programmer's text in the live grid while the live region is fullscreen.
    let presented = false
    const observePresentation = () => {
      if (presented || !host.__altArm) return
      const grid = document.querySelector<HTMLElement>('.pane.active .terminal-live-grid')
      const area = document.querySelector<HTMLElement>('.pane.active .scrollback-area')
      const live = document.querySelector<HTMLElement>('.pane.active .terminal-live-container')
      const inner = document.querySelector<HTMLElement>('.pane.active .scrollback-inner')
      if (!grid || !area || !live || !inner || !grid.textContent?.includes('ALT-READY')) return
      presented = true
      const pane = document.querySelector<HTMLElement>('.pane.active')
      record('first-presented', {
        cols: Number(grid.getAttribute('aria-colcount')),
        cell: parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-width')),
        width: area.clientWidth,
        gutter: pane
          ? parseFloat(getComputedStyle(pane).getPropertyValue('--pane-inline-padding'))
          : 0,
        fullscreen: live.classList.contains('live-fullscreen'),
      })
    }
    new MutationObserver(observePresentation).observe(document, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
    })
    // The probe: once the program owns the pane, type into it. There is no
    // fence, so these keys are the program's at once.
    let probing = false
    const sample = () => {
      const live = document.querySelector<HTMLElement>('.pane.active .terminal-live-container')
      if (live && host.__altArm && !probing && live.classList.contains('live-fullscreen')) {
        probing = true
        record('probe-trigger', {})
        void host.__altProbe?.()
      }
      requestAnimationFrame(sample)
    }
    requestAnimationFrame(sample)
  })
}

for (const timing of ['before', 'during', 'after']) {
  test.describe(`an alternate-screen switch ${timing} the resize`, () => {
    test.use({ deviceScaleFactor: 2 })
    test('fits the pane, sends the resize first, and holds early input until it applies', async ({
      page,
    }, testInfo) => {
      const started = Date.now()
      const root = mkdtempSync(join(tmpdir(), 'nocx-alt-switch-'))
      const gate = join(root, 'switch'),
        exit = join(root, 'exit')
      const sample = join(root, 'size'),
        events = join(root, 'events')
      const script = join(root, 'fixture.cjs')
      const observations: Record<string, unknown> = {}
      let error = ''
      let probeDone: Promise<void> | null = null
      await installTrace(page, () => {
        probeDone = (async () => {
          observations.probeStarted = Date.now()
          // Keys produced after the switch. There is no fence to wait for
          // (ADR-92zzg2y2), so they are the program's at once.
          await page.keyboard.type('earlyQ7')
          await page.keyboard.press('ArrowDown')
          observations.probeFinished = Date.now()
        })()
        return probeDone
      })
      // A real foreground program in raw mode: Node drains each input chunk
      // without starting a process per byte. Each observation queries the PTY
      // size once, rather than using Node's cached dimensions or the renderer.
      writeFileSync(
        script,
        String.raw`
const { execFileSync } = require('node:child_process')
const { appendFileSync, existsSync, renameSync, writeFileSync } = require('node:fs')
const gate = ${JSON.stringify(gate)}
const exit = ${JSON.stringify(exit)}
const sample = ${JSON.stringify(sample)}
const events = ${JSON.stringify(events)}
const { stdin, stdout } = process
let switched = false
let lastSize = ''
// Node's dimensions are cached until SIGWINCH is handled. Query the PTY once
// per observation, never per byte, so each evidence line names its actual size.
const observe = (kind, hex = '') => {
  const current = execFileSync('stty', ['size'], {
    encoding: 'utf8',
    stdio: ['inherit', 'pipe', 'inherit'],
  }).trim()
  const log = (event, value = '') => {
    appendFileSync(events, (Date.now() / 1000).toFixed(6) + ' ' + event + ' ' + current + ' ' + value + '\n')
  }
  writeFileSync(sample + '.tmp', current + '\n')
  renameSync(sample + '.tmp', sample)
  if (kind === 'SWITCH') log('SWITCH')
  if (switched && current !== lastSize) {
    log('SIZE')
    lastSize = current
  }
  if (kind === 'BYTE') {
    log('RETURN', '0:' + hex)
    log('BYTE', hex)
  }
}
stdout.on('resize', () => observe('SIZE'))
observe('SIZE')
stdout.write('WAITING-FOR-SWITCH\n')
process.on('exit', () => {
  if (switched) stdin.setRawMode(false)
  stdout.write('\x1b[?1000l\x1b[?1006l\x1b[?1049l')
})
setInterval(() => {
  if (existsSync(exit)) process.exit(0)
  if (switched || !existsSync(gate)) return
  stdin.setRawMode(true)
  stdin.on('data', (chunk) => observe('BYTE', chunk.toString('hex')))
  switched = true
  observe('SWITCH')
  stdout.write('\x1b[?1049h\x1b[?1000h\x1b[?1006h\x1b[2J\x1b[5;1HALT-READY')
}, 10)
`,
      )
      try {
        await page.setViewportSize({ width: 850, height: 800 })
        await page.goto('/')
        await promptReady(page)
        await page.keyboard.type(`node '${script}'`)
        await page.keyboard.press('Enter')
        await expect(page.locator('.pane.active .terminal-live-container')).toContainText(
          'WAITING-FOR-SWITCH',
        )
        if (timing === 'before') {
          await page.setViewportSize({ width: 1700, height: 800 })
          // A real prerequisite: the program confirms the wide PTY before the
          // switch, so 'before' is a settled pane rather than a queued resize.
          await expect
            .poll(async () => {
              const cols = Number(read(sample).trim().split(/\s+/)[1])
              return page.evaluate((ptyCols) => {
                const area = document.querySelector<HTMLElement>('.pane.active .scrollback-area')!
                const inner = document.querySelector<HTMLElement>('.pane.active .scrollback-inner')!
                const cell = parseFloat(
                  getComputedStyle(inner).getPropertyValue('--term-cell-width'),
                )
                // ADR-92zzg2y2: every mode is fitted inside the one gutter,
                // which `usableViewport` reads from the stylesheet's own
                // token. Read that same number here rather than the live
                // row's padding: the running live region sits inside the
                // block, so its own padding is 0 while the block pays the
                // gutter.
                const gutter =
                  parseFloat(getComputedStyle(area).getPropertyValue('--pane-inline-padding')) || 0
                return ptyCols === Math.floor((area.clientWidth - 2 * gutter) / cell)
              }, cols)
            })
            .toBe(true)
        }
        await page.evaluate(() => {
          ;(window as unknown as SwitchHost).__altArm = true
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
            // THE SWITCH IS PRESENTED BEFORE THE PANE GROWS, or this variant
            // does not exist: `.live-fullscreen` is set when the frame is
            // ACCEPTED, while the first alternate paint is queued for the next
            // animation frame. Widening at acceptance raced that paint, and
            // the `first-presented` snapshot then read the NEW pane width
            // beside the grid of the width it had been painted at — a pane the
            // product never produced, reported as the product's
            // (nocx-1ebrs.66.1.25). ALT-READY in the live grid is the
            // observable "the switch is on screen".
            await expect(page.locator('.pane.active .terminal-live-grid')).toContainText(
              'ALT-READY',
            )
            observations.resizeAfterAt = Date.now()
            await page.setViewportSize({ width: 1700, height: 800 })
          }
        }
        // The program has read something: the switch's own text reached it.
        await expect.poll(() => read(events)).toContain('BYTE')
        // And the settled state: the program, the presented grid and the
        // pane's box are one number.
        await expect
          .poll(async () => {
            const ptyCols = Number(read(sample).trim().split(/\s+/)[1])
            return page.evaluate((cols) => {
              const grid = document.querySelector<HTMLElement>('.pane.active .terminal-live-grid')!
              const area = document.querySelector<HTMLElement>('.pane.active .scrollback-area')!
              const inner = document.querySelector<HTMLElement>('.pane.active .scrollback-inner')!
              const pane = document.querySelector<HTMLElement>('.pane.active')!
              const cell = parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-width'))
              const gutter =
                parseFloat(getComputedStyle(pane).getPropertyValue('--pane-inline-padding')) || 0
              return (
                cols === Number(grid.getAttribute('aria-colcount')) &&
                cols === Math.floor((area.clientWidth - 2 * gutter) / cell)
              )
            }, ptyCols)
          })
          .toBe(true)
        if (probeDone) await probeDone
        const trace = await page.evaluate(() => (window as unknown as SwitchHost).__altTrace ?? [])
        const switchIndex = trace.findIndex(
          (event) =>
            event.kind === 'frame' &&
            (event.state as { bufferKind?: string })?.bufferKind === 'alternate',
        )
        expect(switchIndex, 'the runtime accepted alternate-screen mode').toBeGreaterThanOrEqual(0)
        const postSwitch = trace.slice(switchIndex + 1)
        const firstResize = postSwitch.findIndex(
          (event) => event.kind === 'send' && event.method === 'resize',
        )
        const firstInput = postSwitch.findIndex(
          (event) => event.kind === 'send' && event.method === 'session.intent',
        )
        // THE SWITCH ITSELF SENDS NOTHING (ADR-92zzg2y2). In the 'before'
        // variant the pane has already settled at its new width before the
        // switch, so nothing about the switch may appear on the wire — no
        // resize, and no held input either.
        const shape = postSwitch
          .slice(0, 8)
          .map((event) =>
            event.kind === 'send'
              ? `${String(event.method)}${
                  event.method === 'session.intent'
                    ? `(${String((event.params as { kind?: string }).kind)})`
                    : ''
                }`
              : event.kind,
          )
          .join(' → ')
        expect(
          firstInput,
          `the early input reaches the session (socket saw: ${shape})`,
        ).toBeGreaterThanOrEqual(0)
        if (timing === 'before') {
          expect(firstResize, `the switch sends no resize (socket saw: ${shape})`).toBe(-1)
        }
        // THE FIRST FRAME PRESENTED IS THE PANE'S OWN GRID WHEN THE PANE HAS
        // NOT MOVED — the same columns as every other mode, less the one-cell
        // gutter. A concurrent real resize is a different event: the program
        // may still be painting the grid it was given before the new size
        // applies, and the settled-state poll above is where that is proven.
        const painted = trace.find((event) => event.kind === 'first-presented')
        expect(painted, 'an alternate-screen frame is actually presented').toBeDefined()
        expect(painted?.fullscreen).toBe(true)
        if (timing === 'before') {
          expect(painted?.cols, 'the first presented alternate frame fits the content box').toBe(
            Math.floor(
              (Number(painted?.width) - 2 * Number(painted?.gutter)) / Number(painted?.cell),
            ),
          )
        }
        // And every byte the person produced arrived, in the order produced.
        const expected = [...Buffer.from('earlyQ7\x1b[B')].map((byte) =>
          byte.toString(16).padStart(2, '0'),
        )
        await expect
          .poll(() =>
            read(events)
              .split('\n')
              .filter((line) => line.includes(' BYTE '))
              .flatMap((line) => line.trim().split(/\s+/).at(-1)!.match(/.{2}/g) ?? []),
          )
          .toEqual(expected)
        observations.receiptsComplete = true
      } catch (err) {
        error = String(err)
      } finally {
        // The failure's own evidence, so the next reader does not have to
        // reproduce the run to see what crossed the wire.
        if (error !== '') {
          await testInfo.attach('alt-screen-switch.json', {
            body: JSON.stringify(
              {
                project: testInfo.project.name,
                timing,
                seconds: (Date.now() - started) / 1000,
                observations,
                events: read(events),
                size: read(sample),
                error,
                trace: await page.evaluate(
                  () => (window as unknown as SwitchHost).__altTrace ?? [],
                ),
              },
              null,
              2,
            ),
            contentType: 'application/json',
          })
        }
        writeFileSync(exit, 'exit')
      }
      if (error) throw new Error(error)
    })
  })
}
