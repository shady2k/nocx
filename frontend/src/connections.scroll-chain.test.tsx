// @vitest-environment jsdom
/**
 * The connections list's scroll chain, guarded (nocx-mx27).
 *
 * nocx-dzi1 was a broken flex height chain: `.cm-root` and
 * `.ui-collection-view` claimed no height, so `.ui-collection-view__body`
 * never received a bounded one and the list clipped instead of scrolling.
 * The repair was four CSS declarations, and nothing asserted them — jsdom
 * computes no layout, so no test here could go red when someone dropped
 * `min-height: 0` again.
 *
 * ── Why this test reads files from disk ──
 * `ui/scroll-ownership.test.tsx` works around the same jsdom limitation by
 * injecting a hand-copied CSS string. That proves the invariant about the
 * copy, not about the shipped rule: deleting `min-height: 0` from the real
 * stylesheet leaves the copy — and the test — untouched. So this one reads
 * the three real stylesheets the chain spans, the way
 * `ui/match-contrast.test.ts` already reads `floating-panel.css` to prove a
 * shipped rule rather than a restated one.
 *
 * ── The two halves ──
 *  1. The DOM half renders the real ConnectionsView inside a real
 *     `Page scrollMode="contained"` and WALKS from the scroll owner up to
 *     the page root. The chain is discovered, not asserted from memory, so
 *     inserting a wrapper element puts that wrapper under the same
 *     obligation as everything else.
 *  2. The CSS half then requires every link in the walked chain to declare,
 *     in the shipped stylesheet, that it grows (`flex-grow: 1`) and that its
 *     automatic minimum size is not its content (`min-height: 0`). Those two
 *     together are what "the parent's height reaches the child" means, and
 *     they are exactly what broke.
 *
 * The scroll owner itself is exempt from `min-height: 0` — a flex item whose
 * overflow is not `visible` already has an automatic minimum size of zero, so
 * its `overflow-y: auto` does that job. It is asserted to have that overflow
 * instead.
 *
 * A real browser still owns the proof that the resolved layout scrolls; the
 * e2e suite is where that lives. This is the structural guard beneath it.
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, cleanup, waitFor } from '@solidjs/testing-library'
import { Page } from './ui/page'
import { ConnectionsView } from './connections'
import { ProfileClient, type SSHProfile } from './profiles'
import { Dispatcher } from './dispatcher'

const dirname =
  (import.meta as { dirname?: string }).dirname ?? resolve(new URL('.', import.meta.url).pathname)

/** The stylesheets the chain spans: the page frame, the connections surface,
 *  and the collection-view kit component. Read from disk on purpose — see the
 *  file comment. */
const STYLESHEETS = [
  'styles/base.css',
  'styles/surfaces/connections.css',
  'styles/components/collection-view.css',
].map((p) => readFileSync(resolve(dirname, p), 'utf8'))

type Rule = { selectors: string[]; body: string }

/** Top-level rules only. An at-rule block (`@media`) is skipped whole: a
 *  declaration that only holds at some viewport width does not hold. */
