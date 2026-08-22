// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { Tab, type TabProps } from './tab'

afterEach(() => cleanup())

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

  it('prefixes a sandboxed tab name with the shield before every other marker', () => {
    subject({ sandboxed: true, pinned: true, warning: true, title: 'Project shell' })

    const line = screen.getByRole('tab').querySelector('.nocx-tab-line')
    expect(line?.firstElementChild?.classList.contains('nocx-tab-sandboxed-marker')).toBe(true)
    expect(line?.firstElementChild?.querySelector('svg')).not.toBeNull()
    expect(line?.querySelector('.nocx-tab-title')?.textContent).toBe('Project shell')
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
