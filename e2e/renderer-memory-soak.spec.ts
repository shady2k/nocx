import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { test, expect, promptReady } from './harness'

const TAB = '.nocx-tab'
const NEW_TAB = '[aria-label="New tab"]'
const CLOSE_TAB = '[aria-label="Close tab"]'
const CYCLES = Number(process.env.NOCX_MEMORY_CYCLES ?? 18)
const OUTPUT_ROUNDS = Number(process.env.NOCX_MEMORY_OUTPUT_ROUNDS ?? 12)
// Every cycle leaves the replacement tab with real printed output. The count
// of retained stored-row artifacts is this spec's evidence (nocx-3an1s.1.6);
// the bytes make the heap-growth figure mean something as well, and are kept
// small enough that a slow machine still paints each block in a second or two.
const CYCLE_LINES = Number(process.env.NOCX_MEMORY_CYCLE_LINES ?? 48)
const RETAINED_HEAP_GROWTH_LIMIT = Number(
  process.env.NOCX_MEMORY_HEAP_GROWTH_LIMIT ?? 24 * 1024 * 1024,
)
const RETAINED_DOM_GROWTH_LIMIT = Number(process.env.NOCX_MEMORY_DOM_GROWTH_LIMIT ?? 250)
// Detached native nodes (DOM) the heap graph may still name after the churn.
// The two snapshots are taken against the same six live tabs, so the honest
// figure is "no more than the baseline carried"; the slack absorbs the browser
// keeping a handful of nodes for its own bookkeeping.
const RETAINED_DETACHED_GROWTH_LIMIT = Number(process.env.NOCX_MEMORY_DETACHED_LIMIT ?? 8)
// How much the retained-artifact count may grow in the SECOND HALF of the
// churn. This is the churn-retention bound (nocx-atpw5.1.35): a leak that
// grows with past commands adds one artifact per cycle, and a count that has
// already reached its steady state adds none.
const STORED_ROWS_GROWTH_LIMIT = Number(process.env.NOCX_MEMORY_STORED_ROWS_GROWTH ?? 2)
// A ceiling on the whole retained set beyond the blocks still on screen. The
// renderer parks artifact reads for a block that has not bound yet in
// `_pendingStoredRows` (blocks.ts) — a per-BlockManager ring capped at
// MAX_PENDING_STORED_ROWS = 8, which is why this is not zero — and the DevTools
// instrument's own locator snapshot cache holds a detached block or two. This
// is a ceiling, not the regression bound: the growth check below is the claim.
const STORED_ROWS_CEILING_SLACK = Number(process.env.NOCX_MEMORY_STORED_ROWS_CEILING ?? 12)

type Metrics = {
  JSHeapUsedSize?: number
  JSHeapTotalSize?: number
  Nodes?: number
  Documents?: number
  LayoutCount?: number
  RecalcStyleCount?: number
}

type RetainedCounts = {
  Pane: number
  TerminalContent: number
  /** Detached nodes in the heap graph, all shapes. Reported, never asserted
   *  on: Chromium keeps detached `Attr` and sidebar nodes of its own, and that
   *  figure moves by thousands for reasons this spec does not control. */
  DetachedHeapNodes: number
  /** Detached nodes that ARE this product's terminal DOM — a pane, a block, a
   *  painted row, an editor input, the scrollback. This is the detached-tree
   *  assertion: `document.querySelectorAll` cannot enumerate a subtree that
   *  left the document, the heap graph can. */
  DetachedProductNodes: number
  /** Plain-object nodes shaped like `StoredBlockRows` (block-rows.ts): the
   *  parsed rows behind every painted artifact. A JS heap number is a
   *  Chromium instrument; this count is the shape itself. */
  StoredBlockRows: number
}

/** Node names that only this product's terminal DOM carries. A detached node
 *  whose name contains one of these is a subtree the renderer let go of. */
const PRODUCT_DOM_NODE_NAMES = [
  'pane',
  'nocx-editor-input',
  'cmd-block',
  'term-grid-row',
  'scrollback',
]

type DomCounts = {
  nodes: number
  /** `.pane` elements in the document. This is an ATTACHED-tree count, not a
   *  detached-object observable: `document.querySelectorAll` cannot see a
   *  subtree that has left the document. Detachment is measured from the
   *  heap graph instead, where a node carries its own `detachedness`. */
  paneNodes: number
}

type Sample = {
  dom: DomCounts
  retainedObjects: { panes: number; terminalContents: number } | null
  retained: RetainedCounts | null
  metrics: Metrics | null
}

