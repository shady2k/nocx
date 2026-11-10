/**
 * e2e: a reconnect over a long transcript does not overflow the response
 * queue (nocx-t7xhe).
 *
 * The defect: after a socket outage the renderer asked every mounted block for
 * its whole stored body at once. Five hundred blocks, and one command that
 * printed five thousand rows, put more than the connection's 64-frame critical
 * queue could hold; the server's overflow policy closed the socket, and the
 * person saw the Connection status modal for a read they never asked to be
 * urgent.
 *
 * The owner's decision (2026-11-10, ADR-y45sp41c) is lazy: after a reconnect the
 * renderer reads what is on screen first, older blocks newest-to-oldest, and
 * within a large block only the rows around the viewport. Every block reserves
 * its height from its row count, so nothing moves when rows arrive. The server
 * keeps answering paged reads, but holds a slot until each RESPONSE has left
 * the socket and answers "retry after N ms" when none is free.
 *
 * WHAT THIS SPEC WATCHES, end to end: a real 500-block transcript with one
 * 5000-row command at its oldest end, a real socket cut in front of the app,
 * the reconnect, the visible page painting first, the oldest block arriving
 * only when the person scrolls to it, and the transcript's height unchanged
 * across both.
 */
import net from 'node:net'
import type { Socket } from 'node:net'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import type { Page } from '@playwright/test'
import {
  standalone as base,
  appReadyForInput,
  bindEndpoint,
  openControlPlane,
  VaultBackend,
} from './harness'
import { readStand } from './stand'

const serverBin = () => readStand().server

const BLOCKS = 500
const LARGE_ROWS = 5000
// The renderer's page (restore-client BLOCK_ROWS_PAGE). Reading more than this
// in one response is what the lazy path exists to stop; the assertion allows
// one page and the merge of a page already in hand.
const PAGE_ROWS = 200
// A paged read may be merged with the page a growth notification already
// painted, so give the bound one page of headroom and no more. A whole 5000-row
// body cannot fit under it.
const LOADED_LIMIT = PAGE_ROWS * 2

const INPUT = '.pane.active .nocx-editor-input'
const BLOCK = '.pane.active .scrollback-inner > .cmd-block:not(.cmd-block-running)'
const SCROLLER = '.pane.active .scrollback-area'
const CONNECTION = '[aria-label="Connection status"]'

const test = base
test.use({ trace: { mode: 'retain-on-failure', snapshots: false, screenshots: false } })

interface CutProxy {
  readonly port: number
  cut(): void
  close(): Promise<void>
}

/** A TCP relay in front of the control plane. `cut` destroys every live
 *  socket and keeps listening, so the app's ordinary reconnect can succeed
 *  immediately — the same shape e2e/fault-proxy.ts gives an SSH flow, one
 *  level down. */
