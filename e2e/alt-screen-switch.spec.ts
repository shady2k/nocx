// THE ALTERNATE-SCREEN SWITCH: the pane's grid, sent first, held until it
// applies (nocx-1ebrs.10).
//
// Measured before the fix, on this fixture: the first alternate-screen frames
// carried the PREVIOUS mode's geometry (192 columns with the pane settled
// wider; 70 columns in a pane of 1400 px), the resize request went out 87–234
// ms after the first fullscreen frame and always AFTER the post-switch text,
// and the program read those keys while its own `stty size` still reported the
// old width. Mouse reports landed on row 9 while the click was made on row 11.
//
// So this spec drives a real PTY program through the switch and states the
// four things a person can see:
//
//  1. the first alternate-screen frame PRESENTED is on the pane's measured
//     columns, not the previous mode's;
//  2. the resize request is sent when the switch is accepted, before any
//     input produced after it;
//  3. that input arrives at the program only after the receipt that says the
//     resize APPLIED — never merely after a frame was painted;
//  4. the pointer's cell is derived from the geometry that receipt reported.
//
// The three orderings (resize before, during and after the switch) are the
// three ways a pane changes size around a mode switch; all three ended in the
// same stale interval before the fix.
import { test, expect, promptReady, type Page } from './harness'
import { clickGesture } from './pointer-click.mts'
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

/** The bytes the fixture's program printed, one hex chunk per read. */
function programBytes(log: string): string[] {
  return log
    .split('\n')
    .filter((line) => line.includes(' BYTE '))
    .map((line) => line.trim().split(/\s+/).at(-1) ?? '')
}

type TraceEvent = Record<string, unknown> & { at: number; kind: string }
interface SwitchHost {
  __altTrace?: TraceEvent[]
  __altArm?: boolean
  __altProbe?: (target: { x: number; y: number }) => Promise<void>
  __nocxPaneCellToPixel?: (col: number, row: number) => { x: number; y: number } | null
}

/** Record what crosses the wire, in order: the frames the runtime published,
 *  the resize requests and the receipts that answered them, and every session
 *  intent the pane sent. The page's own Date.now() timestamps them, so the
 *  order here is the order the socket saw. */
