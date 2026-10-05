// e2e: the live tier's scrollback surface (nocx-zg3k3.10.4).
//
// Five clauses, driven through the real UI against the real backend, in a
// session with NO shell integration: the fixture sshd opened with
// desiredMode raw, so no launcher runs, no OSC 133 ever arrives, and the
// unstructured mode owns the pane for the whole test. The rows the reader
// scrolls into view come from the backend's session.historyPage — the
// emulator's own scrollback — painted by the same painter as the live
// screen. Every wait is on observable state: the pane's own screen model,
// the surface's painted pages, the scroller's position, and the real
// control-plane op where a backend fact is the readiness gate.
import { test, expect, appReadyForInput, openControlPlane, resolveBackend } from './harness'
import { readStand } from './stand'
import { startSshd, rpc } from './sshd-fixture'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

// window.__nocxPaneScreen is declared once for the e2e project, in
// screen-plane-reaches-the-pane.spec.ts (PaneScreenReading); this spec
// reads the same seam and needs no declaration of its own.

const LINES = 200
/** The fixture shell prints zero-padded labels, so no label is a prefix of
 *  another and a count per line is a count of occurrences in the rows. */
const label = (i: number): string => `SCROLLBK-${String(i).padStart(3, '0')}`

test('scrollback: a markerless session scrolls the rows that left the screen back into view', async ({
  page,
}) => {
  test.setTimeout(240_000)
  const stand = readStand()
  // The remote home is NOT the backend's home: a shell that sourced the
  // local rc hooks would come up integrated, and this session's premise is
  // that nothing ever does (shell-mode.spec.ts records the same hazard).
  const remoteHome = path.join(path.dirname(stand.home), 'scrollback-remote-home')
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  const fixture = await startSshd({ home: remoteHome, cwd: remoteHome })
  let createdId: string | null = null
  try {
    // The backend's ssh client must accept the fixture's freshly minted
    // host key: the known_hosts is REPLACED, not appended (a stale line for
    // a dead key refuses the connection).
    const sshDir = path.join(stand.home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), fixture.knownHosts + '\n')

    await page.setViewportSize({ width: 1280, height: 900 })
    await page.goto('/')
    await appReadyForInput(page)

    // The connection is seeded the way Settings would, with desiredMode
    // raw: a plain login shell, nothing installed, nothing integrated.
    const ws = await resolveBackend(page)
    const profileName = `e2e-scrollback-${Date.now()}`
    const created = await rpc<{ id: string }>(page, ws, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: 'e2e',
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

    const pane = page.locator('.pane.active')
    const surface = pane.locator('.live-history')
    const scrollTop = (): Promise<number> =>
      page.evaluate(() => {
        const a = document.querySelector('.pane.active .scrollback-area')
        return a === null ? -1 : a.scrollTop
      })
    const atLiveEnd = (): Promise<boolean> =>
      page.evaluate(() => {
        const a = document.querySelector('.pane.active .scrollback-area')
        return a !== null && a.scrollTop + a.clientHeight >= a.scrollHeight - 2
      })
    const liveRows = (): Promise<string> =>
      page.evaluate(() =>
        Array.from(
          document.querySelectorAll('.pane.active .xterm-inner .term-grid-row'),
          (r) => r.textContent ?? '',
        ).join('\n'),
      )
    // The conventional pane's input is the terminal's own textbox: focus
    // the live region and the keys reach the pty, as in any terminal.
    const type_ = async (command: string): Promise<void> => {
      await page.keyboard.type(command)
      await page.keyboard.press('Enter')
    }

    // The session is up when the ACTIVE pane is the markerless one — the
    // unstructured mode is the raw session's own signature, one the local
    // pane (integrated, block model) never wears — and its screen model
    // has installed a frame the remote shell produced.
    await expect(pane.locator('.xterm-live-container.live-unstructured')).toHaveCount(1, {
      timeout: 45_000,
    })
    await expect
      .poll(async () => await page.evaluate(() => window.__nocxPaneScreen?.()?.revision ?? -1), {
        timeout: 30_000,
      })
      .toBeGreaterThanOrEqual(0)
    await pane.locator('.xterm-live-container').click()

    // ── Clause 1: more than one screen of output, every line found ──────
    const nonce = Date.now().toString(36)
    await type_(
      `for i in $(seq 1 ${LINES}); do echo ${'SCROLLBK-'}$(printf '%03d' $i); done; echo DONE-${nonce}`,
    )
    await expect.poll(async () => liveRows(), { timeout: 20_000 }).toContain(`DONE-${nonce}`)

    // Readiness on the REAL op: rows have actually departed. The floor the
    // backend names is where the surface's oldest page must land — the
    // emulator's own word on how far its history reaches, whatever the
    // session's scrollback budget kept.
    const sessionId = await pane.getAttribute('data-session-id')
    expect(sessionId).toBeTruthy()
    const wire = await openControlPlane(stand.port, stand.token)
    let floor = -1
    await expect
      .poll(
        async () => {
          const res = (await wire.call('session.historyPage', {
            sessionId,
            before: null,
            limit: 1,
          })) as { start: number; end: number; floor: number }
          floor = res.floor
          return res.end - res.start
        },
        { timeout: 20_000 },
      )
      .toBeGreaterThan(0)

    // The reader wheels up. Each poll is one real wheel gesture over the
    // real scroller; the walk stops at the page whose start is the floor
    // the backend named.
    await pane.locator('.scrollback-area').hover()
    const oldest = surface.locator('.live-history-page').first()
    await expect
      .poll(
        async () => {
          const start = await oldest.getAttribute('data-start').catch(() => null)
          if (start === String(floor)) return true
          await page.mouse.wheel(0, -900)
          return false
        },
        { timeout: 60_000, intervals: [400] },
      )
      .toBe(true)

    // Every printed line is found — history pages and the live screen
    // together hold each label exactly once, and no row was invented.
    const counts = await page.evaluate(
      ([total]) => {
        const rows = document.querySelectorAll(
          '.pane.active .live-history .term-grid-row, .pane.active .xterm-inner .term-grid-row',
        )
        const text = Array.from(rows, (r) => r.textContent ?? '').join('\n')
        const out: number[] = []
        for (let i = 1; i <= (total as number); i++) {
          out.push(text.split(`SCROLLBK-${String(i).padStart(3, '0')}`).length - 1)
        }
        return out
      },
      [LINES],
    )
    for (let i = 1; i <= LINES; i++) {
      expect(counts[i - 1], label(i)).toBe(1)
    }

    // ── Clause 2: arriving output does not move the reader's anchor ─────
    // The reader is scrolled up into the oldest rows, reading. A delayed
    // producer was armed BEFORE the anchor was captured, so its output is
    // guaranteed to land while the reader holds the position.
    const anchor = await page.evaluate(() => {
      const scroller = document.querySelector('.pane.active .scrollback-area')
      if (!(scroller instanceof HTMLElement)) throw new Error('scrollback scroller is missing')
      const rows = Array.from(scroller.querySelectorAll('.live-history-page .term-grid-row'))
      const visible = rows.find((row) => {
        const rect = row.getBoundingClientRect()
        const clip = scroller.getBoundingClientRect()
        return rect.bottom > clip.top && rect.top < clip.bottom
      })
      if (!(visible instanceof HTMLElement)) throw new Error('no history row is visible to anchor')
      return { text: visible.textContent ?? '', top: visible.getBoundingClientRect().top }
    })
    expect(anchor.text).toMatch(/SCROLLBK-\d{3}/)
    const anchorTop = await scrollTop()
    await type_(`( sleep 8; echo ANCHOR-LATE-${nonce} ) &`)
    await expect
      .poll(
        async () =>
          liveRows().then((rows) =>
            rows.split('\n').some((row) => row.trim() === `ANCHOR-LATE-${nonce}`),
          ),
        { timeout: 25_000 },
      )
      .toBe(true)
    const anchorAfter = await page.evaluate((text) => {
      const scroller = document.querySelector('.pane.active .scrollback-area')
      if (!(scroller instanceof HTMLElement)) throw new Error('scrollback scroller is missing')
      const row = Array.from(scroller.querySelectorAll('.live-history-page .term-grid-row')).find(
        (candidate) => candidate.textContent === text,
      )
      if (!(row instanceof HTMLElement)) throw new Error('the anchored history row was replaced')
      return row.getBoundingClientRect().top
    }, anchor.text)
    expect(anchorAfter).toBeCloseTo(anchor.top, 0)
    expect(await scrollTop()).toBe(anchorTop)

    // ── Clause 3: returning to the bottom resumes tail follow ───────────
    // Each poll is a real wheel-down gesture until the live end is reached.
    await expect
      .poll(
        async () => {
          if (await atLiveEnd()) return true
          await page.mouse.wheel(0, 1400)
          return false
        },
        { timeout: 30_000, intervals: [250] },
      )
      .toBe(true)
    // At the live end again: a new command's output is followed, not lost
    // below the fold.
    await pane.locator('.xterm-live-container').click()
    await type_(`echo TAIL-RESUMED-${nonce}`)
    await expect
      .poll(async () => liveRows(), { timeout: 20_000 })
      .toContain(`TAIL-RESUMED-${nonce}`)
    expect(await atLiveEnd()).toBe(true)

    // ── Clause 4: `clear` removes what went before, and invents nothing ──
    await type_('clear')
    // The backend sighted the real erase-saved-lines and told the client:
    // everything painted above the live screen goes when the fact lands.
    // (And if that notification were ever missed, the next page read's
    // floor — the head, after ED3 — prunes the same pages at the same
    // gesture; either way what the reader finds below is the truth.)
    await expect(pane.locator('.live-history-page')).toHaveCount(0, { timeout: 20_000 })
    // Scrolling up now shows only what the emulator still holds: nothing.
    // Each poll is a wheel-up over the real scroller; the settled state is
    // position 0 with no page above the live screen.
    await expect
      .poll(
        async () => {
          await page.mouse.wheel(0, -1500)
          return page.evaluate(() => ({
            top: document.querySelector('.pane.active .scrollback-area')?.scrollTop ?? -1,
            pages: document.querySelectorAll('.pane.active .live-history-page').length,
          }))
        },
        { timeout: 15_000, intervals: [300] },
      )
      .toEqual({ top: 0, pages: 0 })

    // ── Clause 5: a full-screen program's pane reveals no primary rows ──
    // Fresh history first, so there is something a Page Up COULD wrongly
    // reveal: rows depart while the reader sits at the tail, and the
    // surface arms itself without any scroll.
    await type_(`for i in $(seq 1 120); do echo ${'SCROLLBK-'}$(printf '%03d' $i); done`)
    await expect
      .poll(async () => surface.locator('.live-history-page').count(), {
        timeout: 30_000,
        intervals: [300],
      })
      .toBeGreaterThan(0)
    // The reader wheels up into it — each poll iteration ONE real gesture,
    // as a person's repeated wheels are — and then starts a program that
    // takes the pane (the alternate screen, spelled by hand: no curses
    // needed).
    const beforeAlt = await scrollTop()
    await expect
      .poll(
        async () => {
          const top = await scrollTop()
          if (top < beforeAlt) return true
          await page.mouse.wheel(0, -1200)
          return false
        },
        { timeout: 10_000, intervals: [150] },
      )
      .toBe(true)
    await type_(`printf '\\033[?1049h'; echo ALT-${nonce}; sleep 60`)
    await expect(pane.locator('.xterm-live-container.live-fullscreen')).toHaveCount(1, {
      timeout: 20_000,
    })
    // Out of the flow: the surface is display:none while the program owns
    // the pane, and neither Page Up nor the wheel can move a scroll that
    // no longer exists. (The PageUp bytes queue behind the sleeping shell
    // and are flushed harmlessly by the interrupt below; nothing here
    // reads the screen back as input.)
    await page.keyboard.press('PageUp')
    await expect
      .poll(
        async () => {
          await page.mouse.wheel(0, -1000)
          return scrollTop()
        },
        { timeout: 10_000, intervals: [200] },
      )
      .toBe(0)
    // The program lets go: the primary's rows are back, under the same
    // numbers, and the surface shows what it kept.
    await page.keyboard.press('Control+C')
    await type_(`printf '\\033[?1049l'`)
    await expect(pane.locator('.xterm-live-container.live-fullscreen')).toHaveCount(0, {
      timeout: 30_000,
    })
    await expect(surface.locator('.term-grid-row').first()).toBeAttached({ timeout: 15_000 })
  } finally {
    try {
      const info = await resolveBackend(page)
      if (createdId) await rpc(page, info, 'profiles.delete', { id: createdId })
    } catch {
      // The cleanup is best-effort; the assertions above own the verdict.
    }
    fixture.proc.kill()
  }
})
