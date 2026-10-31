import { test, expect, promptReady } from './harness'
import { mkdtempSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import path from 'node:path'

test('one pane resize cannot feed a resize-borrow presentation loop', async ({ page }) => {
  const events: string[] = []
  page.on('console', (msg) => {
    if (msg.text().includes('lifecycle fact') || msg.text().includes('FLIPTRACE')) {
      const line = `${new Date().toISOString()} ${msg.text()}`
      events.push(line)
      console.log(line)
    }
  })
  await page.addInitScript(() => {
    const Native = window.WebSocket
    window.WebSocket = class extends Native {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols)
        this.addEventListener('message', (event) => {
          if (!(event.data instanceof ArrayBuffer)) return
          const bytes = new Uint8Array(event.data)
          try {
            const frame = JSON.parse(new TextDecoder().decode(bytes.slice(18)))
            if (frame.state?.bufferKind) {
              console.debug(
                'FLIPTRACE frame ' +
                  JSON.stringify({
                    revision: frame.revision,
                    state: frame.state,
                    rows: frame.rows?.map((r: { text: string }) => r.text),
                  }),
              )
            }
          } catch {
            /* Not a screen frame. */
          }
        })
      }
      override send(data: Parameters<WebSocket['send']>[0]): void {
        if (typeof data === 'string' && data.includes('resize'))
          console.debug('FLIPTRACE resize request ' + data)
        super.send(data)
      }
    }
    const seen = new WeakSet<Element>()
    const observe = () => {
      const live = document.querySelector('.pane.active .terminal-live-container')
      if (!live || seen.has(live)) return
      seen.add(live)
      const obs = new MutationObserver(() =>
        console.debug(
          'FLIPTRACE layout ' +
            JSON.stringify({
              mode: live.className,
              header:
                document.querySelector('.pane.active .cmd-header[data-borrow-header="same"]') !==
                null,
              headerHeight: document
                .querySelector('.pane.active .cmd-header[data-variant="running"]')
                ?.getBoundingClientRect().height,
              width: live.getBoundingClientRect().width,
              areaWidth: live.closest('.scrollback-area')?.clientWidth,
              areaScrollWidth: live.closest('.scrollback-area')?.scrollWidth,
              paneWidth: live.closest('.pane')?.clientWidth,
              paneScrollWidth: live.closest('.pane')?.scrollWidth,
              composer: !!document.querySelector<HTMLElement>('.pane.active .nocx-editor')
                ?.offsetHeight,
            }),
        ),
      )
      obs.observe(live, { attributes: true, attributeFilter: ['class'] })
    }
    window.addEventListener('load', () => {
      new MutationObserver(observe).observe(document.documentElement, {
        childList: true,
        subtree: true,
      })
      observe()
    })
  })
  await page.goto('/')
  await promptReady(page)
  const dir = mkdtempSync(join(tmpdir(), 'nocx-flip-'))
  const script = path.resolve(__dirname, 'fixtures', 'resize-borrow.mjs')
  const trace = join(dir, 'trace.jsonl')
  await page.keyboard.type(`node ${script} ${trace}`)
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .terminal-live-grid')).toContainText('OMP-BANNER')
  const header = page.locator('.pane.active .cmd-header[data-variant="running"]')
  await expect(header).toHaveCount(1)
  await expect(page.locator('.pane.active .ui-process-bar:visible')).toHaveCount(0)
  await expect(header).toHaveCSS('height', '32px')
  await header.evaluate((el) => el.setAttribute('data-borrow-header', 'same'))
  // Exercise the physical seat used by an assistant-owned command without
  // configuring a model: a genuine manager claim is covered by controller
  // units; the browser owns overflow/column visibility, which jsdom cannot.
  await page.locator('.pane.active .cmd-block-running').evaluate((block) => {
    const turn = document.createElement('div')
    turn.className = 'cmd-block'
    turn.dataset.nestedParity = 'turn'
    const children = document.createElement('div')
    children.className = 'cmd-children'
    block.before(turn)
    turn.append(children)
    children.append(block)
  })
  const nestedGeometry = () =>
    page.locator('.pane.active').evaluate((pane) => {
      const area = pane.querySelector<HTMLElement>('.scrollback-area')!
      const live = pane.querySelector<HTMLElement>('.terminal-live-container')!
      const grid = live.getBoundingClientRect()
      const box = area.getBoundingClientRect()
      return {
        areaWidth: area.clientWidth,
        areaScrollWidth: area.scrollWidth,
        paneWidth: pane.clientWidth,
        paneScrollWidth: pane.scrollWidth,
        gridWidth: grid.width,
        gridLeft: grid.left,
        areaLeft: box.left,
      }
    })
  await expect
    .poll(async () => (await nestedGeometry()).gridWidth)
    .toBe((await nestedGeometry()).areaWidth)
  const primaryGeometry = await nestedGeometry()
  expect(primaryGeometry.areaScrollWidth).toBe(primaryGeometry.areaWidth)
  expect(primaryGeometry.paneScrollWidth).toBe(primaryGeometry.paneWidth)
  expect(primaryGeometry.gridLeft).toBe(primaryGeometry.areaLeft)
  const observationStart = events.length
  console.log('FLIPTRACE RESIZE TRIGGER')
  await page.setViewportSize({ width: 1100, height: 800 })
  await expect.poll(() => readFileSync(trace, 'utf8')).toContain('WORKLOAD-DONE')
  await expect.poll(() => readFileSync(trace, 'utf8')).toContain('1049l')
  await expect(page.locator('.pane.active .terminal-live-container')).toHaveClass(/live-running/)
  console.log('FLIPTRACE PROGRAM ' + readFileSync(trace, 'utf8'))
  console.log(
    'FLIPTRACE FINAL ' +
      JSON.stringify(
        await page.locator('.pane.active .terminal-live-container').getAttribute('class'),
      ),
  )
  await expect(header).toHaveAttribute('data-borrow-header', 'same')
  await expect(header).toHaveCSS('height', '32px')
  await expect(page.locator('.pane.active .ui-process-bar:visible')).toHaveCount(0)
  const observation = events.slice(observationStart)
  await page.keyboard.type('x')
  await promptReady(page)
  const programEvents = readFileSync(trace, 'utf8')
    .trim()
    .split('\n')
    .map((line) => JSON.parse(line) as { kind: string })
  const sigwinches = programEvents.filter((event) => event.kind === 'SIGWINCH')
  const resizes = observation.filter((line) => line.includes('FLIPTRACE resize request'))
  const modes = observation.filter((line) => line.includes('FLIPTRACE layout'))
  const enters = programEvents.filter((event) => event.kind === '1049h').length
  const leaves = programEvents.filter((event) => event.kind === '1049l').length
  const counts = `resize RPCs=${resizes.length}, SIGWINCH=${sigwinches.length}, presentation changes=${modes.length}, alternate enters=${enters}, leaves=${leaves}`
  console.log('FLIPTRACE COUNTS ' + counts)
  console.log('FLIPTRACE PRIMARY GEOMETRY ' + JSON.stringify(primaryGeometry))
  const facts = observation.filter((line) => line.includes('lifecycle fact'))
  const output = await page.locator('.pane.active .cmd-output').allTextContents()
  const matches = output.flatMap((text) =>
    text.split('\n').filter((line) => line.includes('OMP-BANNER')),
  )
  const occurrences = output.reduce(
    (count, text) => count + (text.match(/OMP-BANNER/g)?.length ?? 0),
    0,
  )
  console.log('FLIPTRACE BANNER ' + JSON.stringify({ occurrences, matches }))
  expect
    .soft(
      occurrences,
      `${counts}; banner occurs ${occurrences} times in block output: ${JSON.stringify(matches)}`,
    )
    .toBeLessThanOrEqual(1)
  expect
    .soft(resizes.length, `${counts}; requests: ${JSON.stringify(resizes)}`)
    .toBeLessThanOrEqual(1)
  expect.soft(sigwinches.length, `${counts}; PTY events: ${JSON.stringify(programEvents)}`).toBe(1)
  const layoutStates = modes.map(
    (line) =>
      JSON.parse(line.slice(line.indexOf('{'))) as {
        header: boolean
        headerHeight: number
        width: number
        areaWidth: number
        areaScrollWidth: number
        paneWidth: number
        paneScrollWidth: number
      },
  )
  expect
    .soft(
      layoutStates.every(
        (state) =>
          state.header &&
          state.headerHeight === 32 &&
          state.width === state.areaWidth &&
          state.areaScrollWidth === state.areaWidth &&
          state.paneScrollWidth === state.paneWidth,
      ),
      JSON.stringify(layoutStates),
    )
    .toBe(true)
  expect.soft(modes.length, `${counts}; layout: ${JSON.stringify(modes)}`).toBeLessThanOrEqual(2)
  expect.soft(enters, counts).toBeLessThanOrEqual(1)
  expect.soft(leaves, counts).toBeLessThanOrEqual(1)
  expect(
    facts,
    `lifecycle must stay running during resize borrow: ${JSON.stringify(facts)}`,
  ).toEqual([])
})

