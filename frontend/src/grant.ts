import { grantRows, type GrantBlock } from './ask-entry'
import { FloatingPanel, type FloatingPanelRow } from './ui/floating-panel'
import { createIconButton } from './ui/icon-button-element'
import { createButton } from './ui/button-element'
import { CloseIcon, iconElement } from './ui/icons'

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
  /** The frozen frame is an attachment, not a person mark: keep it in the
   * same panel for discoverability, but outside the stored mark list and its
   * count. */
  private automaticBlock: GrantBlock | null = null
  private readonly paintedBlocks = new Set<HTMLElement>()
  private mounted = false
  private readonly paintedRows = new Set<HTMLElement>()
  private readonly ownsChip: boolean
  /** Presentation only: hidden grants remain the exact stored objects. */
  private visible = true

  constructor(options: GrantControllerOptions = {}) {
    this.onChange = options.onChange
    this.ownsChip = options.chip === undefined
    this.chip =
      options.chip ??
      createButton({
        label: '',
        variant: 'ghost',
        size: 'sm',
        truncate: true,
        onClick: () => this.toggle(),
      })
    if (this.ownsChip) this.chip.dataset.control = 'grant'
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
    if (!this.visible) return
    if (this.panel.isOpen) this.panel.hide()
    else if (this.blocks.length > 0 || this.automaticBlock !== null) this.renderPanel()
  }

  mount(container: HTMLElement): void {
    if (this.mounted) return
    this.mounted = true
    if (this.ownsChip) container.append(this.chip)
    this.panel.mount(container)
  }

  setBlocks(blocks: ReadonlyArray<GrantBlock>): void {
    this.blocks = blocks.filter((block) => !block.automatic)
    this.repaintBlocks()
    this.updateChip()
    if (this.panel.isOpen) {
      if (this.blocks.length > 0 || this.automaticBlock !== null) this.renderPanel()
      else this.panel.hide()
    }
  }

  setAutomaticBlock(block: GrantBlock | null): void {
    this.automaticBlock = block?.automatic === true ? block : null
    this.updateChip()
    if (this.panel.isOpen) {
      if (this.blocks.length > 0 || this.automaticBlock !== null) this.renderPanel()
      else this.panel.hide()
    }
  }

  /** The panel rows include the automatic attachment after person marks —
   *  unless a person mark has NARROWED it. Marking rows inside the frozen
   *  screen produces a mark on the screen's own item id plus a row span, and
   *  that band is what the question carries; listing the whole screen beside
   *  it would name two attachments where one travels (nocx-hp8p2.7). */
  private panelBlocks(): GrantBlock[] {
    const automatic = this.automaticBlock
    if (automatic === null) return [...this.blocks]
    const narrowed = this.blocks.some((block) => block.itemId === automatic.itemId)
    return narrowed ? [...this.blocks] : [...this.blocks, automatic]
  }

  /**
   * Project the stored grants onto the assistant surface without changing
   * their state. Hiding closes the panel and removes every paint marker;
   * showing reapplies those manifestations from the same grant objects.
   */
  setVisible(visible: boolean): void {
    this.visible = visible
    this.chip.style.display = visible ? '' : 'none'
    if (!visible) this.panel.hide()
    this.repaintBlocks()
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
    this.automaticBlock = null
    this.mounted = false
  }

  private updateChip(): void {
    const count = this.blocks.length
    this.chip.dataset.state = count === 0 ? 'default' : 'chosen'
    // SHORT ON THE CHIP, WHOLE IN THE NAME (nocx-hp8p2.5). The chip is a
    // count in a narrow box beside several others; the full sentence made it
    // twice its width and it was painted over its neighbours. The attachment
    // is already stated twice at full length beside it — the freeze badge on
    // the same row, and its own row in this chip's popover — so what belongs
    // here is the fact that something came with the question, not the
    // sentence about it. The accessible name and the title keep the sentence.
    const automatic = this.automaticBlock === null ? '' : ' + screen'
    const spoken = this.automaticBlock === null ? '' : ' · frozen screen attached automatically'
    this.chip.textContent = `marked for the question · ${count}${automatic}`
    // The chip ellipsises within its bounded width (Button's `data-truncate`,
    // composer.css), so the title carries the whole line rather than a label
    // about it — nothing may live only in the ellipsis.
    this.chip.title =
      count === 0 && this.automaticBlock === null
        ? 'Mark blocks to include them in a question'
        : `Open the marked blocks — marked for the question · ${count}${spoken}`
    this.chip.setAttribute('aria-label', `marked for the question · ${count}${spoken}`)
  }

  private renderPanel(): void {
    const rows: FloatingPanelRow[] = this.panelBlocks().map((grant) => ({
      id: grant.itemId,
      displayText: grant.automatic
        ? `Frozen screen attached automatically · ${grant.command}`
        : grant.command,
      matchRanges: [],
      ...(grant.automatic ? {} : { actions: [this.dismissButton(grant.itemId)] }),
    }))
    const footer = document.createElement('div')
    footer.className = 'ui-floating-panel__footer'
    const dismissAll = createButton({
      label: 'Dismiss all',
      variant: 'ghost',
      size: 'sm',
      onClick: (event) => {
        event.stopPropagation()
        this.blocks = []
        this.repaintBlocks()
        this.updateChip()
        this.onChange?.(this.blocks)
        this.panel.hide()
      },
    })
    dismissAll.dataset.action = 'dismiss-all-grants'
    footer.appendChild(dismissAll)
    this.panel.show({ rows, selectedIndex: -1, after: [footer] })
  }

  private dismissButton(itemId: string): HTMLButtonElement {
    // The kit's icon button, not a context-menu row class borrowed for a lone ×:
    // the row's identity belongs to ContextMenu, and a glyph is not an icon.
    const button = createIconButton({
      size: 'xs',
      ariaLabel: 'Dismiss this mark',
      icon: () => iconElement(CloseIcon),
      attrs: { 'data-action': 'dismiss-grant', 'data-item-id': itemId },
      onClick: (event) => {
        event.stopPropagation()
        this.blocks = this.blocks.filter((grant) => grant.itemId !== itemId)
        this.repaintBlocks()
        this.updateChip()
        this.onChange?.(this.blocks)
        if (this.blocks.length === 0 && this.automaticBlock === null) this.panel.hide()
        else this.renderPanel()
      },
    })
    button.addEventListener('mousedown', (event) => event.stopPropagation())
    return button
  }

  private reveal(index: number): void {
    const grant = this.panelBlocks()[index]
    if (!grant) return
    grant.blockEl.scrollIntoView?.({ block: 'center', behavior: 'smooth' })
    const marked: HTMLElement[] =
      grant.start !== undefined && grant.count !== undefined
        ? grantRows(grant.blockEl).slice(grant.start, grant.start + grant.count)
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
    if (!this.visible) {
      for (const grant of this.blocks) {
        grant.blockEl.classList.remove('cmd-block-grant-flash')
        for (const row of grantRows(grant.blockEl)) {
          row.classList.remove('cmd-block-grant-flash')
        }
      }
      return
    }
    for (const grant of this.blocks) {
      if (grant.start !== undefined && grant.count !== undefined) {
        // The rows a mark covers are the markable surface's own — a block's
        // `.term-line`, the frozen screen's `.nocx-freeze-frame__row`
        // (nocx-hp8p2.7). Reading one selector here painted nothing at all
        // for a mark made inside the frame.
        const rows = grantRows(grant.blockEl).slice(grant.start, grant.start + grant.count)
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