async function metrics(page: import('@playwright/test').Page): Promise<Metrics> {
  const cdp = await page.context().newCDPSession(page)
  try {
    await cdp.send('Performance.enable')
    const result = (await cdp.send('Performance.getMetrics')) as {
      metrics: { name: string; value: number }[]
    }
    return Object.fromEntries(result.metrics.map(({ name, value }) => [name, value])) as Metrics
  } finally {
    await cdp.detach()
  }
}

async function snapshot(page: import('@playwright/test').Page): Promise<string> {
  const cdp = await page.context().newCDPSession(page)
  try {
    await cdp.send('HeapProfiler.enable')
    await cdp.send('HeapProfiler.collectGarbage')
    const chunks: string[] = []
    cdp.on('HeapProfiler.addHeapSnapshotChunk', (event: { chunk: string }) =>
      chunks.push(event.chunk),
    )
    await cdp.send('HeapProfiler.takeHeapSnapshot', {
      reportProgress: false,
      captureNumericValue: true,
    })
    return chunks.join('')
  } finally {
    await cdp.detach()
  }
}

async function send(
  page: import('@playwright/test').Page,
  command: string,
  outputMarker: string,
): Promise<void> {
  const input = page.locator('.pane.active .nocx-editor-input')
  await input.fill(command)
  await page.keyboard.press('Enter')
  const block = page
    .locator('.pane.active .cmd-block[data-block-kind="command"]')
    .filter({ hasText: outputMarker })
  await expect(block).toHaveCount(1)
  await expect(block.locator('.cmd-output')).toContainText(outputMarker)
  await expect(block).not.toHaveClass(/cmd-block-running/)
}

async function openSix(page: import('@playwright/test').Page): Promise<void> {
  await expect(page.locator(TAB)).toHaveCount(1)
  await promptReady(page)
  for (let i = 1; i < 6; i++) {
    await page.locator(NEW_TAB).click()
    await expect(page.locator(TAB)).toHaveCount(i + 1)
    await promptReady(page)
  }
}

/**
 * Count object nodes by shape out of a V8 heap snapshot.
 *
 * `StoredBlockRows` is a plain object literal (block-rows.ts), so the snapshot
 * has no constructor name for it. Its SHAPE is its name: an object carrying
 * `droppedRows`, `unavailableRows` and `truncated` as own properties is one of
 * those rows (parseStoredBlockRows always writes all three). Counting them in
 * a post-GC snapshot is how this spec can see a module-global cache holding
 * artifacts of closed tabs — which a DOM query and an attached-instance count
 * both cannot.
 */
function snapshotObjectCounts(snapshotText: string): RetainedCounts {
  const snapshot = JSON.parse(snapshotText) as {
    snapshot: {
      meta: {
        node_fields: string[]
        node_types: string[][]
        edge_fields: string[]
        edge_types: string[][]
      }
    }
    nodes: number[]
    edges: number[]
    strings: string[]
  }
  const meta = snapshot.snapshot.meta
  const fields = meta.node_fields
  const stride = fields.length
  const nameIndex = fields.indexOf('name')
  const typeIndex = fields.indexOf('type')
  const detachedIndex = fields.indexOf('detachedness')
  const edgeCountIndex = fields.indexOf('edge_count')
  const typeNames = meta.node_types[typeIndex]
  const edgeFields = meta.edge_fields
  const edgeStride = edgeFields.length
  const edgeTypeIndex = edgeFields.indexOf('type')
  const edgeNameIndex = edgeFields.indexOf('name_or_index')
  const edgeTypeNames = meta.edge_types[edgeTypeIndex]
  const counts: RetainedCounts = {
    Pane: 0,
    TerminalContent: 0,
    DetachedHeapNodes: 0,
    DetachedProductNodes: 0,
    StoredBlockRows: 0,
  }
  let edgeCursor = 0
  for (let index = 0; index < snapshot.nodes.length; index += stride) {
    const edgeCount = snapshot.nodes[index + edgeCountIndex]
    if (snapshot.nodes[index + detachedIndex] > 0) {
      counts.DetachedHeapNodes++
      const detachedName = snapshot.strings[snapshot.nodes[index + nameIndex]]
      if (PRODUCT_DOM_NODE_NAMES.some((needle) => detachedName.includes(needle))) {
        counts.DetachedProductNodes++
      }
    }
    if (typeNames[snapshot.nodes[index + typeIndex]] === 'object') {
      const name = snapshot.strings[snapshot.nodes[index + nameIndex]]
      if (name === 'Pane' || name === 'TerminalContent') counts[name]++
      let droppedRows = false
      let unavailableRows = false
      let truncated = false
      for (let edge = 0; edge < edgeCount; edge++) {
        const at = edgeCursor + edge * edgeStride
        if (edgeTypeNames[snapshot.edges[at + edgeTypeIndex]] !== 'property') continue
        const property = snapshot.strings[snapshot.edges[at + edgeNameIndex]]
        if (property === 'droppedRows') droppedRows = true
        else if (property === 'unavailableRows') unavailableRows = true
        else if (property === 'truncated') truncated = true
      }
      if (droppedRows && unavailableRows && truncated) counts.StoredBlockRows++
    }
    edgeCursor += edgeCount * edgeStride
  }
  return counts
}