async function startCutProxy(port: number): Promise<CutProxy> {
  const sockets = new Set<Socket>()
  const server = net.createServer((client) => {
    const upstream = net.connect(port, '127.0.0.1')
    sockets.add(client)
    sockets.add(upstream)
    client.on('close', () => sockets.delete(client))
    upstream.on('close', () => sockets.delete(upstream))
    client.on('error', () => client.destroy())
    upstream.on('error', () => upstream.destroy())
    client.on('data', (chunk) => {
      if (!upstream.destroyed) upstream.write(chunk)
    })
    upstream.on('data', (chunk) => {
      if (!client.destroyed) client.write(chunk)
    })
  })
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolve())
  })
  const address = server.address()
  if (address === null || typeof address === 'string') {
    server.close()
    throw new Error('the cut proxy did not get a port')
  }
  return {
    port: address.port,
    cut() {
      for (const socket of sockets) socket.destroy()
      sockets.clear()
    },
    async close() {
      for (const socket of sockets) socket.destroy()
      sockets.clear()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}

/** One credit request per connection: the count is what says a reconnect
 *  happened, without asking the DOM to interpret a socket's life. */
function connections(log: string): number {
  return log.match(/method=transport\.credit/g)?.length ?? 0
}

async function runCommand(
  page: Page,
  command: string,
  index: number,
  marker: string,
): Promise<void> {
  const input = page.locator(INPUT)
  await input.fill(command)
  await page.keyboard.press('Enter')
  await page
    .waitForFunction(
      ({ frozen, n, first }) => {
        const block = document.querySelectorAll<HTMLElement>(frozen)[n - 1]
        return (
          block !== undefined &&
          block.checkVisibility() &&
          (block.textContent ?? '').includes(first)
        )
      },
      { frozen: BLOCK, n: index, first: marker },
      { timeout: 60_000 },
    )
    .catch((error: unknown) => {
      throw new Error(`command ${index} never froze: ${String(error)}`)
    })
}

test.describe('a reconnect over a long transcript stays lazy', () => {
  test.setTimeout(16 * 60_000)

  test('500 blocks and a 5000-row command reconnect without the connection modal', async ({
    page,
  }) => {
    const backend = new VaultBackend(serverBin(), {
      root: mkdtempSync(join(tmpdir(), 'nocx-lazy-reconnect-')),
    })
    const endpoint = await backend.start()
    const setupWire = await openControlPlane(endpoint.port, endpoint.token)
    try {
      // The 5000-row command is past the default per-command cap; raise it so
      // this spec measures a block that really holds 5000 rows.
      await setupWire.call('settings.set', { key: 'history.outputCapKB', value: 4096 })
    } finally {
      setupWire.close()
    }

    const proxy = await startCutProxy(endpoint.port)
    try {
      await bindEndpoint(page, { port: proxy.port, token: endpoint.token })
      await page.goto('/')
      await appReadyForInput(page)

      // THE OLDEST BLOCK IS THE LARGE ONE, and it is the one a person
      // scrolls UP to: its page must be absent while the bottom of the
      // transcript is on screen and arrive when they get there. The 500 short
      // blocks below it are already one page each and keep their rows.
      await runCommand(
        page,
        `printf 'lazy-large-%04d\\n' {1..${LARGE_ROWS}}`,
        1,
        // The tail marker: a live page always holds the newest rows, and the
        // last row is what the pane is showing when the command ends.
        `lazy-large-${String(LARGE_ROWS).padStart(4, '0')}`,
      )
      for (let index = 1; index <= BLOCKS; index += 1) {
        const marker = `lazy-${String(index).padStart(4, '0')}`
        await runCommand(
          page,
          `printf '${marker}-one\\n${marker}-two\\n'`,
          index + 1,
          `${marker}-one`,
        )
      }
      await expect(page.locator(BLOCK)).toHaveCount(BLOCKS + 1, { timeout: 120_000 })
      // The last command's final rows are read after its freeze. The cut must
      // measure a settled transcript, not a read in flight.
      await expect
        .poll(
          () =>
            page.evaluate(() => {
              let loaded = 0
              for (const output of document.querySelectorAll<HTMLElement>(
                '.pane.active .cmd-block .cmd-output',
              )) {
                loaded += Number(output.dataset.rowsLoaded ?? 0)
              }
              return loaded
            }),
          { timeout: 120_000, message: 'the live transcript never finished reading its rows' },
        )
        .toBeGreaterThanOrEqual(LARGE_ROWS + BLOCKS * 2)

      const before = await page.evaluate((selector) => {
        const area = document.querySelector<HTMLElement>(selector)!
        const blocks = document.querySelectorAll<HTMLElement>(
          '.pane.active .cmd-block:not(.cmd-block-running)',
        )
        const first = blocks[0]
        const output = first?.querySelector<HTMLElement>('.cmd-output')
        return {
          scrollHeight: area.scrollHeight,
          blockHeight: first?.offsetHeight ?? 0,
          blocksHeight: [...blocks].reduce((total, block) => total + block.offsetHeight, 0),
          rowCount: Number(output?.dataset.rowsCount ?? 0),
          loaded: Number(output?.dataset.rowsLoaded ?? 0),
        }
      }, SCROLLER)
      expect(before.rowCount).toBe(LARGE_ROWS)
      expect(before.loaded).toBe(LARGE_ROWS)

      // THE CUT. The renderer reconnects through the same proxy, and the
      // refresh path runs exactly as it does after any ordinary outage.
      const connectionsBefore = connections(backend.logTail(1 << 20))
      proxy.cut()
      await expect
        .poll(() => connections(backend.logTail(1 << 20)), {
          timeout: 60_000,
          message: 'the renderer did not reconnect after the socket was cut',
        })
        .toBeGreaterThan(connectionsBefore)

      // The modal the defect produced must never survive the reconnect. The
      // element itself lives in the document while the host is closed, so
      // the question is whether it is VISIBLE, never whether it exists.
      await expect(page.locator(CONNECTION)).toBeHidden({ timeout: 30_000 })
      expect(backend.logTail(1 << 20)).not.toContain('connection outbound stalled')

      // WHAT IS ON SCREEN PAINTED FIRST. The bottom block is the newest of the
      // 500 short ones; its rows are back.
      await expect
        .poll(
          () =>
            page.evaluate(() => {
              const blocks = document.querySelectorAll<HTMLElement>(
                '.pane.active .scrollback-inner > .cmd-block:not(.cmd-block-running)',
              )
              const last = blocks[blocks.length - 1]?.querySelector<HTMLElement>('.cmd-output')
              return Number(last?.dataset.rowsLoaded ?? 0)
            }),
          { timeout: 60_000, message: 'the visible block never painted its rows back' },
        )
        .toBeGreaterThan(0)

      // WHAT IS ON SCREEN HAS A BODY: the newest short block is at the
      // bottom and its two rows are there.
      const visibleNewest = await page.evaluate(() => {
        const blocks = document.querySelectorAll<HTMLElement>(
          '.pane.active .cmd-block:not(.cmd-block-running)',
        )
        const last = blocks[blocks.length - 1]
        const output = last?.querySelector<HTMLElement>('.cmd-output')
        return {
          loaded: Number(output?.dataset.rowsLoaded ?? 0),
          rowCount: Number(output?.dataset.rowsCount ?? 0),
        }
      })
      expect(visibleNewest.rowCount).toBe(2)
      expect(visibleNewest.loaded).toBe(2)

      // The oldest block is off screen: it reserves its height and holds none
      // of its body.
      const away = await page.evaluate(() => {
        const blocks = document.querySelectorAll<HTMLElement>(
          '.pane.active .cmd-block:not(.cmd-block-running)',
        )
        const first = blocks[0]
        const output = first?.querySelector<HTMLElement>('.cmd-output')
        return {
          loaded: Number(output?.dataset.rowsLoaded ?? 0),
          rowCount: Number(output?.dataset.rowsCount ?? 0),
          spacer: Number(
            first?.querySelector<HTMLElement>('.cmd-output-spacer')?.dataset.rows ?? 0,
          ),
          blockHeight: first?.offsetHeight ?? 0,
          blocksHeight: [...blocks].reduce((total, block) => total + block.offsetHeight, 0),
        }
      })
      expect(away.rowCount).toBe(LARGE_ROWS)
      expect(away.loaded).toBeLessThanOrEqual(LOADED_LIMIT)
      expect(away.spacer).toBeGreaterThan(0)
      const beforeScrollLoaded = away.loaded

      // SCROLL UP TO IT. The page arrives, the block's height does not change
      // — every row was already reserved — and no row moves.
      await page.evaluate((selector) => {
        document.querySelector<HTMLElement>(selector)!.scrollTo({ top: 0, behavior: 'instant' })
      }, SCROLLER)
      await expect
        .poll(
          () =>
            page.evaluate(() => {
              const first = document.querySelector<HTMLElement>(
                '.pane.active .cmd-block:not(.cmd-block-running)',
              )
              const output = first?.querySelector<HTMLElement>('.cmd-output')
              return Number(output?.dataset.rowsLoaded ?? 0)
            }),
          { timeout: 60_000, message: 'the oldest block never painted its page' },
        )
        .toBeGreaterThan(beforeScrollLoaded)

      const after = await page.evaluate((selector) => {
        const area = document.querySelector<HTMLElement>(selector)!
        const blocks = document.querySelectorAll<HTMLElement>('.pane.active .cmd-block')
        const first = blocks[0]
        const output = first?.querySelector<HTMLElement>('.cmd-output')
        return {
          scrollHeight: area.scrollHeight,
          blockHeight: first?.offsetHeight ?? 0,
          blocksHeight: [...blocks].reduce((total, block) => total + block.offsetHeight, 0),
          loaded: Number(output?.dataset.rowsLoaded ?? 0),
        }
      }, SCROLLER)
      // Scrolling loaded more of the body, and still not the whole 5000-row
      // block: the decision is a page around the viewport, not a full read.
      expect(after.loaded).toBeGreaterThan(beforeScrollLoaded)
      expect(after.loaded).toBeLessThan(LARGE_ROWS / 2)
      // THE RESERVATION WAS TRUE: the block's height after the page arrived is
      // the height it had while that page was still missing. Nothing above the
      // reader moved.
      expect(after.blocksHeight).toBe(away.blocksHeight)
      expect(after.blockHeight).toBe(away.blockHeight)

      // The connection survived the whole exchange, and the app still takes
      // a command.
      await expect(page.locator(CONNECTION)).toBeHidden()
      await expect(page.locator(INPUT)).toBeVisible()
    } finally {
      await proxy.close()
      backend.stop()
    }
  })
})
