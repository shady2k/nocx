// @vitest-environment jsdom
// The row-overflow defect (nocx-wzc4.9, reshaped in nocx-wzc4.10): in the
// sidebar the address — the row's PRIMARY KEY, the thing the user came for —
// was truncated to "127.0...." while the process took the rest of the width.
// Ranking them against each other only decided which one lost; the answer is
// that they stop competing. The address owns its own line and the process
// sits beneath it, which costs nothing because the rows were already spending
// their height on air. jsdom computes no layout, so this pins what jsdom CAN
// see: the DOM structure that expresses the intent (a stacked text column
// beside the action), and the stylesheet contract that keeps the address
// unbounded on its own line.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, waitFor } from '@solidjs/testing-library'
import {
  PortsPanel,
  createPortsFilterControl,
  createPortsPauseControl,
  type PortsPanelServices,
} from './ports'
import type { PortsStatusResult } from './generated/ports.status'

afterEach(() => cleanup())

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
const PORTS_CSS = resolve(import.meta.dirname ?? '.', 'styles/surfaces/ports.css')

function extractRuleBlock(css: string, needle: string): string | null {
  const re = new RegExp(`\\.${needle}(?![\\w-])`)
  let i = 0
  while (i < css.length) {
    const open = css.indexOf('{', i)
    if (open === -1) return null
    let depth = 1
    let j = open + 1
    while (j < css.length && depth > 0) {
      if (css[j] === '{') depth++
      else if (css[j] === '}') depth--
      j++
    }
    if (depth !== 0) return null
    if (re.test(css.slice(i, open))) return css.slice(open + 1, j - 1)
    i = j
  }
  return null
}

