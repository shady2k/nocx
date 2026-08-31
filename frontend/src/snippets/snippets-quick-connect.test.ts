// The snippets provider for the quick-connect palette (nocx-8rtr, owner
// review). The surface swap landed without tests of its own — the e2e gate
// then caught what they would have: a delivered fire left the keyboard on
// the document, so the Enter that submits what was just inserted reached
// nobody.
import { describe, expect, it, vi } from 'vitest'
import { SnippetsQuickConnectProvider } from './snippets-quick-connect'
import { SnippetsStore, type Snippet, type SnippetsClientLike } from './snippets-store'
import type { SnippetFireOutcome } from './fire'

const SNIP = (over: Partial<Snippet> & { id: string }): Snippet => ({
  title: over.id,
  body: 'echo hi',
  ...over,
})

function harness(
  snippets: Snippet[],
  outcome: SnippetFireOutcome = { kind: 'delivered', where: 'pty' },
) {
  const client: SnippetsClientLike = {
    list: vi.fn().mockResolvedValue({ snippets }),
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
    reorder: vi.fn(),
  }
  const fire = vi.fn().mockResolvedValue(outcome)
  const onRefused = vi.fn()
  const onManage = vi.fn()
  const onAsk = vi.fn()
  const onDelivered = vi.fn()
  const onCopied = vi.fn()
  const provider = new SnippetsQuickConnectProvider({
    store: new SnippetsStore(client),
    fire,
    onRefused,
    onManage,
    onAsk,
    onDelivered,
    onCopied,
  })
  return { provider, fire, onRefused, onManage, onAsk, onDelivered, onCopied }
}