function awaitFrame(page: import('@playwright/test').Page): Promise<void> {
  return page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())))
}

async function retainedObjects(
  page: import('@playwright/test').Page,
): Promise<{ panes: number; terminalContents: number } | null> {
  return page.evaluate(() => {
    const host = window as typeof window & {
      __nocxRendererRetainedObjects?: () => { panes: number; terminalContents: number }
    }
    return host.__nocxRendererRetainedObjects?.() ?? null
  })
}

/**
 * One measurement of the renderer's retained state.
 *
 * The heap snapshot is the retention instrument: it is taken after a forced
 * GC, so an object still in it is reachable. The in-page
 * `__nocxRendererRetainedObjects` reading stays as the product's own live-tab
 * bound — it counts attached panes and registered terminal contents, so it
 * cannot by itself see a retained data structure, and this spec no longer
 * treats it as if it could.
 */
async function measure(
  page: import('@playwright/test').Page,
  browserName: string,
  artifacts: string,
  label: 'before' | 'mid' | 'after',
): Promise<Sample> {
  await awaitFrame(page)
  const dom = await page.evaluate(() => ({
    nodes: document.querySelectorAll('*').length,
    paneNodes: document.querySelectorAll('.pane').length,
  }))
  const retained = await retainedObjects(page)
  if (browserName !== 'chromium')
    return { dom, retainedObjects: retained, retained: null, metrics: null }
  const snapshotText = await snapshot(page)
  writeFileSync(path.join(artifacts, `${label}.heapsnapshot`), snapshotText)
  const counts = snapshotObjectCounts(snapshotText)
  // After the forced GC the metrics describe the same collected heap the
  // snapshot does; taking them before it would compare a dirty heap with a
  // clean one.
  const measured = await metrics(page)
  return { dom, retainedObjects: retained, retained: counts, metrics: measured }
}

/** Block entry ids whose artifact rows are painted RIGHT NOW — the set the
 *  stored-output cache is allowed to hold. */
async function liveArtifactCount(page: import('@playwright/test').Page): Promise<number> {
  return page.evaluate(() => {
    const ids = new Set<string>()
    for (const output of document.querySelectorAll('.cmd-output')) {
      const block = output.parentElement
      const id = block?.getAttribute('data-entry-id')
      if (id && output.querySelector('[data-artifact-version]')) ids.add(id)
    }
    return ids.size
  })
}