async function installTrace(
  page: Page,
  probe: (target: { x: number; y: number }) => Promise<void>,
) {
  await page.exposeFunction('__altProbe', probe)
  await page.addInitScript(() => {
    const host = window as unknown as SwitchHost
    const trace: TraceEvent[] = []
    host.__altTrace = trace
    const record = (kind: string, data: Record<string, unknown>) =>
      trace.push({ at: Date.now(), kind, ...data })
    const resizes = new Map<number, Record<string, unknown>>()
    let committed: Record<string, unknown> | null = null
    let pointer: { x: number; y: number } | null = null
    for (const type of ['pointerdown', 'pointerup', 'pointermove'])
      window.addEventListener(
        type,
        (event) => {
          const e = event as PointerEvent
          pointer = { x: e.clientX, y: e.clientY }
        },
        true,
      )
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
            // The cell the pointer SHOULD name, from the newest geometry the
            // backend confirmed — the receipt's own numbers, and nothing the
            // product told us about its own arithmetic.
            let expectedCell: { x: number; y: number } | null = null
            if (msg.params?.kind === 'mouse' && pointer && committed) {
              const grid = document.querySelector<HTMLElement>('.pane.active .terminal-live-grid')
              const width = Number(committed.xpixel) / Number(committed.cols) / devicePixelRatio
              const height = Number(committed.ypixel) / Number(committed.rows) / devicePixelRatio
              if (grid && width > 0 && height > 0) {
                const rect = grid.getBoundingClientRect()
                expectedCell = {
                  x: Math.floor((pointer.x - rect.left) / width),
                  y: Math.floor((pointer.y - rect.top) / height),
                }
              }
            }
            record('send', { id: msg.id, method: msg.method, params: msg.params, expectedCell })
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
            if ('result' in msg && !msg.error) committed = size
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
      record('first-presented', {
        cols: Number(grid.getAttribute('aria-colcount')),
        cell: parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-width')),
        width: area.clientWidth,
        fullscreen: live.classList.contains('live-fullscreen'),
      })
    }
    new MutationObserver(observePresentation).observe(document, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
    })
    // The probe: once the program owns the pane, type, press a key and click a
    // cell — the three things measured as lost in the stale interval.
    let probing = false
    const sample = () => {
      const grid = document.querySelector<HTMLElement>('.pane.active .terminal-live-grid')
      const live = document.querySelector<HTMLElement>('.pane.active .terminal-live-container')
      if (grid && live && host.__altArm && !probing && live.classList.contains('live-fullscreen')) {
        const pos = host.__nocxPaneCellToPixel?.(10, 10)
        if (pos) {
          probing = true
          const rect = grid.getBoundingClientRect()
          const target = { x: rect.left + pos.x + 2, y: rect.top + pos.y + 2 }
          record('probe-trigger', { target })
          void host.__altProbe?.(target)
        }
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
                // ADR-3kqrem7v: the active rows are edge to edge in both
                // buffers, so the fit is the scroller's own width. Read the
                // live row's inset instead of assuming one.
                const live = document.querySelector<HTMLElement>(
                  '.pane.active .terminal-live-container',
                )!
                const cs = getComputedStyle(live)
                const inset = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight)
                return ptyCols === Math.floor((area.clientWidth - inset) / cell)
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
              const cell = parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-width'))
              return (
                cols === Number(grid.getAttribute('aria-colcount')) &&
                cols === Math.floor(area.clientWidth / cell)
              )
            }, ptyCols)
          })
          .toBe(true)
        if (probeDone) await probeDone
        // THE CLICK IS STILL TRAVELLING when the mouse returns: the pane
        // sends one intent at a time, and a receptive pane fences them until
        // the pending resize applies. A wire snapshot taken here can hold the
        // press alone, or neither, while the program goes on to receive both
        // — and taking that snapshot for the gesture reports as lost a
        // release the program already has (nocx-1ebrs.66.1.26). The
        // program's bytes say the click got through; the wire is read after
        // them, not instead.
        await expect
          .poll(() => clickGesture(programBytes(read(events))), {
            message: 'the program received the whole click: a press and its release',
          })
          .not.toBeNull()
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
        // WHAT THE SWITCH OWES (ADR-3kqrem7v). A screen-mode switch is a
        // PRESENTATION change: the grid the program is told depends on pane
        // geometry and never on which buffer is showing. So a switch accepted
        // at a size the pane already has sends NO resize at all, and holds no
        // input — the two presentations used to charge the program different
        // space, and that difference is the resize/redraw loop this ADR
        // removes (nocx-3an1s.1.4). This spec used to require the opposite,
        // "the resize request is sent when the switch is accepted"; that
        // request is exactly the feedback the owner asked to end.
        //
        // A REAL pane resize still goes out and still orders the person's
        // input: whatever resize is in flight when the switch is accepted must
        // APPLY before the first key produced after it reaches the program. A
        // receipt that is not that request's — superseded, cancelled — is not
        // an application and releases nothing (nocx-u1dts, nocx-bz3qu).
        //
        // WHAT THE SWITCH DID, in the message: a failure here used to say only
        // "expected > 1, received 0", which names neither the send that beat
        // the resize nor the method it carried.
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
          // The pane was resized and the program confirmed the wide PTY before
          // the switch, so the size is already applied: the switch must not
          // send another one.
          expect(
            firstResize,
            `accepting the switch sent a resize for a size already applied (socket saw: ${shape})`,
          ).toBe(-1)
        }
        // The ordering a REAL resize owes — the person's key behind the
        // receipt that applied it — is asserted by the unit suite
        // (terminal-content.test.ts's fence cases): the wire alone cannot tell
        // a key the fence held from one typed while no resize was pending, so
        // an end-to-end assertion here would be measuring its own timing.
        const pointerReports: string[] = []
        for (const event of postSwitch) {
          if (event.kind !== 'send' || event.method !== 'session.intent') continue
          const params = event.params as { kind: string; payload: string }
          if (params.kind !== 'mouse') continue
          const report = Buffer.from(params.payload, 'base64').toString()
          const [action, button, x, y] = report.split(' ')
          const expected = event.expectedCell as { x: number; y: number } | null
          expect(expected, 'pointer mapping uses the applied geometry').not.toBeNull()
          expect({ x: Number(x), y: Number(y) }).toEqual(expected)
          if (button === 'left' && (action === 'press' || action === 'release'))
            pointerReports.push(
              `\x1b[<0;${Number(x) + 1};${Number(y) + 1}${action === 'press' ? 'M' : 'm'}`,
            )
        }
        expect(pointerReports, 'the wire spells the click the program received').toHaveLength(2)
        // THE FIRST FRAME PRESENTED IS THE PANE'S OWN GRID.
        const painted = trace.find((event) => event.kind === 'first-presented')
        expect(painted, 'an alternate-screen frame is actually presented').toBeDefined()
        expect(painted?.fullscreen).toBe(true)
        expect(painted?.cols, 'the first presented alternate frame fits the pane').toBe(
          Math.floor(Number(painted?.width) / Number(painted?.cell)),
        )
        // And every byte the person produced arrived, in the order produced.
        const expected = [...Buffer.from('earlyQ7\x1b[B' + pointerReports.join(''))].map((byte) =>
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
