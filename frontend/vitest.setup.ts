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

// jsdom (≤ 25) does not implement Range.getClientRects; CodeMirror 6 calls it
// to measure text geometry (coordsAtPos, measureTextSize). There is no layout
// in jsdom, so an empty rect list is the honest answer — it stops CM6 from
// crashing on the measurement path while never fabricating geometry.
if (typeof Range !== 'undefined' && !Range.prototype.getClientRects) {
  Range.prototype.getClientRects = () => []
}

// Node 22+ defines globalThis.localStorage (experimental, non-functional without
// --localstorage-file). In jsdom environments this shadows the jsdom-provided
// localStorage. Provide a simple in-memory stub so tests that clear/read
// localStorage don't crash. The stub is only needed when jsdom's own
// localStorage is absent (typically because Node's undefined global takes
// precedence in vitest 3 + jsdom 29).
if (typeof localStorage === 'undefined') {
  const _store = new Map<string, string>()
  ;(globalThis as Record<string, unknown>).localStorage = {
    getItem(key: string): string | null {
      const v = _store.get(String(key))
      return v === undefined ? null : v
    },
    setItem(key: string, value: string): void {
      _store.set(String(key), String(value))
    },
    removeItem(key: string): void {
      _store.delete(String(key))
    },
    clear(): void {
      _store.clear()
    },
    get length(): number {
      return _store.size
    },
    key(index: number): string | null {
      const keys = Array.from(_store.keys())
      return keys[index] ?? null
    },
  }
}
if (typeof sessionStorage === 'undefined') {
  const _sessStore = new Map<string, string>()
  ;(globalThis as Record<string, unknown>).sessionStorage = {
    getItem(key: string): string | null {
      const v = _sessStore.get(String(key))
      return v === undefined ? null : v
    },
    setItem(key: string, value: string): void {
      _sessStore.set(String(key), String(value))
    },
    removeItem(key: string): void {
      _sessStore.delete(String(key))
    },
    clear(): void {
      _sessStore.clear()
    },
    get length(): number {
      return _sessStore.size
    },
    key(index: number): string | null {
      const keys = Array.from(_sessStore.keys())
      return keys[index] ?? null
    },
  }
}