function topLevelRules(css: string): Rule[] {
  const rules: Rule[] = []
  const source = css.replace(/\/\*[\s\S]*?\*\//g, '')
  let depth = 0
  let head = ''
  let body = ''
  for (const ch of source) {
    if (ch === '{') {
      depth++
      if (depth === 1) {
        body = ''
        continue
      }
    } else if (ch === '}') {
      depth--
      if (depth === 0) {
        const selector = head.trim()
        if (!selector.startsWith('@')) {
          rules.push({ selectors: selector.split(',').map((s) => s.trim()), body })
        }
        head = ''
        continue
      }
    }
    if (depth === 0) head += ch
    else body += ch
  }
  return rules
}

const RULES: Rule[] = STYLESHEETS.flatMap(topLevelRules)

/** The value a shipped rule gives `property` for an element carrying
 *  `classes`, or null. Every matching class contributes, later rules winning,
 *  which is how the cascade resolves it in the browser. */
function shippedValue(classes: readonly string[], property: string): string | null {
  let found: string | null = null
  const pattern = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
  for (const rule of RULES) {
    if (!rule.selectors.some((s) => classes.includes(s.replace(/^\./, '')) && s.startsWith('.'))) {
      continue
    }
    // A compound or descendant selector is not a rule about this element
    // alone; only a bare class selector is.
    if (!rule.selectors.some((s) => /^\.[\w-]+$/.test(s) && classes.includes(s.slice(1)))) continue
    const m = rule.body.match(pattern)
    if (m) found = m[1].trim()
  }
  return found
}

/** flex-grow, from either the `flex` shorthand or the longhand. `flex: 1 1 auto`
 *  and `flex: 1` both grow; `flex: 0 0 auto` does not. */
function flexGrow(classes: readonly string[]): string | null {
  const longhand = shippedValue(classes, 'flex-grow')
  if (longhand !== null) return longhand
  const shorthand = shippedValue(classes, 'flex')
  if (shorthand === null) return null
  return shorthand.trim().split(/\s+/)[0]
}

function identity(el: Element): string {
  return el.className || el.tagName.toLowerCase()
}

/** One connection is enough: CollectionView renders its body — the scroll
 *  owner — only when the list has items, and an empty list has nothing to
 *  scroll. So the chain under test exists exactly when it matters. */
const ONE_PROFILE: SSHProfile[] = [
  {
    id: 'ssh:p1',
    type: 'ssh',
    name: 'prod-web',
    options: {
      host: 'web.example.com',
      port: 22,
      user: 'deploy',
      keepaliveInterval: 0,
      keepaliveCountMax: 0,
      readyTimeout: 0,
      agentForward: false,
      canBeJumpServer: false,
    },
  },
]

/** A ProfileClient whose calls never reach a socket. The chain is a layout
 *  property; what the rows say is irrelevant to it. */
function mockClient(): ProfileClient {
  const pc = new ProfileClient(new Dispatcher())
  vi.spyOn(pc, 'listProfiles').mockResolvedValue(ONE_PROFILE)
  vi.spyOn(pc, 'listGroups').mockResolvedValue([])
  vi.spyOn(pc, 'sessionStatus').mockResolvedValue({ statuses: {} })
  vi.spyOn(pc, 'loadEffective').mockResolvedValue({ profiles: [] })
  return pc
}

/** The Connections page as Settings mounts it (settings.tsx registers the
 *  connections page with scrollMode 'contained'). Resolves once the list has
 *  loaded, which is when the scroll owner exists. */
async function renderConnectionsPage(): Promise<HTMLElement> {
  const { container } = render(() => (
    <Page title="Connections" scrollMode="contained">
      <ConnectionsView client={mockClient()} />
    </Page>
  ))
  await waitFor(() => expect(container.querySelector('.ui-collection-view__body')).not.toBeNull())
  return container
}

describe('the connections list scroll chain', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('renders the scroll owner inside the contained page', async () => {
    const container = await renderConnectionsPage()
    expect(container.querySelector('.ui-page__contained')).not.toBeNull()
    expect(container.querySelector('.ui-collection-view__body')).not.toBeNull()
  })

  it('every element between the page root and the scroll owner grows and may shrink', async () => {
    const container = await renderConnectionsPage()
    const owner = container.querySelector<HTMLElement>('.ui-collection-view__body')!
    const root = container.querySelector<HTMLElement>('.ui-page')!

    // Walk the real DOM rather than naming the chain from memory: a wrapper
    // added later joins the chain and inherits its obligations.
    const chain: HTMLElement[] = []
    for (let el = owner.parentElement; el && el !== root.parentElement; el = el.parentElement) {
      chain.push(el)
    }
    expect(chain.map(identity)).toContain('cm-root')
    expect(chain.map(identity)).toContain('ui-collection-view')

    const broken = chain
      .map((el) => {
        const classes = Array.from(el.classList)
        return { el, grow: flexGrow(classes), minHeight: shippedValue(classes, 'min-height') }
      })
      .filter((link) => link.grow !== '1' || link.minHeight !== '0')
      .map(
        (link) => `${identity(link.el)} (flex-grow: ${link.grow}, min-height: ${link.minHeight})`,
      )

    expect(broken).toEqual([])
  })

  it('the scroll owner grows and owns the overflow', async () => {
    const container = await renderConnectionsPage()
    const owner = container.querySelector<HTMLElement>('.ui-collection-view__body')!
    const classes = Array.from(owner.classList)

    expect(flexGrow(classes)).toBe('1')
    // Not min-height: 0 — overflow that is not `visible` already zeroes a flex
    // item's automatic minimum size, and this declaration is what does it.
    expect(shippedValue(classes, 'overflow-y')).toBe('auto')
  })

  it('the contained page area keeps the flex chain out of the surface below it', () => {
    // base.css calls this boundary load-bearing: without it the chain reaches
    // into the child, which re-expands to content height and never bounds.
    expect(shippedValue(['ui-page__contained'], 'overflow')).toBe('hidden')
  })
})