const stripComments = (s: string): string => s.replace(/\/\*[\s\S]*?\*\//g, '')

// ── Fixtures (mirror ports.test.tsx) ─────────────────────────────────────

const statusFixture = (port: number): PortsStatusResult => ({
  profileId: 'ssh:p1:1',
  host: 'host.example',
  discovery: {
    state: 'available',
    listeners: [
      {
        family: 'ipv4' as const,
        address: '127.0.0.1',
        port,
        process: { evidence: 'known', name: 'node', pid: 123 },
      },
    ],
    probe: 'ss',
    probesTried: ['ss'],
    classification: '',
    stderr: '',
    lastSampleAt: null,
    paused: false,
    visible: true,
    connLost: false,
  },
  forwards: [],
})

function fakeServices(port: number, statusResult?: PortsStatusResult): PortsPanelServices {
  const st = statusResult ?? statusFixture(port)
  const status = (): Promise<PortsStatusResult> => Promise.resolve(st)
  return {
    status: vi.fn(status),
    sample: vi.fn(status),
    pause: vi.fn().mockResolvedValue({}),
    visible: vi.fn().mockResolvedValue({}),
    openForward: vi.fn().mockResolvedValue({}),
    stopForward: vi.fn().mockResolvedValue({}),
  }
}

/** A row in the forwarded state: a listener on the remote host and a running
 *  forward whose destination is the listener's derived destination, so the
 *  row owns its port's state. */
const forwardedStatusFixture = (): PortsStatusResult => ({
  profileId: 'ssh:p1:1',
  host: 'host.example',
  discovery: {
    state: 'available',
    listeners: [
      {
        family: 'ipv4' as const,
        address: '192.168.0.93',
        port: 9993,
        process: { evidence: 'known', name: 'node', pid: 123 },
      },
    ],
    probe: 'ss',
    probesTried: ['ss'],
    classification: '',
    stderr: '',
    lastSampleAt: null,
    paused: false,
    visible: true,
    connLost: false,
  },
  forwards: [
    {
      id: 'fwd-1',
      direction: 'local',
      requestedBind: { host: '127.0.0.1', port: 9993 },
      actualBind: { host: '127.0.0.1', port: 9993 },
      destination: '192.168.0.93:9993',
      caveat: '',
      scope: 'ports:ssh:p1:1',
      state: 'running',
      stopReason: null,
      error: null,
    },
  ],
})

describe('the detected row keeps the address first and primary (nocx-wzc4.9)', () => {
  it('stacks the address above the process, with the action beside them', async () => {
    const services = fakeServices(6768)
    const root = document.createElement('div')
    document.body.append(root)
    // Created once, outside the JSX: Solid wraps prop expressions in getters,
    // so building it inline yields a fresh control on every read.
    const pause = createPortsPauseControl()
    const filter = createPortsFilterControl()
    render(
      () => (
        <PortsPanel
          profileId={() => 'ssh:p1:1'}
          services={services}
          visible={() => true}
          pause={pause}
          filter={filter}
        />
      ),
      { container: root },
    )
    await waitFor(() => expect(root.querySelector('.ports-row__main')).not.toBeNull())
    const main = root.querySelector<HTMLElement>('.ports-row__main') as HTMLElement
    const text = main.querySelector<HTMLElement>('.ports-row__text')
    expect(text).not.toBeNull()

    // The text column holds the address and the process, in that order, so
    // neither can steal the other's width.
    const stacked = [...(text as HTMLElement).children]
    const addr = stacked.find((el) => el.classList.contains('ports-row__addr'))
    const proc = stacked.find((el) => el.classList.contains('ports-row__proc'))
    expect(addr).toBeDefined()
    expect(proc).toBeDefined()
    expect(stacked.indexOf(addr as HTMLElement)).toBeLessThan(stacked.indexOf(proc as HTMLElement))
    expect(addr?.textContent).toBe('127.0.0.1:6768')

    // A known process is a label, not a caution: quiet text, no chip.
    expect(text?.querySelector('.ui-badge')).toBeNull()
    expect(proc?.textContent).toBe('node (pid 123)')

    // The action sits beside the column, not inside it — inside the row's
    // action group, which is a SIBLING of the text column (nocx-4wbx: the
    // group is taken out of layout, so a hidden action reserves no width).
    const actions = [...main.children].find((el) => el.classList.contains('ports-row__actions'))
    expect(actions).toBeDefined()
    const action = (actions as HTMLElement).querySelector('.ui-icon-button')
    expect(action).toBeDefined()
    expect((text as HTMLElement).contains(actions as HTMLElement)).toBe(false)
    root.remove()
  })

  it('the stylesheet stacks the column and leaves the address unbounded', () => {
    const css: string = readFileSync(PORTS_CSS, 'utf8')
    const text = stripComments(extractRuleBlock(css, 'ports-row__text') ?? '')
    const addr = stripComments(extractRuleBlock(css, 'ports-row__addr') ?? '')
    const proc = stripComments(extractRuleBlock(css, 'ports-row__proc') ?? '')
    expect(text).not.toBe('')
    expect(addr).not.toBe('')
    expect(proc).not.toBe('')

    // The column is what removes the competition: a flex COLUMN, so the two
    // lines never share a width. If this reverts to a row, the defect is back.
    expect(text).toMatch(/flex-direction\s*:\s*column/)
    expect(text).toMatch(/min-width\s*:\s*0/)

    // The address carries no cap and no share of anything — it has the whole
    // line. The ellipsis is a floor for a pathological address, not the norm.
    expect(addr).not.toMatch(/max-width/)
    expect(addr).not.toMatch(/flex\s*:/)
    expect(addr).toMatch(/text-overflow\s*:\s*ellipsis/)

    // The process is in the quiet register beneath it.
    expect(proc).toMatch(/text-overflow\s*:\s*ellipsis/)
    expect(proc).toMatch(/--color-text-dim/)
  })
})

// ── The destination line (nocx-na05) ────────────────────────────────────
// The rework moved the forwarded destination onto its OWN line inside the
// stacked text column, but the CSS kept the 40% cap that made sense when it
// sat BESIDE the address. A forwarded row's destination is the entire point
// of the row — the remote end the bind reaches — so the cap cut it to two
// characters on a line nothing else was using. The address line above it is
// the local bind and is unaffected by this change.
describe('the forwarded destination renders in full on its own line (nocx-na05)', () => {
  it('shows the whole destination under the local bind', async () => {
    const services = fakeServices(9993, forwardedStatusFixture())
    const root = document.createElement('div')
    document.body.append(root)
    const pause = createPortsPauseControl()
    const filter = createPortsFilterControl()
    render(
      () => (
        <PortsPanel
          profileId={() => 'ssh:p1:1'}
          services={services}
          visible={() => true}
          pause={pause}
          filter={filter}
        />
      ),
      { container: root },
    )
    await waitFor(() => expect(root.querySelector('.ports-row__dest')).not.toBeNull())
    const main = root.querySelector<HTMLElement>('.ports-row__main') as HTMLElement
    const text = main.querySelector<HTMLElement>('.ports-row__text') as HTMLElement
    const stacked = [...text.children]
    const addr = stacked.find((el) => el.classList.contains('ports-row__addr'))
    const dest = stacked.find((el) => el.classList.contains('ports-row__dest'))

    // The local bind stays the primary line — the user asks "where do I
    // reach this" — and the destination sits beneath it in full: what the
    // cap used to cut to "19…" is the string the row exists to show.
    expect(addr?.textContent).toBe('127.0.0.1:9993')
    expect(dest?.textContent).toBe('→ 192.168.0.93:9993')
    expect(stacked.indexOf(dest as HTMLElement)).toBeGreaterThan(
      stacked.indexOf(addr as HTMLElement),
    )
    root.remove()
  })

  it('the stylesheet gives the destination the whole line, ellipsis as a floor', () => {
    const css: string = readFileSync(PORTS_CSS, 'utf8')
    const dest = stripComments(extractRuleBlock(css, 'ports-row__dest') ?? '')
    expect(dest).not.toBe('')

    // The 40% cap was written when the destination sat BESIDE the address
    // and the two competed for the rail. On its own line nothing else uses
    // the width, so the cap only ever cut the string the row exists to show.
    expect(dest).not.toMatch(/max-width/)
    expect(dest).not.toMatch(/flex\s*:/)

    // The floor stays: a genuinely over-long destination still ellipsises,
    // so the row can never overflow, and the line can shrink for it.
    expect(dest).toMatch(/text-overflow\s*:\s*ellipsis/)
    expect(dest).toMatch(/white-space\s*:\s*nowrap/)
    expect(dest).toMatch(/min-width\s*:\s*0/)
  })
})
// ── The hidden actions reserve no width (nocx-4wbx) ──────────────────────
// A forwarded row carries three hover-revealed actions (Copy, Open, Stop)
// where a plain detected row carries one. `opacity: 0` does not remove a
// flex item from layout, so the forwarded row's hidden buttons were still
// taking 70px from the text column — 52px more than the plain row's 26px —
// and the ADDRESS line, the row's primary key, truncated at the default rail
// width while the pointer was elsewhere. jsdom computes no layout, so the
// scrollWidth/clientWidth proof lives in the e2e spec; what this pins is the
// mechanism that makes it possible: the actions live in a group that is
// TAKEN OUT OF THE ROW'S LAYOUT until it is revealed, so a hidden action
// reserves nothing.
describe('the hidden actions reserve no width (nocx-4wbx)', () => {
  it("groups the forwarded row's three actions in a wrapper beside the text column", async () => {
    const services = fakeServices(9993, forwardedStatusFixture())
    const root = document.createElement('div')
    document.body.append(root)
    const pause = createPortsPauseControl()
    const filter = createPortsFilterControl()
    render(
      () => (
        <PortsPanel
          profileId={() => 'ssh:p1:1'}
          services={services}
          visible={() => true}
          pause={pause}
          filter={filter}
        />
      ),
      { container: root },
    )
    await waitFor(() => expect(root.querySelector('.ports-row__actions')).not.toBeNull())
    const main = root.querySelector<HTMLElement>('.ports-row__main') as HTMLElement
    const text = main.querySelector<HTMLElement>('.ports-row__text') as HTMLElement
    const actions = [...main.children].find((el) => el.classList.contains('ports-row__actions'))

    // The group is a SIBLING of the text column, never inside it: the CSS
    // can then take it out of the row's flex layout without touching the
    // column's width.
    expect(actions).toBeDefined()
    expect(text.contains(actions as HTMLElement)).toBe(false)

    // The forwarded row's three actions, in the order they appear.
    const ids = [...(actions as HTMLElement).querySelectorAll('.ui-icon-button')].map((b) =>
      b.getAttribute('data-testid'),
    )
    expect(ids).toEqual(['ports-copy', 'ports-open', 'ports-stop'])
    root.remove()
  })

  it('the stylesheet takes the group out of layout and reveals it on hover and focus', () => {
    const css: string = readFileSync(PORTS_CSS, 'utf8')
    const actions = stripComments(extractRuleBlock(css, 'ports-row__actions') ?? '')
    expect(actions).not.toBe('')

    // Out of flow: the group reserves NO width whether visible or not.
    // Position-absolute is the mechanism — opacity alone is exactly the
    // defect, so an opacity-only "fix" must fail this test.
    expect(actions).toMatch(/position\s*:\s*absolute/)
    expect(actions).toMatch(/right\s*:\s*0/)

    // Hidden until the row is pointed at or focused, then revealed in place.
    expect(actions).toMatch(/opacity\s*:\s*0/)

    // The reveal fires on hover AND on focus-within — keyboard users have no
    // hover (nocx-wzc4.11), so a reveal that dropped the focus path would
    // strand the actions for them.
    const clean = stripComments(css)
    const reveals = clean.match(/\.ports-row:(?:hover|focus-within)[^}]{0,80}\.ports-row__actions/g)
    expect(reveals).not.toBeNull()
    expect(reveals?.filter((r) => r.includes(':hover'))).toHaveLength(1)
    expect(reveals?.filter((r) => r.includes(':focus-within'))).toHaveLength(1)
  })
})
