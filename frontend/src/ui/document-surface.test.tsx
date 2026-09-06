// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import {
  DocumentSurface,
  type DocumentSurfaceHandle,
  type DocumentSurfaceProps,
} from './document-surface'

afterEach(cleanup)

const source = (overrides: Record<string, unknown> = {}): DocumentSurfaceProps => ({
  text: 'one\ntwo',
  documentKey: 'document',
  ariaLabel: 'Document source',
  search: 'enabled',
  height: 'field',
  readOnly: true,
  language: 'plain',
  presentation: {
    kind: 'source',
    source: { wrap: 'none', lineNumbers: false },
  },
  ...overrides,
})

describe('DocumentSurface', () => {
  it('makes read-only documents non-editable and ignores keystrokes', () => {
    render(() => <DocumentSurface {...source()} />)

    const content = document.querySelector('.cm-content') as HTMLElement
    expect(content.getAttribute('contenteditable')).toBe('false')
    fireEvent.keyDown(content, { key: 'a' })
    fireEvent.keyDown(content, { key: 'Backspace' })
    expect(content.textContent).toContain('one')
    expect(content.textContent).toContain('two')
  })

  it('reports the editable document through onChange', () => {
    const changes: string[] = []
    render(() => (
      <DocumentSurface
        {...source({
          readOnly: false,
          onChange: (text: string) => changes.push(text),
        })}
      />
    ))

    expect(document.querySelector('.cm-content')?.getAttribute('contenteditable')).toBe('true')
    expect(changes).toContain('one\ntwo')
  })

  it('opens the search panel through its public handle', () => {
    render(() => (
      <DocumentSurface
        {...source({
          onHandle: (next: DocumentSurfaceHandle | null) => next?.openSearch(),
        })}
      />
    ))

    expect(document.querySelector('.cm-search')).not.toBeNull()
  })

  it('only renders line numbers when the source asks for them', () => {
    const { unmount } = render(() => <DocumentSurface {...source()} />)
    expect(document.querySelector('.cm-lineNumbers')).toBeNull()

    unmount()
    render(() => (
      <DocumentSurface
        {...source({
          presentation: {
            kind: 'source' as const,
            source: { wrap: 'none' as const, lineNumbers: true },
          },
        })}
      />
    ))
    expect(document.querySelector('.cm-lineNumbers')).not.toBeNull()
  })

  it('renders markdown as a document preview', () => {
    render(() => (
      <DocumentSurface
        {...source({
          text: '# Heading\n\n- item',
          language: 'markdown' as const,
          presentation: { kind: 'preview' as const },
        })}
      />
    ))

    expect(document.querySelector('.ui-md-body h1')?.textContent).toBe('Heading')
    expect(screen.getByText('item')).toBeTruthy()
  })

  it('keeps the source mounted while switching between source and preview', () => {
    const [view, setView] = createSignal<'source' | 'preview'>('source')
    render(() => (
      <DocumentSurface
        text="# Heading"
        documentKey="document"
        ariaLabel="Document"
        search="disabled"
        height="fill"
        readOnly
        language="markdown"
        presentation={{
          kind: 'markdown',
          view: view(),
          onViewChange: setView,
          source: { wrap: 'soft', lineNumbers: false },
        }}
      />
    ))

    const sourcePane = document.querySelector('.ui-document-surface__source')!
    expect(sourcePane.querySelector('.cm-editor')).not.toBeNull()
    setView('preview')
    expect(sourcePane.querySelector('.cm-editor')).not.toBeNull()
    expect(sourcePane.hasAttribute('hidden')).toBe(true)
    setView('source')
    expect(sourcePane.querySelector('.cm-editor')).not.toBeNull()
    expect(sourcePane.hasAttribute('hidden')).toBe(false)
  })

  it('revealing a line from preview switches to source first', () => {
    const [view, setView] = createSignal<'source' | 'preview'>('preview')
    render(() => (
      <DocumentSurface
        text="# Heading\n\nBody"
        documentKey="document"
        ariaLabel="Document"
        search="disabled"
        height="fill"
        readOnly
        language="markdown"
        presentation={{
          kind: 'markdown',
          view: view(),
          onViewChange: setView,
          source: { wrap: 'soft', lineNumbers: true },
        }}
        onHandle={(next: DocumentSurfaceHandle | null) => next?.revealLine(2)}
      />
    ))

    expect(document.querySelector('.ui-document-surface')?.getAttribute('data-view')).toBe('source')
    expect(document.querySelector('.ui-document-surface__source')?.hasAttribute('hidden')).toBe(
      false,
    )
  })
})
