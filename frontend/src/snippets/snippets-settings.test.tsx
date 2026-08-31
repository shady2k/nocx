// @vitest-environment jsdom
/**
 * Component-level acceptance for the snippets settings page (nocx-gjnr,
 * design §10.4, plan Task 10).
 *
 * Drives the real SnippetsSection the way a person drives it — the buttons,
 * not the handlers — against a client whose five methods are spied, and
 * asserts the surface and the wire together: the empty state's button
 * reaches snippets.create, an edit reaches snippets.update with the
 * unchanged id, a reorder sends the FULL id list, a delete goes through the
 * kit confirm and a cancelled one writes NOTHING, a refused save leaves the
 * draft on screen with the reason, and an unavailable store says so instead
 * of offering a write.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { SnippetsSection, type BodyEditorHost } from './snippets-settings'
import { SnippetsStore, type Snippet, type SnippetsClientLike } from './snippets-store'

/** A host that records what it was given and lets the test act as the
 *  person typing — jsdom does not emulate contenteditable input, so the
 *  real CM6 host cannot be typed into. The default (real) host has its own
 *  test below: that the field mounts and shows the body. */
class FakeBodyHost implements BodyEditorHost {
  private text = ''
  private onChange: ((t: string) => void) | undefined
  mounted = false
  mount(
    _parent: HTMLElement,
    _signal: AbortSignal,
    _extensions?: unknown,
    onDocChange?: (t: string) => void,
  ): void {
    this.mounted = true
    this.onChange = onDocChange
  }
  setDoc(text: string): void {
    this.text = text
    this.onChange?.(text)
  }
  doc(): string {
    return this.text
  }
  focus(): void {}
  dispose(): void {
    this.mounted = false
  }
  /** The person types. */
  type(text: string): void {
    this.setDoc(text)
  }
}

function snip(over: Partial<Snippet> & { id: string }): Snippet {
  return { title: over.id, body: 'body', ...over }
}

/**
 * A recording client over a mutable list: list reads it, create appends,
 * update replaces in place, delete removes, reorder permutes — so a write's
 * reload shows the change, exactly the round trip the real backend makes.
 */
function createHarness(initial: Snippet[] = [], opts: { firstListError?: Error } = {}) {
  const list: Snippet[] = [...initial]
  let next = 1
  const client: SnippetsClientLike = {
    // eslint-disable-next-line @typescript-eslint/require-await
    list: async () => {
      if (opts.firstListError) {
        const err = opts.firstListError
        opts.firstListError = undefined
        throw err
      }
      return { snippets: [...list] }
    },
    // eslint-disable-next-line @typescript-eslint/require-await
    create: async (title: string, body: string) => {
      const created = { id: `id-${next++}`, title, body }
      list.push(created)
      return created
    },
    // eslint-disable-next-line @typescript-eslint/require-await
    update: async (id: string, title: string, body: string) => {
      const at = list.findIndex((s) => s.id === id)
      if (at < 0) throw new Error('no such snippet')
      list[at] = { id, title, body }
      return list[at]
    },
    // eslint-disable-next-line @typescript-eslint/require-await
    remove: async (id: string) => {
      const at = list.findIndex((s) => s.id === id)
      if (at >= 0) list.splice(at, 1)
      return { id }
    },
    // eslint-disable-next-line @typescript-eslint/require-await
    reorder: async (ids: string[]) => {
      const by = new Map(list.map((s) => [s.id, s]))
      list.splice(0, list.length, ...ids.map((id) => by.get(id)!))
      return { snippets: [...list] }
    },
  }
  const spies = {
    list: vi.spyOn(client, 'list'),
    create: vi.spyOn(client, 'create'),
    update: vi.spyOn(client, 'update'),
    remove: vi.spyOn(client, 'remove'),
    reorder: vi.spyOn(client, 'reorder'),
  }
  const store = new SnippetsStore(client)
  const host = new FakeBodyHost()
  const view = render(() => <SnippetsSection store={store} createBodyHost={() => host} />)
  return { ...view, store, spies, host, list }
}

/** Every write method — the assertion "cancel performs zero writes" needs
 *  all of them, not the one the test happens to think of. */
