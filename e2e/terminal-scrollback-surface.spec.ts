// e2e: the live tier's scrollback surface (nocx-zg3k3.10.4).
//
// Five clauses, driven through the real UI against the real backend, in a
// session with NO shell integration: the fixture sshd opened with
// desiredMode raw, so no launcher runs, no OSC 133 ever arrives, and the
// unstructured mode owns the pane for the whole test. The rows the reader
// scrolls into view come from the backend's session.historyPage — the
// emulator's own scrollback — painted by the same painter as the live
// screen. Every wait is on visible row content or the pane's own screen
// model, with the real history-page op as the backend readiness gate.
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
    const atLiveEnd = (): Promise<boolean> =>
      page.evaluate(() => {
        const a = document.querySelector('.pane.active .scrollback-area')
        return a !== null && a.scrollTop + a.clientHeight >= a.scrollHeight - 2
      })
    const liveRows = (): Promise<string> =>
      page.evaluate(() =>
        Array.from(
          document.querySelectorAll('.pane.active .terminal-live-inner .term-grid-row'),
          (r) => r.textContent ?? '',
        ).join('\n'),
      )
    const readVisibleRows = async () =>
      page.evaluate(() => {
        const scroller = document.querySelector('.pane.active .scrollback-area')
        if (!(scroller instanceof HTMLElement)) throw new Error('scrollback scroller is missing')
        const clip = scroller.getBoundingClientRect()
        return Array.from(
          scroller.querySelectorAll(
            '.live-history .term-grid-row, .terminal-live-inner .term-grid-row',
          ),
          (row) => ({ el: row, text: (row.textContent ?? '').trim() }),
        )
          .filter(({ el }) => {
            const rect = el.getBoundingClientRect()
            return rect.bottom > clip.top && rect.top < clip.bottom
          })
          .map(({ text }) => text)
      })
    // The conventional pane's input is the terminal's own textbox: focus
    // the live region and the keys reach the pty, as in any terminal.
    const type_ = async (command: string): Promise<void> => {
      await page.keyboard.type(command)
      await page.keyboard.press('Enter')
    }
    const labelsInRows = (rows: string[]): string[] =>
      rows.flatMap((text) => text.match(/SCROLLBK-\d{3}/g) ?? [])
    const firstExpectedLabels = Array.from(
      { length: 5 },
      (_, index) => `SCROLLBK-${String(index + 1).padStart(3, '0')}`,
    )

    // The raw profile and isolated remote home make this session markerless.
    // Wait for its first frame before sending the user's command.
    await expect
      .poll(async () => await page.evaluate(() => window.__nocxPaneScreen?.()?.revision ?? -1), {
        timeout: 30_000,
      })
      .toBeGreaterThanOrEqual(0)
    // The pane can paint a first frame before the raw shell's input route is
    // ready. Wait for the observable integration check to settle, then verify
    // the terminal's own input element owns focus before sending the command.
    await expect(pane.getByRole('status', { name: 'Checking this shell…' })).toHaveCount(0, {
      timeout: 30_000,
    })
    const painterGrid = pane.getByRole('grid', { name: 'Terminal output' })
    const terminalInput = pane.locator('.ui-terminal-input')
    await painterGrid.click()
    await expect(terminalInput).toBeFocused()

    // ── Clause 1: more than one screen of output, every line found ──────
    const nonce = Date.now().toString(36)
    await type_(
      `for i in $(seq 1 ${LINES}); do echo ${'SCROLLBK-'}$(printf '%03d' $i); done; printf '%s%s\\n' 'DONE-' '${nonce}'`,
    )
    await expect.poll(async () => liveRows(), { timeout: 20_000 }).toContain(`DONE-${nonce}`)

    // A raw shell owns the pane before it has any integrated command blocks.
    // Its painted text must still use the cell width committed by the backend;
    // a correctly resized PTY alone does not prove the glyphs use that pitch.
    await expect
      .poll(
        () =>
          page.evaluate(() => {
            const geometry = window.__nocxPaneScreen?.()?.geometry
            const row = document.querySelector('.pane.active .terminal-live-grid .term-grid-row')
            const stack = document.querySelector('.pane.active .scrollback-inner')
            if (!stack?.classList.contains('inner-fullscreen-mode')) return Infinity
            const published = parseFloat(
              getComputedStyle(stack).getPropertyValue('--term-cell-width'),
            )
            if (!Number.isFinite(published) || published <= 0) return Infinity
            if (!geometry || geometry.cellWidthPx <= 0 || !row) return Infinity
            const range = document.createRange()
            range.selectNodeContents(row)
            const painted = range.getBoundingClientRect().width
            const cellWidth = geometry.cellWidthPx / devicePixelRatio
            const committed = geometry.cols * cellWidth
            return Math.max(Math.abs(painted - committed), Math.abs(published - cellWidth))
          }),
        {
          timeout: 20_000,
          message: 'the markerless shell is painted at a different pitch from its committed grid',
        },
      )
      .toBeLessThanOrEqual(1)

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

    // Traverse from the live tail to the first retained page, collecting rows
    // from the viewport as a person sees them. Never require 200 rows to be
    // mounted simultaneously.
    await pane.locator('.scrollback-area').hover()
    const oldest = surface.locator('.live-history-page').first()
    const scrollToHistoryStart = async (observeRows?: (rows: string[]) => void): Promise<void> => {
      await expect
        .poll(
          async () => {
            const visibleRows = await readVisibleRows()
            observeRows?.(visibleRows)
            const start = await oldest.getAttribute('data-start').catch(() => null)
            const visibleLabels = labelsInRows(visibleRows)
            if (
              start === String(floor) &&
              firstExpectedLabels.every((expected, index) => visibleLabels[index] === expected)
            ) {
              return true
            }
            await page.mouse.wheel(0, -400)
            return false
          },
          { timeout: 60_000, intervals: [400] },
        )
        .toBe(true)
    }
    const expectedLabels = new Set(Array.from({ length: LINES }, (_, index) => label(index + 1)))
    const seenLabels = new Set<string>()
    await scrollToHistoryStart((rows) => {
      const visibleLabels = labelsInRows(rows)
      expect([...new Set(visibleLabels)]).toEqual(visibleLabels)
      for (const current of visibleLabels) {
        expect(expectedLabels.has(current), `unexpected visible label ${current}`).toBe(true)
        seenLabels.add(current)
      }
    })
    for (let i = 1; i <= LINES; i++) {
      expect(seenLabels.has(label(i)), label(i)).toBe(true)
    }

    // ── Clause 2: arriving output does not move the reader's anchor ─────
    // Arm the producer while following the live end. Submitting a command can
    // itself move the scroller; that is not arriving output. Wait for its
    // armed marker before returning to the oldest history page to read.
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
    await pane.locator('.terminal-live-container').click()
    await expect(terminalInput).toBeFocused()
    const lateMarker = `ANCHOR-LATE-${nonce}`
    await type_(`( sleep 15; echo ${lateMarker} ) & echo ANCHOR-ARMED-${nonce}`)
    await expect
      .poll(async () => liveRows(), { timeout: 20_000 })
      .toContain(`ANCHOR-ARMED-${nonce}`)

    // Re-enter the same history position only after the producer confirms it
    // is armed. Compare visible rows (not the virtualized mounted-page window)
    // and the anchor row's viewport position.
    await pane.locator('.scrollback-area').hover()
    await scrollToHistoryStart()
    const visibleRowsBefore = await readVisibleRows()
    const visibleAnchorIndex = visibleRowsBefore.findIndex((text) => /SCROLLBK-\d{3}/.test(text))
    if (visibleAnchorIndex < 0) throw new Error('no visible history row is available to anchor')
    const anchorStart = Math.max(0, visibleAnchorIndex - 2)
    const anchorEnd = Math.min(visibleRowsBefore.length, visibleAnchorIndex + 3)
    const anchorRow = visibleRowsBefore[visibleAnchorIndex]
    const anchor = {
      rows: visibleRowsBefore.slice(anchorStart, anchorEnd),
      index: visibleAnchorIndex - anchorStart,
      rowText: anchorRow,
    }
    expect(anchor.rows[anchor.index]).toMatch(/SCROLLBK-\d{3}/)
    const anchorTop = await page.evaluate((text) => {
      const area = document.querySelector('.pane.active .scrollback-area')
      if (area === null) return null
      const clip = area.getBoundingClientRect()
      const row = Array.from(area.querySelectorAll('.live-history-page .term-grid-row')).find(
        (candidate) => (candidate.textContent ?? '').trim() === text,
      )
      if (row === undefined) return null
      const bounds = row.getBoundingClientRect()
      return bounds.bottom > clip.top && bounds.top < clip.bottom ? bounds.top : null
    }, anchor.rowText)
    expect(anchorTop).not.toBeNull()
    const visibleBefore = visibleRowsBefore

    await expect.poll(async () => liveRows(), { timeout: 25_000 }).toContain(lateMarker)
    const visibleAfter = await readVisibleRows()
    expect(visibleAfter.filter((row) => row.includes(lateMarker)).length).toBeLessThanOrEqual(1)
    const withoutLateMarker = (rows: string[]): string[] =>
      rows.flatMap((row) => {
        if (!row.includes(lateMarker)) return [row]
        const remaining = row.replace(lateMarker, '').trim()
        return remaining === '' ? [] : [remaining]
      })
    const anchorTopAfter = await page.evaluate((text) => {
      const area = document.querySelector('.pane.active .scrollback-area')
      if (area === null) return null
      const clip = area.getBoundingClientRect()
      const row = Array.from(area.querySelectorAll('.live-history-page .term-grid-row')).find(
        (candidate) => (candidate.textContent ?? '').trim() === text,
      )
      if (row === undefined) return null
      const bounds = row.getBoundingClientRect()
      return bounds.bottom > clip.top && bounds.top < clip.bottom ? bounds.top : null
    }, anchor.rowText)
    // The reader still sees the same anchor at the same place. These are the
    // visible history rows and the row's viewport position, not the virtual
    // window's mounted count or a scroller offset.
    expect(anchorTopAfter).not.toBeNull()
    expect(anchorTopAfter).toBeCloseTo(anchorTop!, 0)
    expect(withoutLateMarker(visibleAfter)).toEqual(visibleBefore)

    await scrollToHistoryStart()
    const visibleAnchorAfter = await readVisibleRows()
    const anchorIndexAfter = visibleAnchorAfter.findIndex((text) => text === anchor.rowText)
    expect(anchorIndexAfter).toBeGreaterThanOrEqual(0)
    expect(visibleAnchorAfter.filter((text) => text === anchor.rowText)).toHaveLength(1)
    const anchorWindowStart = Math.max(0, anchorIndexAfter - anchor.index)
    const anchorWindowAfter = visibleAnchorAfter.slice(
      anchorWindowStart,
      anchorWindowStart + anchor.rows.length,
    )
    expect(anchorWindowAfter).toEqual(anchor.rows)

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
    await pane.locator('.terminal-live-container').click()
    await type_(`printf '%s%s\\n' 'TAIL-RESUMED-' '${nonce}'`)
    await expect
      .poll(async () => liveRows(), { timeout: 20_000 })
      .toContain(`TAIL-RESUMED-${nonce}`)
    expect(await atLiveEnd()).toBe(true)

    // ── Clause 4: `clear` removes what went before, and invents nothing ──
    const previousOutputVisible = (rows: string[]): boolean =>
      labelsInRows(rows).length > 0 ||
      rows.some((row) => row.includes(`DONE-${nonce}`) || row.includes(`TAIL-RESUMED-${nonce}`))
    expect(previousOutputVisible(await readVisibleRows())).toBe(true)
    await type_('clear')
    await expect
      .poll(async () => !previousOutputVisible(await readVisibleRows()), { timeout: 20_000 })
      .toBe(true)
    // Keep trying the user's wheel-up action across the old history range;
    // none of the cleared output may reappear in the viewport.
    for (let attempt = 0; attempt < 5; attempt++) {
      await page.mouse.wheel(0, -1500)
      await page.evaluate(
        () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())),
      )
      expect(previousOutputVisible(await readVisibleRows())).toBe(false)
    }

    // ── Clause 5: a full-screen program's pane reveals no primary rows ──
    // Fresh history first, and enough of it that the reader is still several
    // pages from the floor when the staged line below lands. The person must
    // be able to wheel back to its first row before the program takes the
    // pane.
    await pane.locator('.terminal-live-container').click()
    await type_(`for i in $(seq 1 200); do echo ${'SCROLLBK-'}$(printf '%03d' $i); done`)
    await expect.poll(async () => liveRows(), { timeout: 30_000 }).toContain(label(200))
    await expect
      .poll(
        async () => {
          if (labelsInRows(await readVisibleRows()).includes(label(1))) return true
          await page.mouse.wheel(0, -900)
          return false
        },
        { timeout: 30_000, intervals: [300] },
      )
      .toBe(true)
    const altRow = `ALT-${nonce}`
    await type_(`printf '\\033[?1049h'; echo ${altRow}; sleep 60`)

    // WAIT ON THE PROGRAM'S OWN ROW, NOT ON THE MARKER ANYWHERE. The shell
    // echoes the typed line — marker and all — before it runs it, so a
    // substring test over the joined rows is satisfied by that ECHO, and the
    // read that follows lands while the pane still paints the primary screen:
    // the program has not started yet. Measured on the merged-tree gate run of
    // 2026-10-06 (webkit): the failed read carried SCROLLBK-080..120 plus the
    // echoed command line, and the trace's DOM snapshot at that instant shows
    // `.terminal-live-container live-unstructured` with the history surface still
    // visible, the alternate screen taking the pane ~25 ms later. The row the
    // shell did not echo is the one that says the program owns the pane.
    const altScreenIsUp = async (): Promise<boolean> =>
      liveRows().then((rows) => rows.split('\n').some((row) => row.trim() === altRow))
    await expect.poll(altScreenIsUp, { timeout: 20_000 }).toBe(true)
    // WHAT THE PERSON CAN SEE, on the viewport and not on the mounted rows: the
    // live grid is the whole of this mode's visible surface, and the visible
    // union beside it covers the history surface the program must have taken
    // away.
    expect(labelsInRows(await readVisibleRows())).toEqual([])
    // The sleeping shell may echo queued key sequences. Assert the contract
    // directly: ALT stays visible and primary scrollback labels stay absent —
    // through the two gestures that used to reach the primary's rows.
    await page.keyboard.press('PageUp')
    await page.mouse.wheel(0, -1000)
    await expect.poll(altScreenIsUp, { timeout: 20_000 }).toBe(true)
    expect(labelsInRows(await readVisibleRows())).toEqual([])

    // Leaving the program restores the primary history, which the reader can
    // still reach by its visible first row.
    await page.keyboard.press('Control+C')
    await pane.locator('.terminal-live-container').click()
    // A post-exit line the shell did not echo proves Ctrl+C returned to the
    // live shell and the alternate-buffer exit sequence reached its parser.
    // The command echo is longer than this row, so it cannot satisfy the check.
    const returnedMarker = `AFTER-ALT-${nonce}`
    await type_(`printf '\\033[?1049l'; printf '\\n%s\\n' '${returnedMarker}'`)
    await expect
      .poll(
        async () => (await liveRows()).split('\n').some((row) => row.trim() === returnedMarker),
        { timeout: 20_000 },
      )
      .toBe(true)
    expect(await pane.getAttribute('data-session-id')).toBe(sessionId)

    // THE PANE IS BACK, on its own account, before the reader's first
    // wheel: the surface is on again in the unstructured mode. A wheel sent
    // while the program still owns the pane reaches nothing at all, and a
    // gesture the surface swallowed is not a scroll the reader made — the
    // poll below would then be timing its own ability to be heard.
    await expect
      .poll(
        () =>
          page.evaluate(() => {
            const stack = document.querySelector('.pane.active .scrollback-inner')
            const surface = document.querySelector(
              '.pane.active .live-history',
            ) as HTMLElement | null
            return (
              stack !== null &&
              stack.classList.contains('inner-unstructured') &&
              surface !== null &&
              !surface.hidden
            )
          }),
        { timeout: 20_000 },
      )
      .toBe(true)

    // AND THE SHELL SPEAKS WHILE THE READER IS IN THE PAST. This is the
    // everyday shape of the gesture, not a contrivance: a background job
    // prints one line a fraction of a second after the reader wheels up.
    // The line makes the painted coverage stale at its YOUNG end, which the
    // surface answers without touching a reader who is not at the tail —
    // and it must not stop that reader reaching the rows BELOW the oldest
    // painted one. The gesture below is the assertion either way: the
    // oldest label, in view, while the new line lands somewhere above.
    await pane.locator('.terminal-live-container').click()
    await type_(`( sleep 0.4; echo SPOKEN-${nonce} ) & echo ARMED-${nonce}`)
    await expect.poll(async () => liveRows(), { timeout: 20_000 }).toContain(`ARMED-${nonce}`)
    await expect
      .poll(
        async () => {
          if (labelsInRows(await readVisibleRows()).includes(label(1))) return true
          await page.mouse.wheel(0, -900)
          return false
        },
        { timeout: 30_000, intervals: [300] },
      )
      .toBe(true)
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
