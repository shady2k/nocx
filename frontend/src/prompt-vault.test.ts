// @vitest-environment jsdom
//
// The "secrets in the prompt" flow (prompt-vault.ts) after the receipt
// round: the '@' trigger -> picker -> insert-with-replacement, the
// detection round-trip driving a quiet DECORATION (never a panel), the ⌘S
// save of the candidate or the selection through the collision-resolving
// create, and the recall resolution door (Enter on a masked row opens the
// picker TARGETED at the first unresolved chip). The editor and vault are
// fakes; the seams they cross are real.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { PromptVaultController, type PromptVaultEditor } from './prompt-vault'
import type { VaultClient } from './vault-client'
import type { UnresolvedSpan } from './unresolved-redactions'

/** A fake editor with a real document model: applyReplacement behaves like
 *  the editor's, so the controller's edits are observable, and the two
 *  decoration seams record what the controller painted. */
class FakeEditor implements PromptVaultEditor {
  readonly root = document.createElement('div')
  doc = ''
  caret = 0
  selection: { from: number; to: number } | null = null
  replacements: Array<{ from: number; to: number; text: string }> = []
  candidateDecoration: { from: number; to: number } | null = null
  unresolvedSpans: UnresolvedSpan[] = []

  getDoc(): string {
    return this.doc
  }

  getSelection(): { from: number; to: number } {
    return this.selection ?? { from: this.caret, to: this.caret }
  }

  applyReplacement(from: number, to: number, text: string): void {
    this.doc = this.doc.slice(0, from) + text + this.doc.slice(to)
    this.caret = from + text.length
    this.replacements.push({ from, to, text })
    // Map the unresolved spans through the change the way CM6's StateField
    // does: a span wholly replaced (a resolution) collapses to zero and
    // leaves the list; spans after the change shift by the length delta.
    const delta = text.length - (to - from)
    this.unresolvedSpans = this.unresolvedSpans
      .map((span) => {
        if (span.to <= from) return span
        if (span.from >= to) {
          return { ...span, from: span.from + delta, to: span.to + delta }
        }
        if (span.from >= from && span.to <= to) {
          // Wholly replaced (a resolution): collapse to zero length.
          return { ...span, from, to: from }
        }
        return { ...span, from, to: Math.max(from, span.to + delta) }
      })
      .filter((span) => span.to > span.from)
  }

  setCandidateDecoration(span: { from: number; to: number } | null): void {
    this.candidateDecoration = span
  }

  setUnresolvedSpans(spans: ReadonlyArray<UnresolvedSpan>): void {
    this.unresolvedSpans = [...spans]
  }

  firstUnresolvedSpan(): UnresolvedSpan | null {
    return this.unresolvedSpans.find((s) => s.to > s.from) ?? null
  }
}

const UNSEALED = { state: 'unsealed', osKeyCapable: true, defaultProvider: 'file' } as const

/** The VaultClient seams the controller touches, stubbed directly (the
 *  controller calls the client's METHODS, never the raw dispatcher). */
interface VaultStub {
  status: ReturnType<typeof vi.fn>
  inventory: ReturnType<typeof vi.fn>
  detect: ReturnType<typeof vi.fn>
  createSecret: ReturnType<typeof vi.fn>
  captureSave: ReturnType<typeof vi.fn>
  captureDismiss: ReturnType<typeof vi.fn>
  setup: ReturnType<typeof vi.fn>
}

interface Harness {
  controller: PromptVaultController
  editor: FakeEditor
  vault: VaultStub
  reports: Array<{ level: string; message: string }>
  container: HTMLElement
}

function setup(
  entries: Array<{ id: string; name: string }> = [{ id: 'row_1', name: 'openrouter.ai' }],
): Harness {
  const editor = new FakeEditor()
  const vault: VaultStub = {
    status: vi.fn(() => Promise.resolve({ ...UNSEALED })),
    inventory: vi.fn(() => Promise.resolve({ entries })),
    detect: vi.fn(),
    createSecret: vi.fn(),
    captureSave: vi.fn(),
    captureDismiss: vi.fn(),
    setup: vi.fn(() => Promise.resolve({})),
  }
  const reports: Harness['reports'] = []
  const controller = new PromptVaultController({
    editor,
    vault: vault as unknown as VaultClient,
    report: (level, message) => {
      reports.push({ level, message })
    },
  })
  controller.mount()
  return { controller, editor, vault, reports, container: editor.root }
}

