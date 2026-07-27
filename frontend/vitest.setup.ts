// vitest setup: polyfill browser APIs that jsdom does not ship.
// jsdom does not provide ResizeObserver, so we supply a minimal stub
// that never fires — enough for unit tests that don't depend on layout.

if (typeof ResizeObserver === 'undefined') {
  ;(globalThis as Record<string, unknown>).ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

// jsdom does not implement HTMLDialogElement.prototype.showModal/close as of
// jsdom 25. Provide these methods so tests using showModal() work.
// The `open` property and `returnValue` already exist on instances via IDL.
if (typeof HTMLDialogElement !== 'undefined' && !HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function () {
    if (this.open) {
      throw new DOMException(
        'Failed to execute "showModal" on HTMLDialogElement: The element already has an "open" attribute, and therefore cannot be opened modally.',
      )
    }
    this.open = true
  }

  HTMLDialogElement.prototype.close = function (returnValue?: string) {
    if (!this.open) return
    this.open = false
    if (returnValue !== undefined) this.returnValue = returnValue
    this.dispatchEvent(new Event('close', { bubbles: false }))
  }
}
