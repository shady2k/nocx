import type { GrantBlock } from './ask-entry'
import { FloatingPanel, type FloatingPanelRow } from './ui/floating-panel'

export type { GrantBlock }

export interface GrantControllerOptions {
  chip?: HTMLButtonElement
  onChange?: (blocks: ReadonlyArray<GrantBlock>) => void
}

/**
 * The single renderer-owned surface for the block grant. It owns the chip and
 * its popover, while TerminalContent owns the marked blocks and supplies the
 * current list. The chip is a count, never a second list of command names.
 */
export class GrantController {
  readonly chip: HTMLButtonElement
  private readonly panel: FloatingPanel
  private readonly onChange: ((blocks: ReadonlyArray<GrantBlock>) => void) | undefined
  private blocks: GrantBlock[] = []
  private readonly paintedBlocks = new Set<HTMLElement>()
  private mounted = false
  private readonly paintedRows = new Set<HTMLElement>()
  private readonly ownsChip: boolean
  constructor(options: GrantControllerOptions = {}) {
    this.onChange = options.onChange
    this.ownsChip = options.chip === undefined
    this.chip = options.chip ?? document.createElement('button')
    if (this.ownsChip) {
      this.chip.type = 'button'
      this.chip.className = 'nocx-chip nocx-editor-grant'
      this.chip.addEventListener('click', () => this.toggle())
    }
    this.panel = new FloatingPanel({
      variant: 'grant',
      role: 'listbox',
      ariaLabel: 'marked for the question',
      dismissBoundary: this.chip,
      callbacks: {
        onPick: (index) => this.reveal(index),
        onDismiss: () => this.panel.hide(),
      },
    })
    this.updateChip()
  }
  toggle(): void {
    if (this.panel.isOpen) this.panel.hide()
    else if (this.blocks.length > 0) this.renderPanel()
  }

  mount(container: HTMLElement): void {
    if (this.mounted) return
    this.mounted = true
    if (this.ownsChip) container.append(this.chip)
    this.panel.mount(container)
  }

  setBlocks(blocks: ReadonlyArray<GrantBlock>): void {
    this.blocks = [...blocks]
    this.repaintBlocks()
    this.updateChip()
    if (this.panel.isOpen) {
      if (this.blocks.length > 0) this.renderPanel()
      else this.panel.hide()
    }
  }

  get current(): ReadonlyArray<GrantBlock> {
    return this.blocks
  }

  destroy(): void {
    this.panel.destroy()
    for (const block of this.paintedBlocks) delete block.dataset.granted
    for (const row of this.paintedRows) delete row.dataset.granted
    this.paintedBlocks.clear()
    this.paintedRows.clear()
    if (this.ownsChip) this.chip.remove()
    this.blocks = []
    this.mounted = false
  }

  private updateChip(): void {
    const count = this.blocks.length
    this.chip.dataset.state = count === 0 ? 'default' : 'chosen'
    this.chip.textContent = `marked for the question · ${count}`
    this.chip.title =
      count === 0 ? 'Mark blocks to include them in a question' : 'Open the marked blocks'
    this.chip.setAttribute('aria-label', `marked for the question · ${count}`)
  }

  private renderPanel(): void {
    const rows: FloatingPanelRow[] = this.blocks.map((grant) => ({
      id: grant.itemId,
      displayText: grant.command,
      matchRanges: [],
      actions: [this.dismissButton(grant.itemId)],
    }))
    const footer = document.createElement('div')
    footer.className = 'ui-floating-panel__footer'
    const dismissAll = document.createElement('button')
    dismissAll.type = 'button'
    dismissAll.className = 'ui-context-menu__item'
    dismissAll.dataset.action = 'dismiss-all-grants'
    dismissAll.textContent = 'Dismiss all'
    dismissAll.addEventListener('click', (event) => {
      event.stopPropagation()
      this.blocks = []
      this.repaintBlocks()
      this.updateChip()
      this.onChange?.(this.blocks)
      this.panel.hide()
    })
    footer.appendChild(dismissAll)
    this.panel.show({ rows, selectedIndex: -1, after: [footer] })
  }

  private dismissButton(itemId: string): HTMLButtonElement {
    const button = document.createElement('button')
    button.type = 'button'
    button.className = 'ui-context-menu__item'
    button.dataset.action = 'dismiss-grant'
    button.dataset.itemId = itemId
    button.setAttribute('aria-label', 'Dismiss this mark')
    button.textContent = '×'
    button.addEventListener('mousedown', (event) => event.stopPropagation())
    button.addEventListener('click', (event) => {
      event.stopPropagation()
      this.blocks = this.blocks.filter((grant) => grant.itemId !== itemId)
      this.repaintBlocks()
      this.updateChip()
      this.onChange?.(this.blocks)
      if (this.blocks.length === 0) this.panel.hide()
      else this.renderPanel()
    })
    return button
  }
  private reveal(index: number): void {
    const grant = this.blocks[index]
    if (!grant) return
    grant.blockEl.scrollIntoView?.({ block: 'center', behavior: 'smooth' })
    const marked: HTMLElement[] =
      grant.start !== undefined && grant.count !== undefined
        ? Array.from(grant.blockEl.querySelectorAll<HTMLElement>('.term-line')).slice(
            grant.start,
            grant.start + grant.count,
          )
        : [grant.blockEl]
    for (const element of marked) {
      element.classList.remove('cmd-block-grant-flash')
      void element.offsetWidth
      element.classList.add('cmd-block-grant-flash')
    }
  }

  private repaintBlocks(): void {
    for (const block of this.paintedBlocks) delete block.dataset.granted
    for (const row of this.paintedRows) delete row.dataset.granted
    this.paintedBlocks.clear()
    this.paintedRows.clear()
    for (const grant of this.blocks) {
      if (grant.start !== undefined && grant.count !== undefined) {
        const rows = Array.from(grant.blockEl.querySelectorAll<HTMLElement>('.term-line')).slice(
          grant.start,
          grant.start + grant.count,
        )
        for (const row of rows) {
          row.dataset.granted = 'true'
          this.paintedRows.add(row)
        }
      } else {
        grant.blockEl.dataset.granted = 'true'
        this.paintedBlocks.add(grant.blockEl)
      }
    }
  }
}