test('renderer memory soak: retained artifacts return to the live set after churn (nocx-1ebrs.71, nocx-3an1s.1.6, nocx-atpw5.1.35)', async ({
  page,
  browserName,
}, info) => {
  test.setTimeout(240000)
  const startedAt = Date.now()
  await page.goto('/')
  await openSix(page)

  // Exercise output and redraw in every live tab before the baseline. The
  // baseline is the SAME live arrangement the run must return to: six tabs,
  // every one with painted output.
  for (let tab = 0; tab < 6; tab++) {
    await page.locator(TAB).nth(tab).click()
    await promptReady(page)
    const marker = `NOCX_SOAK_${tab}_${Date.now().toString(36)}`
    const command = `i=0; while [ $i -lt ${OUTPUT_ROUNDS} ]; do i=$((i+1)); printf '\\033[?1049h\\033[H\\033[2J${marker} %s\\n\\033[?1049l\\033[H\\033[2J' "$i"; done; printf '\\n%s_ROUNDS=%s\\n' '${marker}' "$i"`
    await send(page, command, `${marker}_ROUNDS=${OUTPUT_ROUNDS}`)
  }

  const artifacts = path.join(info.outputDir, 'renderer-memory')
  mkdirSync(artifacts, { recursive: true })
  const expectedLiveCount = await page.locator(TAB).count()

  // BASELINE, before the workload. Everything below that compares two states
  // compares this one with the state after the churn; the old spec took both
  // samples after the churn, which is comparing the final state with itself.
  const before = await measure(page, browserName, artifacts, 'before')
  expect(before.retainedObjects, 'in-page retained-object observable is installed').not.toBeNull()
  expect(before.retainedObjects).toEqual({
    panes: expectedLiveCount,
    terminalContents: expectedLiveCount,
  })

  const sample: {
    cycle: number
    dom: DomCounts
    retainedObjects: { panes: number; terminalContents: number } | null
  }[] = []
  // The churn runs in TWO HALVES. The measurement between them is what makes
  // this a retention test: by the second half every bounded structure the
  // renderer keeps has reached its steady state, so anything that still grows
  // with the number of closed tabs is retaining them.
  const firstHalf = Math.ceil(CYCLES / 2)
  let mid: Sample | null = null
  for (let cycle = 1; cycle <= CYCLES; cycle++) {
    await page.locator(CLOSE_TAB).last().click()
    await expect(page.locator(TAB)).toHaveCount(5)
    await page.locator(NEW_TAB).click()
    await expect(page.locator(TAB)).toHaveCount(6)
    await promptReady(page)
    const marker = `NOCX_CYCLE_${cycle}_${Date.now().toString(36)}`
    await send(
      page,
      `i=0; while [ $i -lt ${CYCLE_LINES} ]; do i=$((i+1)); printf '\\033[?1049h\\033[H\\033[2J${marker} %04d xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\\n' "$i"; done; printf '\\033[?1049l\\033[H\\033[2J'; echo ${marker}_DONE`,
      `${marker}_DONE`,
    )
    await awaitFrame(page)
    if (cycle === firstHalf) mid = await measure(page, browserName, artifacts, 'mid')
    if (cycle % 3 === 0 || cycle === CYCLES) {
      sample.push({
        cycle,
        dom: await page.evaluate(() => ({
          nodes: document.querySelectorAll('*').length,
          paneNodes: document.querySelectorAll('.pane').length,
        })),
        retainedObjects: await retainedObjects(page),
      })
    }
  }

  // FINAL STATE, measured the same way and at the same live arrangement.
  const after = await measure(page, browserName, artifacts, 'after')
  const liveArtifacts = await liveArtifactCount(page)
  const report: Record<string, unknown> = {
    browser: browserName,
    browserVersion: page.context().browser()?.version() ?? 'unknown',
    durationMs: Date.now() - startedAt,
    cycles: CYCLES,
    outputRoundsPerTab: OUTPUT_ROUNDS,
    cycleLines: CYCLE_LINES,
    tabs: expectedLiveCount,
    liveArtifacts,
    before,
    mid,
    after,
    sample,
    storedRowsGrowth: after.retained
      ? after.retained.StoredBlockRows - (before.retained?.StoredBlockRows ?? 0)
      : null,
    detachedProductNodeGrowth: after.retained
      ? after.retained.DetachedProductNodes - (before.retained?.DetachedProductNodes ?? 0)
      : null,
    detachedHeapNodeGrowth: after.retained
      ? after.retained.DetachedHeapNodes - (before.retained?.DetachedHeapNodes ?? 0)
      : null,
    heapGrowth: after.metrics
      ? (after.metrics.JSHeapUsedSize ?? 0) - (before.metrics?.JSHeapUsedSize ?? 0)
      : null,
  }

  // Written BEFORE the bounds run: a red run is the evidence this spec exists
  // to produce, and an assertion that fires first must not take the numbers
  // with it. The summary goes to the log as well, because Playwright deletes a
  // PASSING test's output directory (playwright.config.ts preserveOutput).
  writeFileSync(path.join(artifacts, 'measurements.json'), JSON.stringify(report, null, 2))
  console.log(
    `renderer memory soak (${browserName}): storedRows ${before.retained?.StoredBlockRows ?? 'n/a'} → ${mid?.retained?.StoredBlockRows ?? 'n/a'} → ${after.retained?.StoredBlockRows ?? 'n/a'} for ${liveArtifacts} live artifacts; detached terminal nodes ${before.retained?.DetachedProductNodes ?? 'n/a'} → ${after.retained?.DetachedProductNodes ?? 'n/a'}; heap growth ${report.heapGrowth ?? 'n/a'} bytes`,
  )

  // Written BEFORE the bounds run: a red run is the evidence this spec exists
  // to produce, and an assertion that fires first must not take the numbers
  // with it. The summary goes to the log as well, because Playwright deletes a
  // PASSING test's output directory (playwright.config.ts preserveOutput).
  writeFileSync(path.join(artifacts, 'measurements.json'), JSON.stringify(report, null, 2))
  console.log(
    `renderer memory soak (${browserName}): storedRows ${before.retained?.StoredBlockRows ?? 'n/a'} → ${mid?.retained?.StoredBlockRows ?? 'n/a'} → ${after.retained?.StoredBlockRows ?? 'n/a'} for ${liveArtifacts} live artifacts; detached terminal nodes ${before.retained?.DetachedProductNodes ?? 'n/a'} → ${after.retained?.DetachedProductNodes ?? 'n/a'}; heap growth ${report.heapGrowth ?? 'n/a'} bytes`,
  )

  expect(
    after.retainedObjects,
    'in-page retained-object observable remains available',
  ).not.toBeNull()
  expect(after.retainedObjects).toEqual({
    panes: expectedLiveCount,
    terminalContents: expectedLiveCount,
  })
  // Attached-tree counts: one pane per live tab, and the scrollback did not
  // grow with the closed tabs' DOM. Detached subtrees are a heap-graph
  // question (below), not one a document query can answer.
  expect(after.dom.paneNodes).toBe(expectedLiveCount)
  const nodeGrowth = after.dom.nodes - before.dom.nodes
  expect(nodeGrowth, `DOM node count grew ${nodeGrowth}; report: ${artifacts}`).toBeLessThanOrEqual(
    RETAINED_DOM_GROWTH_LIMIT,
  )

  // Heap snapshots and GC metrics use Chromium DevTools Protocol. WKWebView has
  // no CDP, so its product-critical retained-object bound above is in-page.
  if (browserName === 'chromium') {
    expect(before.retained).not.toBeNull()
    expect(mid?.retained, 'the mid-churn measurement ran').toBeTruthy()
    expect(after.retained).not.toBeNull()
    const beforeCounts = before.retained!
    const midCounts = mid!.retained!
    const afterCounts = after.retained!

    // THE CHURN RETENTION BOUND. Stored-output artifacts are parsed rows kept
    // for selection and repaint; a closed tab's block owns its rows, so the
    // cache must hold the blocks still on screen and nothing else. The old
    // module-global map answered with every block ever painted here (46 rows
    // for 45 closed tabs in the owner's measurement), which is linear in the
    // number of closed tabs — so the second half of the churn adds as much as
    // the first did.
    const secondHalfGrowth = afterCounts.StoredBlockRows - midCounts.StoredBlockRows
    expect(
      secondHalfGrowth,
      `stored-output artifacts grew by ${secondHalfGrowth} in the second half of ${CYCLES} cycles (${midCounts.StoredBlockRows} → ${afterCounts.StoredBlockRows}); report: ${artifacts}`,
    ).toBeLessThanOrEqual(STORED_ROWS_GROWTH_LIMIT)
    expect(
      afterCounts.StoredBlockRows,
      `retained stored-output artifacts ${afterCounts.StoredBlockRows} exceed the ${liveArtifacts} live blocks (+${STORED_ROWS_CEILING_SLACK}); report: ${artifacts}`,
    ).toBeLessThanOrEqual(liveArtifacts + STORED_ROWS_CEILING_SLACK)

    // Real detached-object evidence: nodes the heap graph marks detached, not
    // elements a document query can no longer reach (and therefore cannot
    // count). The bound is on THIS product's DOM shapes only — Chromium's own
    // detached nodes move by thousands without the renderer retaining a pane.
    const detachedGrowth = afterCounts.DetachedProductNodes - beforeCounts.DetachedProductNodes
    expect(
      detachedGrowth,
      `detached terminal DOM nodes grew by ${detachedGrowth} (${beforeCounts.DetachedProductNodes} → ${afterCounts.DetachedProductNodes}); report: ${artifacts}`,
    ).toBeLessThanOrEqual(RETAINED_DETACHED_GROWTH_LIMIT)

    const heapGrowth = (after.metrics?.JSHeapUsedSize ?? 0) - (before.metrics?.JSHeapUsedSize ?? 0)
    expect(
      heapGrowth,
      `retained JS heap grew ${heapGrowth} bytes; report: ${artifacts}`,
    ).toBeLessThanOrEqual(RETAINED_HEAP_GROWTH_LIMIT)

    expect(afterCounts.Pane).toBeLessThanOrEqual(beforeCounts.Pane + 1)
    expect(afterCounts.TerminalContent).toBeLessThanOrEqual(beforeCounts.TerminalContent + 1)
  }
})