describe('the snippets palette provider', () => {
  it('answers with one row per snippet, in stored order, plus Manage last', async () => {
    const h = harness([SNIP({ id: 'b', title: 'second' }), SNIP({ id: 'a', title: 'first' })])
    const items = await h.provider.getItems()
    expect(items.map((i) => i.label)).toEqual(['second', 'first'])
    expect(h.provider.getTrailingItems().map((i) => i.label)).toEqual(['Manage snippets…'])
    // One kind set of its own: a row whose Enter types into the pane in
    // front must not sit in the server list or the command palette.
    expect(items.every((i) => i.kind === 'snippet')).toBe(true)
  })

  it('a plain body fires straight from the row, and the keyboard goes back to the pane', async () => {
    const h = harness([SNIP({ id: 'a', title: 'plain', body: 'echo hi' })])
    const items = await h.provider.getItems()
    items[0].run()

    await vi.waitFor(() => {
      expect(h.fire).toHaveBeenCalledTimes(1)
    })
    // The person fired INTO something; their next keystroke belongs to it
    // (design §9.5). The e2e gate is what proved this missing.
    await vi.waitFor(() => {
      expect(h.onDelivered).toHaveBeenCalledTimes(1)
    })
    expect(h.onRefused).not.toHaveBeenCalled()
    expect(h.onAsk).not.toHaveBeenCalled()
  })

  it('a body with ask fields does NOT fire from the row: the form asks first', async () => {
    const h = harness([SNIP({ id: 'a', title: 'asks', body: 'ssh -p {{ask:port=22}} h' })])
    const items = await h.provider.getItems()
    items[0].run()

    expect(h.onAsk).toHaveBeenCalledTimes(1)
    expect(h.onAsk.mock.calls[0][0]).toMatchObject({ id: 'a' })
    expect(h.fire).not.toHaveBeenCalled()
  })

  it('a refusal goes back to the palette as a sentence, and the keyboard stays put', async () => {
    const h = harness([SNIP({ id: 'a', title: 'plain' })], {
      kind: 'refused',
      reason: { kind: 'multi-line-no-bracketed-paste' },
    })
    const items = await h.provider.getItems()
    items[0].run()

    await vi.waitFor(() => {
      expect(h.onRefused).toHaveBeenCalledTimes(1)
    })
    expect(h.onRefused.mock.calls[0][0]).toContain('bracketed paste')
    // Nothing was inserted, so nothing took the keyboard: the person is
    // still looking at the list, deciding what to do about it.
    expect(h.onDelivered).not.toHaveBeenCalled()
  })

  // nocx-8rtr.2 — the clipboard is a destination a person chooses, not a
  // remedy they are offered after being told no. Before these,
  // SnippetDestination had one caller outside its own tests.
  it('every snippet row offers the clipboard, named so a screen reader can say which', async () => {
    const h = harness([SNIP({ id: 'a', title: 'Orchestrator' })])
    const [row] = await h.provider.getItems()
    expect(row.action?.ariaLabel).toBe('Copy "Orchestrator" to the clipboard')
  })

  it('taking it fires the SAME adapter at the clipboard, and says it landed', async () => {
    const h = harness([SNIP({ id: 'a', title: 'Orchestrator', body: 'plain' })], {
      kind: 'delivered',
      where: 'clipboard',
    })
    const [row] = await h.provider.getItems()
    row.action?.run()
    await vi.waitFor(() => expect(h.fire).toHaveBeenCalled())
    expect(h.fire.mock.calls[0][0]).toMatchObject({ destination: 'clipboard' })
    await vi.waitFor(() => expect(h.onCopied).toHaveBeenCalledWith('Orchestrator'))
    // The pane keeps the keyboard: nothing was inserted into it.
    expect(h.onDelivered).not.toHaveBeenCalled()
  })

  it('a body with fields is answered FIRST, for the destination the row chose', async () => {
    const h = harness([SNIP({ id: 'a', title: 'T', body: 'run {{ask:host}}' })])
    const [row] = await h.provider.getItems()
    row.action?.run()
    await vi.waitFor(() => expect(h.onAsk).toHaveBeenCalled())
    expect(h.onAsk.mock.calls[0][1]).toBe('clipboard')
    // Nothing was fired: the answers are not known yet.
    expect(h.fire).not.toHaveBeenCalled()
  })

  it('a refused copy says why — a secret must not outlive the fire on the clipboard', async () => {
    const h = harness([SNIP({ id: 'a', title: 'T', body: 'plain' })], {
      kind: 'refused',
      reason: { kind: 'secret-to-clipboard', name: 'prod-db' },
    })
    const [row] = await h.provider.getItems()
    row.action?.run()
    await vi.waitFor(() => expect(h.onRefused).toHaveBeenCalled())
    expect(h.onRefused.mock.calls[0][0]).toContain('prod-db')
    expect(h.onCopied).not.toHaveBeenCalled()
  })

  it('fireReporting hands the reason to its caller instead of the palette', async () => {
    // The ask form owns a surface of its own, so it shows the refusal there
    // — beside the answers that caused it.
    const h = harness([], { kind: 'refused', reason: { kind: 'no-owner' } })
    const message = await h.provider.fireReporting(SNIP({ id: 'a' }), new Map(), 'input')
    expect(message).toContain('no terminal or editor')
    expect(h.onRefused).not.toHaveBeenCalled()
  })

  it('with the store unavailable it offers the reason and no row that pretends to fire', async () => {
    const client: SnippetsClientLike = {
      list: vi.fn().mockRejectedValue(new Error('snippets not available')),
      create: vi.fn(),
      update: vi.fn(),
      remove: vi.fn(),
      reorder: vi.fn(),
    }
    const provider = new SnippetsQuickConnectProvider({
      store: new SnippetsStore(client),
      fire: vi.fn(),
      onRefused: vi.fn(),
      onManage: vi.fn(),
      onAsk: vi.fn(),
      onDelivered: vi.fn(),
      onCopied: vi.fn(),
    })
    const items = await provider.getItems()
    expect(items).toHaveLength(1)
    expect(items[0].label).toContain("Couldn't load")
    expect(items[0].detail).toContain('snippets not available')
  })
})