function writeCalls(spies: ReturnType<typeof createHarness>['spies']): number {
  return (
    spies.create.mock.calls.length +
    spies.update.mock.calls.length +
    spies.remove.mock.calls.length +
    spies.reorder.mock.calls.length
  )
}

afterEach(() => {
  vi.clearAllMocks()
  cleanup()
  document.body.innerHTML = ''
})

function rows(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('.ui-collection-row'))
}

async function waitForRows(container: HTMLElement, count: number) {
  await vi.waitFor(() => {
    expect(rows(container).length).toBe(count)
  })
}

function clickButton(scope: HTMLElement, label: string) {
  const btn = Array.from(scope.querySelectorAll('.ui-button')).find(
    (b) => b.textContent?.trim() === label,
  )
  expect(btn, `button "${label}" not found`).toBeTruthy()
  fireEvent.click(btn!)
}

function dialog(): HTMLElement | null {
  return document.querySelector('.nocx-dialog')
}

function findConfirmDialog(message: string): HTMLElement | null {
  for (const d of document.querySelectorAll('.nocx-dialog')) {
    if (d.querySelector('.nocx-dialog__message')?.textContent === message) return d as HTMLElement
  }
  return null
}

function fill(scope: HTMLElement, id: string, value: string) {
  const field = scope.querySelector(`#${id}`) as HTMLInputElement
  expect(field, `field #${id} not found`).toBeTruthy()
  fireEvent.input(field, { target: { value } })
}