const flush = async (): Promise<void> => {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

/** Type the doc programmatically (one change) and settle the debounce —
 *  fake timers: the detection fires when the 500 ms settle elapses. */
async function typeAndSettle(h: Harness, text: string): Promise<void> {
  h.editor.doc = text
  h.editor.caret = text.length
  h.controller.onDocChanged(text)
  await vi.advanceTimersByTimeAsync(500)
  await flush()
}

/** An OpenAI-shaped key — a high-confidence finding. */
const OPENAI_KEY = 'sk-proj-abcdef1234567890abcdef'

/** A fake detection answer: one openai finding spanning the key. */
function openaiDetect(revision: number, doc: string, key = OPENAI_KEY) {
  const start = doc.indexOf(key)
  const end = start + key.length
  return {
    revision,
    findings: [
      {
        kind: 'openai',
        start,
        end,
        valueStart: start,
        valueEnd: end,
        suggestedName: 'openrouter.ai',
      },
    ],
  }
}

let h0: Harness

beforeEach(() => {
  vi.useFakeTimers()
  h0 = setup()
})

afterEach(() => {
  h0.controller.destroy()
  vi.useRealTimers()
})

describe('PromptVaultController: the @ trigger -> picker', () => {
  it('@ at a word start opens the picker; picking inserts the reference over the trigger word', async () => {
    const h = setup()
    h.editor.doc = 'curl @'
    h.editor.caret = 6
    h.controller.onDocChanged('curl @')
    h.controller.onSecretPicker(5)
    await flush()
    expect(h.editor.root.querySelector('.ui-floating-panel[data-variant="secret"]')).not.toBeNull()
    h.controller.handleKey(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
    expect(h.editor.doc).toBe('curl {{secret:openrouter.ai}}')
  })

  it('typing continues into the line while the picker is up (passive)', async () => {
    const h = setup()
    h.editor.doc = '@'
    h.editor.caret = 1
    h.controller.onDocChanged('@')
    h.controller.onSecretPicker(0)
    await flush()
    h.editor.doc = '@ope'
    h.editor.caret = 4
    h.controller.onDocChanged('@ope')
    expect(h.editor.root.querySelector('.ui-floating-panel[data-variant="secret"]')).not.toBeNull()
  })

  // nocx-vzdna moved the FIELD-mounted panel off `bottom: 100%` and onto a
  // computed viewport rect, because a body mount resolves that rule against
  // the initial containing block and opens the panel above the top of the
  // window. The terminal is the half that was already right and is the half
  // a positioning change breaks silently: this panel is a child of the
  // editor root, which is `position: relative`, so CSS alone places it
  // directly above the prompt and the component must compute nothing.
  it('the terminal panel stays the CSS-placed one, inside the editor root', async () => {
    const h = setup()
    h.editor.doc = '@'
    h.editor.caret = 1
    h.controller.onDocChanged('@')
    h.controller.onSecretPicker(0)
    await flush()
    const panel = h.editor.root.querySelector<HTMLElement>(
      '.ui-floating-panel[data-variant="secret"]',
    )!
    expect(panel.parentElement).toBe(h.editor.root)
    expect(panel.dataset.anchor).toBeUndefined()
    expect(panel.style.top).toBe('')
    expect(panel.style.position).toBe('')
  })
})

describe('PromptVaultController: detection drives a decoration, never a panel', () => {
  it('typing a full OpenAI-shaped key produces NO panel and NO floating element, and the decoration appears over the credential', async () => {
    const h = setup()
    h.vault.detect.mockImplementation((line: string, revision: number) =>
      Promise.resolve(openaiDetect(revision, line)),
    )
    const doc = `curl -H "Authorization: Bearer ${OPENAI_KEY}" https://api`
    await typeAndSettle(h, doc)
    expect(h.editor.root.querySelector('.ui-secret-offer')).toBeNull()
    expect(h.editor.root.querySelector('.ui-floating-panel[data-open="true"]')).toBeNull()
    // The quiet decoration over the credential span — nothing else.
    expect(h.editor.candidateDecoration).toEqual({
      from: doc.indexOf(OPENAI_KEY),
      to: doc.indexOf(OPENAI_KEY) + OPENAI_KEY.length,
    })
  })

  it('a high-entropy-only string produces no composition-time decoration', async () => {
    const h = setup()
    h.vault.detect.mockImplementation((line: string, revision: number) => {
      const start = line.indexOf('g3Hk9fQ2mZx7vB4nR8tW1yC5E6a')
      return Promise.resolve({
        revision,
        findings: [
          {
            kind: 'high-entropy',
            start,
            end: start + 30,
            valueStart: start,
            valueEnd: start + 30,
            suggestedName: 'api-key',
          },
        ],
      })
    })
    await typeAndSettle(h, 'export TOKEN=g3Hk9fQ2mZx7vB4nR8tW1yC5E6a')
    expect(h.editor.candidateDecoration).toBeNull()
    expect(h.editor.root.querySelector('.ui-secret-offer')).toBeNull()
  })

  it('a stale detection response (revision mismatch) is dropped', async () => {
    const h = setup()
    let responseRevision = 1
    h.vault.detect.mockImplementation((line: string) =>
      Promise.resolve(openaiDetect(responseRevision, line)),
    )
    const doc = `echo ${OPENAI_KEY}`
    h.editor.doc = doc
    h.editor.caret = doc.length
    h.controller.onDocChanged(doc)
    responseRevision = 0 // the response will echo an OLD revision
    await vi.advanceTimersByTimeAsync(500)
    await flush()
    expect(h.editor.candidateDecoration).toBeNull()
  })

  it('a failed detect call shows nothing', async () => {
    const h = setup()
    h.vault.detect.mockRejectedValue(new Error('socket dropped'))
    await typeAndSettle(h, `echo ${OPENAI_KEY}`)
    expect(h.editor.candidateDecoration).toBeNull()
    expect(h.editor.root.querySelector('.ui-secret-offer')).toBeNull()
  })

  it('a finding inside a reference span is never decorated', async () => {
    const h = setup()
    h.vault.detect.mockImplementation((line: string, revision: number) => {
      const start = line.indexOf('sk-')
      return Promise.resolve({
        revision,
        findings: [
          {
            kind: 'openai',
            start,
            end: start + 13, // sk-proj-mine
            valueStart: start,
            valueEnd: start + 13,
            suggestedName: 'x',
          },
        ],
      })
    })
    await typeAndSettle(h, 'echo {{secret:sk-proj-mine}}')
    expect(h.editor.candidateDecoration).toBeNull()
  })
})

describe('PromptVaultController: ⌘S saves the candidate or the selection', () => {
  function withDetect(h: Harness, key = OPENAI_KEY): void {
    h.vault.detect.mockImplementation((line: string, revision: number) => {
      const start = line.indexOf(key)
      return Promise.resolve({
        revision,
        findings: [
          {
            kind: 'openai',
            start,
            end: start + key.length,
            valueStart: start,
            valueEnd: start + key.length,
            suggestedName: 'openrouter.ai',
          },
        ],
      })
    })
    // The vault resolves the collision: a DIFFERENT name comes back.
    h.vault.createSecret.mockImplementation((params: { name: string; resolve?: boolean }) => {
      if (params.resolve !== true) throw new Error('composition save must ask for resolution')
      return Promise.resolve({ name: `${params.name}-2` })
    })
  }

  it('⌘S with a decorated candidate and no selection stores it and leaves {{secret:<RESPONSE name>}} in the document', async () => {
    const h = setup()
    withDetect(h)
    const doc = `curl -H "Authorization: Bearer ${OPENAI_KEY}" https://api`
    await typeAndSettle(h, doc)
    expect(h.editor.candidateDecoration).not.toBeNull()
    const saved = h.controller.saveCandidate()
    expect(saved).toBe(true)
    await flush()
    // The name from the RESPONSE (-2), never the one sent.
    expect(h.editor.doc).toBe(
      `curl -H "Authorization: Bearer {{secret:openrouter.ai-2}}" https://api`,
    )
    expect(h.vault.createSecret).toHaveBeenCalledTimes(1)
    expect(h.vault.createSecret).toHaveBeenCalledWith({
      name: 'openrouter.ai',
      kind: 'password',
      value: OPENAI_KEY,
      resolve: true,
    })
    expect(
      h.reports.some((r) => r.level === 'success' && r.message.includes('openrouter.ai-2')),
    ).toBe(true)
    // The decoration is cleared after the save.
    expect(h.editor.candidateDecoration).toBeNull()
  })

  it('⌘S with a selection stores THE SELECTION, not the detector span', async () => {
    const h = setup()
    withDetect(h)
    const doc = `curl -H "Authorization: Bearer ${OPENAI_KEY}" https://api`
    await typeAndSettle(h, doc)
    // The user corrects the boundary: a shorter selection.
    const from = doc.indexOf(OPENAI_KEY)
    h.editor.selection = { from, to: from + 12 }
    const saved = h.controller.saveCandidate()
    expect(saved).toBe(true)
    await flush()
    expect(h.editor.doc).toBe(
      `curl -H "Authorization: Bearer {{secret:openrouter.ai-2}}ef1234567890abcdef" https://api`,
    )
    expect(h.vault.createSecret).toHaveBeenCalledTimes(1)
    expect(h.vault.createSecret).toHaveBeenCalledWith(
      expect.objectContaining({ value: OPENAI_KEY.slice(0, 12), resolve: true }),
    )
  })

  it('⌘S with nothing to save is a no-op', () => {
    const h = setup()
    expect(h.controller.saveCandidate()).toBe(false)
    expect(h.vault.createSecret).not.toHaveBeenCalled()
  })

  it('a sealed vault reports the vault words and keeps the draft', async () => {
    const h = setup()
    withDetect(h)
    const doc = `echo ${OPENAI_KEY}`
    await typeAndSettle(h, doc)
    h.vault.detect.mockImplementation((line: string, revision: number) =>
      Promise.resolve(openaiDetect(revision, line)),
    )
    h.vault.createSecret.mockRejectedValue({ data: { reason: 'vault-sealed' } })
    h.controller.onDocChanged(doc)
    await vi.advanceTimersByTimeAsync(500)
    await flush()
    h.controller.saveCandidate()
    await flush()
    expect(h.editor.doc).toBe(doc) // the draft survives
    expect(
      h.reports.some((r) => r.level === 'danger' && r.message === 'The vault is locked.'),
    ).toBe(true)
  })
})

describe('PromptVaultController: the recall resolution door', () => {
  it('a recalled masked row registers its spans as unresolved', () => {
    const h = setup()
    h.controller.onRecalledRow([
      { from: 10, to: 21, kind: 'openai' },
      { from: 30, to: 40, kind: 'jwt' },
    ])
    expect(h.editor.unresolvedSpans).toEqual([
      { from: 10, to: 21, kind: 'openai' },
      { from: 30, to: 40, kind: 'jwt' },
    ])
  })

  it('openResolution reports why and opens the picker targeted at the first chip; picking replaces exactly that span', async () => {
    const h = setup()
    h.editor.doc = 'curl -H "Authorization: Bearer sk-p...7890" https://api'
    h.controller.onRecalledRow([{ from: 31, to: 42, kind: 'openai' }])
    const opened = h.controller.openResolution()
    expect(opened).toBe(true)
    expect(h.reports.some((r) => r.level === 'warning')).toBe(true)
    await flush()
    // Pick a secret: the picker is in list mode after the inventory load.
    h.controller.handleKey(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
    expect(h.editor.doc).toBe(
      'curl -H "Authorization: Bearer {{secret:openrouter.ai}}" https://api',
    )
    // The span is gone from the unresolved list (zero length after replace).
    expect(h.editor.firstUnresolvedSpan()).toBeNull()
  })

  it('openResolution with nothing unresolved is a no-op', () => {
    const h = setup()
    expect(h.controller.openResolution()).toBe(false)
  })

  it('reset clears the candidate, the picker and the resolve target', async () => {
    const h = setup()
    h.vault.detect.mockImplementation((line: string, revision: number) =>
      Promise.resolve(openaiDetect(revision, line)),
    )
    await typeAndSettle(h, `echo ${OPENAI_KEY}`)
    expect(h.editor.candidateDecoration).not.toBeNull()
    h.controller.reset()
    expect(h.editor.candidateDecoration).toBeNull()
  })
})