test('running header controls keep keyboard navigation separate from program input', async ({
  page,
}) => {
  await page.goto('/')
  await promptReady(page)
  const dir = mkdtempSync(join(tmpdir(), 'nocx-flip-keys-'))
  const trace = join(dir, 'trace.jsonl')
  const script = path.resolve(__dirname, 'fixtures', 'resize-borrow.mjs')
  await page.keyboard.type(`node ${script} ${trace}`)
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .terminal-live-grid')).toContainText('OMP-BANNER')
  const header = page.locator('.pane.active .cmd-header[data-variant="running"]')
  const interrupt = header.getByRole('button', { name: 'Interrupt', exact: true })
  const stop = header.getByRole('button', { name: 'Stop', exact: true })
  // Tab in PROGRAM remains an actual terminal input, not browser navigation.
  await page.keyboard.press('Tab')
  await expect.poll(() => readFileSync(trace, 'utf8')).toContain('INPUT:09')
  await page.keyboard.press('Alt+F10')
  await expect(interrupt).toBeFocused()
  await page.keyboard.press('Tab')
  await expect(stop).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await expect(interrupt).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(interrupt).not.toBeFocused()
  // Header Escape did not leak to the application; a subsequent Tab did.
  await page.keyboard.press('Tab')
  await expect.poll(() => (readFileSync(trace, 'utf8').match(/INPUT:09/g) ?? []).length).toBe(2)
  expect(readFileSync(trace, 'utf8')).not.toContain('INPUT:1b')
  await page.keyboard.press('Alt+F10')
  await expect(interrupt).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await expect(interrupt).not.toBeFocused()
  await page.keyboard.press('Alt+F10')
  await page.keyboard.press('Enter')
  await expect.poll(() => readFileSync(trace, 'utf8')).toContain('SIGINT')
  await expect(interrupt).not.toBeFocused()
  // Raw mode disables ISIG: keyboard Ctrl+C is byte 0x03, not an OS
  // signal. Header Interrupt above is the explicit signaling action. A
  // terminal must not turn the program's raw input into another SIGINT.
  await page.keyboard.press('Control+c')
  await expect.poll(() => readFileSync(trace, 'utf8')).toContain('INPUT:03')
  expect((readFileSync(trace, 'utf8').match(/"SIGINT"/g) ?? []).length).toBe(1)
  await page.keyboard.press('Alt+F10')
  await page.keyboard.press('Tab')
  await expect(stop).toBeFocused()
  await page.keyboard.press('Space')
  await promptReady(page)
  await expect(header).toHaveCount(0)
})