describe('the snippets settings page (nocx-gjnr)', () => {
  it('the empty state offers creation, and its BUTTON reaches the client', async () => {
    const { container, spies, host } = createHarness([])
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state')).toBeTruthy()
    })

    // The failure this asserts against: a manager that renders a list and
    // has no way to put anything in it (AGENTS.md, the connection manager).
    clickButton(container.querySelector('.ui-empty-state') as HTMLElement, '+ New snippet')
    const d = dialog()
    expect(d, 'the new-snippet dialog did not open').toBeTruthy()

    fill(d!, 'snippet-title', 'deploy')
    host.type('kubectl rollout status {{service}}')
    clickButton(d!, 'Create snippet')

    await vi.waitFor(() => {
      expect(spies.create).toHaveBeenCalledWith('deploy', 'kubectl rollout status {{service}}')
    })
    await waitForRows(container, 1)
  })

  it('editing a snippet saves it through update, with the id it already had', async () => {
    const { container, spies, host } = createHarness([snip({ id: 'a', title: 'one', body: 'ls' })])
    await waitForRows(container, 1)

    fireEvent.click(container.querySelector('[aria-label="Edit one"]')!)
    const d = dialog()!
    expect((d.querySelector('#snippet-title') as HTMLInputElement).value).toBe('one')
    expect(host.doc()).toBe('ls')

    fill(d, 'snippet-title', 'one renamed')
    host.type('ls -la')
    clickButton(d, 'Save snippet')

    await vi.waitFor(() => {
      expect(spies.update).toHaveBeenCalledWith('a', 'one renamed', 'ls -la')
    })
    expect(spies.create).not.toHaveBeenCalled()
  })

  it('dragging a row onto another sends the FULL id list in the new order', async () => {
    const { container, spies } = createHarness([
      snip({ id: 'a', title: 'first' }),
      snip({ id: 'b', title: 'second' }),
      snip({ id: 'c', title: 'third' }),
    ])
    await waitForRows(container, 3)

    // The affordance is the row itself — no arrow buttons, the way no other
    // list in this product has them (owner review).
    const dragRows = container.querySelectorAll<HTMLElement>('.sn-row')
    const data = new Map<string, string>()
    const dataTransfer = {
      setData: (k: string, v: string) => data.set(k, v),
      getData: (k: string) => data.get(k) ?? '',
    }
    fireEvent.dragStart(dragRows[1], { dataTransfer })
    fireEvent.dragOver(dragRows[0], { dataTransfer })
    fireEvent.drop(dragRows[0], { dataTransfer })

    await vi.waitFor(() => {
      expect(spies.reorder).toHaveBeenCalledWith(['b', 'a', 'c'])
    })
    // The order the page shows is the order the backend answered with.
    await vi.waitFor(() => {
      expect(
        rows(container).map((r) => r.querySelector('.ui-record-row__title')?.textContent),
      ).toEqual(['second', 'first', 'third'])
    })
  })

  it('Alt+ArrowUp moves the focused row, so the order is not mouse-only', async () => {
    const { container, spies } = createHarness([
      snip({ id: 'a', title: 'first' }),
      snip({ id: 'b', title: 'second' }),
    ])
    await waitForRows(container, 2)

    fireEvent.keyDown(container.querySelectorAll<HTMLElement>('.sn-row')[1], {
      key: 'ArrowUp',
      altKey: true,
    })

    await vi.waitFor(() => {
      expect(spies.reorder).toHaveBeenCalledWith(['b', 'a'])
    })
  })

  it('a filtered list cannot be reordered: "one place" would mean a place nobody can see', async () => {
    const { container, spies } = createHarness([
      snip({ id: 'a', title: 'first' }),
      snip({ id: 'b', title: 'second' }),
    ])
    await waitForRows(container, 2)
    const search = container.querySelector('input[type="search"]') as HTMLInputElement
    fireEvent.input(search, { target: { value: 'second' } })
    await waitForRows(container, 1)

    const row = container.querySelector<HTMLElement>('.sn-row')!
    expect(row.getAttribute('draggable')).toBe('false')
    fireEvent.keyDown(row, { key: 'ArrowUp', altKey: true })

    expect(spies.reorder).not.toHaveBeenCalled()
  })

  it('a delete happens only through the kit confirm, and a cancelled one writes nothing', async () => {
    const { container, spies } = createHarness([snip({ id: 'a', title: 'one' })])
    await waitForRows(container, 1)

    fireEvent.click(container.querySelector('[aria-label="Delete one"]')!)
    const confirm = findConfirmDialog('Delete "one"?')
    expect(confirm, 'the confirm did not open').toBeTruthy()
    clickButton(confirm!, 'Cancel')

    await vi.waitFor(() => {
      expect(findConfirmDialog('Delete "one"?')).toBeNull()
    })
    expect(writeCalls(spies)).toBe(0)
    await waitForRows(container, 1)

    fireEvent.click(container.querySelector('[aria-label="Delete one"]')!)
    clickButton(findConfirmDialog('Delete "one"?')!, 'OK')
    await vi.waitFor(() => {
      expect(spies.remove).toHaveBeenCalledWith('a')
    })
  })

  it('a rejected save leaves the draft on screen with the reason', async () => {
    const { container, spies, host } = createHarness([])
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state')).toBeTruthy()
    })
    spies.create.mockRejectedValueOnce(new Error('title exceeds 200 characters'))

    clickButton(container.querySelector('.ui-empty-state') as HTMLElement, '+ New snippet')
    const d = dialog()!
    fill(d, 'snippet-title', 'too long')
    host.type('echo hi')
    clickButton(d, 'Create snippet')

    // The dialog stays, the typing survives, and the backend's sentence is
    // ON the surface — a toast over a closed dialog would take both away.
    await vi.waitFor(() => {
      expect(dialog()?.textContent).toContain('title exceeds 200 characters')
    })
    expect((dialog()!.querySelector('#snippet-title') as HTMLInputElement).value).toBe('too long')
    expect(host.doc()).toBe('echo hi')
  })

  it('an empty title is refused before the wire, and says why', async () => {
    const { container, spies, host } = createHarness([])
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state')).toBeTruthy()
    })
    clickButton(container.querySelector('.ui-empty-state') as HTMLElement, '+ New snippet')
    const d = dialog()!
    host.type('echo hi')
    clickButton(d, 'Create snippet')

    await vi.waitFor(() => {
      expect(d.querySelector('#snippet-title__error')?.textContent).toBeTruthy()
    })
    expect(spies.create).not.toHaveBeenCalled()
  })

  it('the preview reports what the parser recognised — and what it did not', async () => {
    const { container, host } = createHarness([])
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state')).toBeTruthy()
    })
    clickButton(container.querySelector('.ui-empty-state') as HTMLElement, '+ New snippet')

    // A mistyped span with one closing brace is the failure the line exists
    // for: it matches nothing, so it would be fired literally.
    host.type('ssh {{env:host}} -p {{ask:port} {{secret:key}}')
    const preview = () => dialog()!.querySelector('.sn-preview')!
    await vi.waitFor(() => {
      expect(preview().textContent).toContain('{{env:host}}')
    })
    const text = preview().textContent ?? ''
    expect(text).toContain('{{ask:port}')
    expect(preview().querySelector('[data-recognised="false"]')?.textContent).toContain(
      '{{ask:port}',
    )
    expect(text).toContain('{{secret:key}}')
  })

  it('the preview line names an option list, a condition and a malformed tag', async () => {
    const { container, host } = createHarness([])
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state')).toBeTruthy()
    })
    clickButton(container.querySelector('.ui-empty-state') as HTMLElement, '+ New snippet')

    host.type('{{w=a|b}}\n{% if fast %}x{% endif %}\n{% if bad %}')
    const preview = () => dialog()!.querySelector('.sn-preview')!
    await vi.waitFor(() => {
      expect(preview().textContent).toContain('{{w=a|b}}')
    })
    const text = preview().textContent ?? ''
    expect(text).toContain('you will choose one of a, b')
    expect(text).toContain('kept only if you tick "fast"')
    // The unclosed block is the reason the whole body cannot fire, so it is
    // reported as a problem rather than as one more tick to offer.
    expect(text).toContain('no {% endif %}')
    expect(preview().querySelector('[data-recognised="false"]')?.textContent).toContain(
      'cannot be fired',
    )
  })

  it('an env key outside the table is reported in the preview, not silently accepted', async () => {
    const { container, host } = createHarness([])
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state')).toBeTruthy()
    })
    clickButton(container.querySelector('.ui-empty-state') as HTMLElement, '+ New snippet')
    host.type('cd {{env:nope}}')

    await vi.waitFor(() => {
      expect(dialog()!.querySelector('.sn-preview')?.textContent).toContain('{{env:nope}}')
    })
    expect(dialog()!.querySelector('.sn-preview [data-recognised="false"]')?.textContent).toContain(
      '{{env:nope}}',
    )
  })

  it('with the store unavailable the page states the reason and offers no write', async () => {
    const { container, spies } = createHarness([], {
      firstListError: new Error('snippets not available'),
    })

    await vi.waitFor(() => {
      expect(container.textContent).toContain('snippets not available')
    })
    // The soft degrade must be visible AND honest: no "+ New snippet"
    // anywhere, because there is nothing that could accept it (§11.5).
    const labels = Array.from(container.querySelectorAll('.ui-button')).map((b) =>
      b.textContent?.trim(),
    )
    expect(labels).not.toContain('+ New snippet')
    expect(writeCalls(spies)).toBe(0)
    expect(rows(container)).toHaveLength(0)
  })

  it('the real body editor mounts and shows the snippet being edited', async () => {
    // The default host is the CM6 EditableHost — the seam the fake stands in
    // for everywhere else. This is the test that keeps the substitution
    // honest: without it, the page could ship with no body editor at all.
    const client: SnippetsClientLike = {
      // eslint-disable-next-line @typescript-eslint/require-await
      list: async () => ({ snippets: [{ id: 'a', title: 'one', body: 'echo real host' }] }),
      create: () => Promise.reject(new Error('unused')),
      update: () => Promise.reject(new Error('unused')),
      remove: () => Promise.reject(new Error('unused')),
      reorder: () => Promise.reject(new Error('unused')),
    }
    const { container } = render(() => <SnippetsSection store={new SnippetsStore(client)} />)
    await waitForRows(container, 1)

    fireEvent.click(container.querySelector('[aria-label="Edit one"]')!)
    await vi.waitFor(() => {
      const lines = dialog()!.querySelectorAll('.cm-line')
      expect(Array.from(lines).map((l) => l.textContent)).toEqual(['echo real host'])
    })
  })
})
