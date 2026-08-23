// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { Tab, type TabProps } from './tab'

afterEach(() => cleanup())

/** A jsdom stand-in for DataTransfer, which jsdom does not implement. It
 *  records what the drag carries — the `types` list is the only thing a
 *  document-level listener can read mid-drag, and it is exactly what Wails'
 *  file-drop listeners test before they act. */
function makeDataTransfer(): DataTransfer {
  const data = new Map<string, string>()
  return {
    get types() {
      return [...data.keys()]
    },
    files: [] as unknown as FileList,
    setData: (type: string, value: string) => data.set(type, value),
    getData: (type: string) => data.get(type) ?? '',
    setDragImage: () => {},
  } as unknown as DataTransfer
}

const defaults: TabProps = {
  id: 'tab-btn-42',
  paneId: 42,
  controlledPaneId: 'pane-42',
  index: 0,
  active: false,
  agentStatus: null,
  title: 'Terminal',
  tooltip: 'Session #42',
  hasActivity: false,
  tabIndex: -1,
  onActivate: vi.fn(),
  onClose: vi.fn(),
  onReorder: vi.fn(),
}

function subject(overrides?: Partial<TabProps>) {
  return render(() => <Tab {...defaults} {...overrides} />)
}

describe('Tab', () => {
  it('renders with base class nocx-tab', () => {
    subject()
    const tab = screen.getByRole('tab')
    expect(tab.classList.contains('nocx-tab')).toBe(true)
  })

  it('has no extra classes beyond nocx-tab', () => {
    subject()
    const tab = screen.getByRole('tab')
    expect(tab.classList.length).toBe(1)
    expect(tab.classList[0]).toBe('nocx-tab')
  })

  it('has role tab', () => {
    subject()
    const tab = screen.getByRole('tab')
    expect(tab).toBeTruthy()
  })

  it('sets aria-controls from paneId', () => {
    subject({ controlledPaneId: 'pane-99' })
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('aria-controls')).toBe('pane-99')
  })

  it('sets aria-selected when active', () => {
    subject({ active: true })
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('aria-selected')).toBe('true')
  })

  it('omits aria-selected when not active', () => {
    subject({ active: false })
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('aria-selected')).toBe('false')
  })

  it('sets data-pane-id', () => {
    subject({ paneId: 99 })
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('data-pane-id')).toBe('99')
  })

  it('sets id from prop', () => {
    subject({ id: 'tab-btn-7' })
    const tab = screen.getByRole('tab')
    expect(tab.id).toBe('tab-btn-7')
  })

  it('sets data-agent-status when agentStatus is working', () => {
    subject({ agentStatus: 'working' })
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('data-agent-status')).toBe('working')
  })

  it('sets data-agent-status when agentStatus is idle', () => {
    subject({ agentStatus: 'idle' })
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('data-agent-status')).toBe('idle')
  })

  it('omits data-agent-status when agentStatus is null', () => {
    subject({ agentStatus: null })
    const tab = screen.getByRole('tab')
    expect(tab.hasAttribute('data-agent-status')).toBe(false)
  })

  it('sets title (tooltip)', () => {
    subject({ tooltip: 'My terminal' })
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('title')).toBe('My terminal')
  })

  it('sets tabIndex', () => {
    subject({ tabIndex: 0 })
    const tab = screen.getByRole('tab')
    expect(tab.tabIndex).toBe(0)
  })

  it('displays index + 1', () => {
    subject({ index: 2 })
    const tab = screen.getByRole('tab')
    expect(tab.textContent).toContain('3')
  })

  it('displays title', () => {
    subject({ title: 'ssh session' })
    const tab = screen.getByRole('tab')
    expect(tab.textContent).toContain('ssh session')
  })

  it('renders a close button with aria-label', () => {
    subject()
    const closeBtn = screen.getByLabelText('Close tab')
    expect(closeBtn).toBeTruthy()
  })

  it('calls onActivate when clicked', () => {
    const onActivate = vi.fn()
    subject({ onActivate })
    const tab = screen.getByRole('tab')
    fireEvent.click(tab)
    expect(onActivate).toHaveBeenCalledTimes(1)
  })

  it('calls onClose when close button clicked', () => {
    const onClose = vi.fn()
    subject({ onClose })
    const closeBtn = screen.getByLabelText('Close tab')
    fireEvent.click(closeBtn)
    expect(onClose).toHaveBeenCalledWith(42)
  })

  it('has nocx-tab-index element', () => {
    subject()
    const el = document.querySelector('.nocx-tab-index')
    expect(el).toBeTruthy()
  })

  it('has nocx-tab-label element', () => {
    subject()
    const el = document.querySelector('.nocx-tab-label')
    expect(el).toBeTruthy()
  })

  it('has nocx-tab-status element', () => {
    subject()
    const el = document.querySelector('.nocx-tab-status')
    expect(el).toBeTruthy()
  })

  it('has nocx-tab-title element', () => {
    subject()
    const el = document.querySelector('.nocx-tab-title')
    expect(el).toBeTruthy()
  })

  it('has nocx-tab-indicator element', () => {
    subject()
    const el = document.querySelector('.nocx-tab-indicator')
    expect(el).toBeTruthy()
  })

  it('sets data-activity on indicator when hasActivity is true and not active', () => {
    subject({ hasActivity: true, active: false })
    const indicator = document.querySelector('.nocx-tab-indicator')
    expect(indicator?.getAttribute('data-activity')).toBe('true')
  })

  it('omits data-activity on indicator when hasActivity is false', () => {
    subject({ hasActivity: false })
    const indicator = document.querySelector('.nocx-tab-indicator')
    expect(indicator?.hasAttribute('data-activity')).toBe(false)
  })

  it('omits data-activity on indicator when active (even if hasActivity)', () => {
    subject({ hasActivity: true, active: true })
    const indicator = document.querySelector('.nocx-tab-indicator')
    expect(indicator?.hasAttribute('data-activity')).toBe(false)
  })

  it('is draggable', () => {
    subject()
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('draggable')).toBe('true')
  })

  // ── EnableFileDrop must not eat the tab drag (nocx-9le.5.8) ────────────
  //
  // The desktop window turns on Wails' EnableFileDrop so a file dropped on a
  // terminal reaches Go (main.go). Wails installs document-level
  // dragenter/dragover/drop listeners that preventDefault, which would kill
  // every drag on the page — except that each one returns immediately unless
  // the drag's `types` contain 'Files' (v3.0.0-beta.9 window.ts:712). A tab
  // drag carries application/x-nocx-tab and text/plain, so it passes through
  // untouched.
  //
  // These two tests are that argument asserted rather than believed. The
  // first says what a tab drag puts on the wire; the second says the whole
  // reorder still completes. Neither can fail today — which is the point: a
  // runtime bump that widened Wails' check, or a change here that put a file
  // on the drag, would otherwise break tab reordering in the shipped app
  // with every unit test green.
  it('a tab drag is not a files drag, so the window-level file-drop listeners let it through', () => {
    subject()
    const tab = screen.getByRole('tab')
    const dataTransfer = makeDataTransfer()
    fireEvent.dragStart(tab, { dataTransfer })

    expect(dataTransfer.types).toContain('application/x-nocx-tab')
    expect(dataTransfer.types).not.toContain('Files')
    expect(dataTransfer.files.length).toBe(0)
  })

  it('reorders on a drop whose types carry no Files entry', () => {
    const onReorder = vi.fn()
    render(() => <Tab {...defaults} paneId={7} onReorder={onReorder} />)
    render(() => <Tab {...defaults} paneId={9} onReorder={onReorder} />)
    const rows = screen.getAllByRole('tab')

    const dataTransfer = makeDataTransfer()
    fireEvent.dragStart(rows[0], { dataTransfer })
    fireEvent.dragOver(rows[1], { dataTransfer, clientX: 0, clientY: 0 })
    fireEvent.drop(rows[1], { dataTransfer })

    expect(dataTransfer.types).not.toContain('Files')
    // The dragged pane and the pane it landed on. The third argument is
    // WHICH SIDE, decided from the row's bounding rect — jsdom reports every
    // rect as zeros, so the side it computes here says nothing about the
    // real strip and is deliberately not asserted. That the reorder happens
    // at all is what a file-drop listener eating the drag would take away.
    expect(onReorder).toHaveBeenCalledTimes(1)
    expect(onReorder.mock.calls[0].slice(0, 2)).toEqual([7, 9])
  })

  it('marks the row hidden with data-hidden when the hidden prop is true', () => {
    subject({ hidden: true })
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('data-hidden')).toBe('true')
    expect(tab.classList.contains('nocx-tab')).toBe(true)
  })

  it('omits data-hidden when the hidden prop is false', () => {
    subject({ hidden: false })
    const tab = screen.getByRole('tab')
    expect(tab.hasAttribute('data-hidden')).toBe(false)
    expect(tab.classList.length).toBe(1)
  })

  it('renders subtitle element in vertical mode when the title is a name', () => {
    subject({ orientation: 'vertical', tooltip: '~/repos/nocx', subtitle: '~/repos/nocx' })
    const subtitle = document.querySelector('.nocx-tab-subtitle')
    expect(subtitle).toBeTruthy()
    expect(subtitle?.textContent).toBe('~/repos/nocx')
  })

  it('omits subtitle element in horizontal mode', () => {
    subject({ orientation: 'horizontal', tooltip: '~/repos/nocx', subtitle: '~/repos/nocx' })
    const subtitle = document.querySelector('.nocx-tab-subtitle')
    expect(subtitle).toBeNull()
  })

  // A plain local tab is titled after its own directory, so the content pushes an
  // empty subtitle rather than printing the first line twice.
  it('omits the subtitle when the content pushed an empty one', () => {
    subject({ orientation: 'vertical', tooltip: '~/repos/nocx', subtitle: '' })
    expect(document.querySelector('.nocx-tab-subtitle')).toBeNull()
  })

  it('omits subtitle element in vertical mode when there is no location to show', () => {
    subject({ orientation: 'vertical', tooltip: '', subtitle: '' })
    const subtitle = document.querySelector('.nocx-tab-subtitle')
    expect(subtitle).toBeNull()
  })

  it('renders subtitle element in vertical mode when tooltip is non-empty', () => {
    subject({ orientation: 'vertical', tooltip: '~/repos/nocx', subtitle: '~/repos/nocx' })
    const subtitle = document.querySelector('.nocx-tab-subtitle')
    expect(subtitle).toBeTruthy()
    expect(subtitle?.textContent).toBe('~/repos/nocx')
  })

  // The subtitle shows the same text, but it ellipses — so the native tooltip is
  // the only way to read a long path in full and stays in both orientations.
  it('keeps the title attribute in vertical mode, where the subtitle ellipses', () => {
    subject({ orientation: 'vertical', tooltip: '~/repos/nocx', subtitle: '~/repos/nocx' })
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('title')).toBe('~/repos/nocx')
  })

  it('sets title attribute in horizontal mode (tooltip as native tooltip)', () => {
    subject({ orientation: 'horizontal', tooltip: '~/repos/nocx' })
    const tab = screen.getByRole('tab')
    expect(tab.getAttribute('title')).toBe('~/repos/nocx')
  })
})
