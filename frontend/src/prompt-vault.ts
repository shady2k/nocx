// PromptVaultController — the "secrets in the prompt" flow, composed in
// terminal-content.ts. Owns three seams and nothing else:
//
//   1. The picker ('@' at a word start, the owner's trigger). The '@'
//      lands in the line; the controller captures the trigger position and
//      replaces the trigger word with `{{secret:NAME}}` on pick. The picker
//      is PASSIVE: typing continues into the line and the controller pushes
//      the trigger word's continuation into the picker's filter on every
//      document change (space and no-match close it — see secret-picker.ts).
//      The picker is also the resolution door for a recalled masked
//      command: Enter on such a command opens it TARGETED at the first
//      unresolved chip, and picking replaces exactly that span.
//
//   2. The composition-time hint: secrets.detect over the wire (the ONE
//      detector — the TS port is deleted) after the settle debounce, so
//      detection is one call per pause, never per keystroke. A detected
//      HIGH-confidence key gets a quiet in-line decoration and nothing
//      else — no panel, no field, no button, no focus change, no
//      auto-selection (selection is an editing command, and the next
//      keystroke would replace the selected text, which is a data-loss
//      trap). ⌘S stores the candidate — or the selection, which is how the
//      user corrects a boundary the detector got wrong — through the
//      collision-resolving create, and replaces the literal with
//      `{{secret:NAME}}` where NAME is the name the RESPONSE carried. The
//      after-submit receipt (this round) is where the offer lives now.
//
//   3. The unresolved-redaction spans of a recalled masked row: the host
//      hands them over when recall places such a row in the editor (the
//      spans map through the user's edits), the editor refuses to submit
//      while any remain, and Enter opens resolution on the first chip.
//
// The resolve-at-submit half lives in submit.ts (planSubmit) — the host's
// submit action runs it through the editor's beforeSubmit seam.
import { kindIsQuiet, type SecretKind } from './secret-kind'
import { findReferences } from './secret-reference'
import { SecretPicker } from './ui/secret-picker'
import type { SecretsDetect, VaultClient } from './vault-client'
import type { UnresolvedSpan } from './unresolved-redactions'

/** One finding on the wire: kind plus UTF-16 offsets into the line. */
type SecretFinding = SecretsDetect['findings'][number]

/** The minimal editor surface the controller drives. CommandEditor
 *  satisfies it; tests substitute a fake. */
export interface PromptVaultEditor {
  root: HTMLElement
  getDoc(): string
  getSelection(): { from: number; to: number }
  applyReplacement(from: number, to: number, text: string): void
  /** Paint (or clear) the quiet composition-time decoration over the
   *  credential span. The controller owns WHEN a candidate exists; the
   *  editor owns HOW (the CM6 StateField the host installed). */
  setCandidateDecoration(span: { from: number; to: number } | null): void
  /** Replace the unresolved-redaction spans (recalled masked text) — the
   *  editor renders them as unresolved chips, and the host refuses to
   *  submit while any remain. */
  setUnresolvedSpans(spans: ReadonlyArray<UnresolvedSpan>): void
  /** The first unresolved span still in the document, or null. */
  firstUnresolvedSpan(): UnresolvedSpan | null
}

export interface PromptVaultDeps {
  editor: PromptVaultEditor
  vault: VaultClient
  /** Surface an outcome where the user is looking (a toast). */
  report(level: 'info' | 'success' | 'warning' | 'danger', message: string): void
  /** The picker's setup offer was activated and the machine has no OS key:
   *  the vault layer owns the setup dialog, so this hook raises it. Wired
   *  by the host through PaneManager to vaultController.openSetup. */
  requestSetupDialog?: () => void
  /** "Add a secret…" was activated: the host opens the vault's own create
   *  dialog (Settings → Secrets), which owns the surface from there. A
   *  secret needs a name AND a value, and a floating row over the prompt is
   *  not where a value gets typed. */
  requestCreateSecret?: (name: string) => void
}

/** How long a detected key must settle before the hint appears — a paste
 *  lands as one burst, and the mark must not flash mid-paste. */
const OFFER_SETTLE_MS = 500

/** A name is a reference NAME (ADR-0016): braces are structural in
 *  `{{secret:NAME}}`, so they cannot ride a name. Spaces are legal. */
function sanitizeName(name: string): string {
  return name.replace(/[{}]/g, '').trim()
}

/** The RPC reason codes the composition-time save can meet, in the vault's
 *  own words (REASON_MESSAGES in vault.tsx is the full map; these are the
 *  ones a store-from-the-prompt can hit). */
function storeErrorMessage(err: unknown): string {
  const data = (err as { data?: { reason?: string } } | null)?.data
  if (data?.reason === 'vault-sealed') return 'The vault is locked.'
  if (data?.reason === 'vault-uninitialized') return 'Protection has not been set up yet.'
  return err instanceof Error ? err.message : String(err)
}

/** The vault kind a composition-time save stores: a private-key finding
 *  stores a private key, everything else a password — the same rule the
 *  capture path applies. */
function vaultKindFor(kind: SecretKind): 'password' | 'private-key' {
  return kind === 'private-key' ? 'private-key' : 'password'
}

/** A detected span the controller is currently offering: the finding, the
 *  credential value at the value bounds, and the span to replace on save
 *  (the VALUE span — never the whole finding, which for structural rules
 *  covers `KEY=` or `Bearer ` too). */
interface Candidate {
  finding: SecretFinding
  /** The credential text at [valueStart, valueEnd). */
  value: string
}

export class PromptVaultController {
  private readonly picker: SecretPicker
  /** The position the '@' trigger landed at; the picker's replacement range
   *  starts here. Null while no trigger is live. */
  private triggerPos: number | null = null
  /** The span a recall resolution is replacing: when set, the picker's
   *  insert replaces exactly this span instead of the '@' trigger word. */
  private resolveTarget: { from: number; to: number } | null = null
  /** The finding the quiet decoration is currently showing (or about to
   *  show). */
  private candidate: Candidate | null = null
  private settleTimer: ReturnType<typeof setTimeout> | null = null
  /** The document revision the detection round-trip is checked against:
   *  bumped on every change, sent with the request, and the echo is
   *  compared on the way back — a stale response is dropped, never
   *  adjusted onto a newer document. */
  private revision = 0

  constructor(private readonly deps: PromptVaultDeps) {
    this.picker = new SecretPicker(
      {
        status: () => deps.vault.status(),
        list: () => deps.vault.inventory().then((i) => i.entries),
        requestUnseal: () => deps.vault.inventory().then(() => undefined),
        requestSetup: () => this.setupVault(),
        requestCreate: (name) => {
          deps.requestCreateSecret?.(name)
          return Promise.resolve(undefined)
        },
      },
      { onInsert: (name) => this.insertReference(name) },
    )
  }

  get isPickerOpen(): boolean {
    return this.picker.isOpen
  }

  /** Mount the picker into the editor root (it floats above it). */
  mount(): void {
    this.picker.mount(this.deps.editor.root)
  }

  /** '@' fired at a word start (the editor's onSecretPicker). Capture the
   *  trigger and open the picker. */
  onSecretPicker(triggerPos: number): void {
    this.triggerPos = triggerPos
    void this.picker.open()
  }

  /** Every user-driven document change: clear the candidate (a mark must
   *  never outlive the text it was computed for), restart the settle
   *  debounce, and drive the picker's passive filter. */
  onDocChanged(text: string): void {
    this.revision++
    this.clearCandidate()
    this.scheduleDetection()
    this.updatePickerFilter(text)
  }

  /** The arbiter's turn (after recall, before completion). */
  handleKey(e: KeyboardEvent): boolean {
    return this.picker.handleKey(e)
  }

  /** Close the picker — the mutual-exclusion rule: opening recall closes
   *  every other floating surface. */
  closePicker(): void {
    this.picker.close()
  }

  /** The line is gone (submit, Esc, Ctrl-C): drop every surface and the
   *  session's offer memory. Wired by the host to the editor's
   *  onDocCleared. */
  reset(): void {
    this.picker.close()
    this.clearCandidate()
    this.triggerPos = null
    this.resolveTarget = null
    if (this.settleTimer !== null) {
      clearTimeout(this.settleTimer)
      this.settleTimer = null
    }
  }

  destroy(): void {
    this.reset()
    this.picker.destroy()
  }

  // ── the picker's insert seam ─────────────────────────────────────────────

  private insertReference(name: string): void {
    const safeName = sanitizeName(name)
    if (safeName === '') return
    const reference = `{{secret:${safeName}}}`
    // Resolution mode: the picker was opened ON an unresolved chip (Enter
    // on a recalled masked row). Picking replaces exactly that span — the
    // masked text goes, the reference takes its place, and the chip flips
    // from unresolved to resolved.
    const resolveTarget = this.resolveTarget
    if (resolveTarget !== null) {
      this.resolveTarget = null
      this.deps.editor.applyReplacement(resolveTarget.from, resolveTarget.to, reference)
      return
    }
    const triggerPos = this.triggerPos
    this.triggerPos = null
    const doc = this.deps.editor.getDoc()
    const caret = this.deps.editor.getSelection().from
    if (
      triggerPos === null ||
      triggerPos >= doc.length ||
      doc[triggerPos] !== '@' ||
      caret < triggerPos
    ) {
      // The trigger is gone or the caret moved away: insert at the caret
      // rather than replacing an unrelated character.
      this.deps.editor.applyReplacement(caret, caret, reference)
      return
    }
    this.deps.editor.applyReplacement(triggerPos, caret, reference)
  }

  // ── the picker's passive filter ──────────────────────────────────────────

  private updatePickerFilter(text: string): void {
    if (!this.picker.isOpen) return
    const triggerPos = this.triggerPos
    if (triggerPos === null) return
    const caret = this.deps.editor.getSelection().from
    if (triggerPos >= text.length || text[triggerPos] !== '@' || caret < triggerPos) {
      this.picker.close()
      return
    }
    // The trigger word's continuation IS the filter. Whitespace and no-match
    // close the panel (secret-picker.setFilter).
    this.picker.setFilter(text.slice(triggerPos + 1, caret))
  }

  // ── the composition-time hint (decoration, never interruption) ───────────

  private clearCandidate(): void {
    if (this.candidate !== null) {
      this.candidate = null
      this.deps.editor.setCandidateDecoration(null)
    }
  }

  /** Arm the settle debounce. The timer is a DEBOUNCE — it waits for the
   *  typing to stop — and it must therefore restart on every change and
   *  re-detect when it fires. The wire call happens once per pause, never
   *  per keystroke, which is what makes the RPC cheap enough to be the
   *  single detector. */
  private scheduleDetection(): void {
    if (this.settleTimer !== null) clearTimeout(this.settleTimer)
    this.settleTimer = setTimeout(() => {
      this.settleTimer = null
      void this.detectAndDecorate()
    }, OFFER_SETTLE_MS)
  }

  /** One detection round over the current document. The revision captured
   *  at call time travels with the request; a response computed for an
   *  older document is dropped — never adjusted onto the newer one (the
   *  contract's revision rule). A failed call shows nothing: a hint from a
   *  broken pass is worse than no hint. */
  private async detectAndDecorate(): Promise<void> {
    const rev = this.revision
    const doc = this.deps.editor.getDoc()
    let resp: SecretsDetect
    try {
      resp = await this.deps.vault.detect(doc, rev)
    } catch {
      return
    }
    if (resp.revision !== this.revision) return
    const refs = findReferences(doc)
    // The quiet tier governs: a vendor prefix earns the mark, a shape-based
    // guess earns nothing at composition time (it still shows in the
    // after-submit receipt). A finding inside a reference span is never
    // offered (the backend shares the blind spot — a name that LOOKS like
    // a vendor key is masked inside the reference; reported).
    const current = resp.findings.find(
      (f) => kindIsQuiet(f.kind) && !refs.some((r) => f.start >= r.from && f.end <= r.to),
    )
    if (!current) {
      this.clearCandidate()
      return
    }
    const value = doc.slice(current.valueStart, current.valueEnd)
    if (value === '') return
    this.candidate = { finding: current, value }
    // Decorate the VALUE span — the credential, never the `KEY=` or
    // `Bearer ` around it.
    this.deps.editor.setCandidateDecoration({
      from: current.valueStart,
      to: current.valueEnd,
    })
  }

  /**
   * ⌘S from the editor: with a non-empty selection, store THE SELECTION —
   * that is how the user corrects a boundary the detector got wrong, and
   * it is the reason we do not auto-select. With no selection and a
   * decorated candidate, store the candidate. Returns whether anything
   * was triggered (the editor consumes the chord either way — the
   * browser's Save Page must never fire from the prompt).
   */
  saveCandidate(): boolean {
    const doc = this.deps.editor.getDoc()
    const sel = this.deps.editor.getSelection()
    const hasSelection = sel.from !== sel.to
    if (hasSelection) {
      const value = doc.slice(sel.from, sel.to)
      if (value === '') return false
      // The selection is a correction of the candidate's boundary: the
      // candidate's backend-suggested name is still the right name. With no
      // candidate the name comes from one detect round over the selection
      // itself (see detectAndStoreSelection).
      const candidateName = this.candidate?.finding.suggestedName
      if (candidateName !== undefined && candidateName !== '') {
        void this.storeComposition({ from: sel.from, to: sel.to }, value, candidateName)
      } else {
        void this.detectAndStoreSelection({ from: sel.from, to: sel.to }, value)
      }
      return true
    }
    const candidate = this.candidate
    if (!candidate) return false
    void this.storeComposition(
      { from: candidate.finding.valueStart, to: candidate.finding.valueEnd },
      candidate.value,
      candidate.finding.suggestedName,
    )
    return true
  }

  /** No candidate to borrow a name from: one detect round over exactly the
   *  selected text, so the name is the backend's own (the brief's rule —
   *  the renderer never predicts a name). A selection the detector does
   *  not recognise has no backend answer; the user asserted it is a
   *  secret, so the kind-neutral fallback is used and the vault resolves
   *  any collision and reports the real name. */
  private async detectAndStoreSelection(
    span: { from: number; to: number },
    value: string,
  ): Promise<void> {
    let name = 'api-key'
    try {
      const resp = await this.deps.vault.detect(value, 0)
      const finding = resp.findings[0]
      if (finding?.suggestedName !== undefined && finding.suggestedName !== '') {
        name = finding.suggestedName
      }
    } catch {
      // Detection failed — the fallback name still stores the selection.
    }
    await this.storeComposition(span, value, name)
  }

  /** Store the composition-time candidate (or selection): the
   *  collision-resolving create, the real name from the response, and the
   *  literal replaced by its reference. */
  private async storeComposition(
    span: { from: number; to: number },
    value: string,
    suggestedName: string,
  ): Promise<void> {
    const safeName = sanitizeName(suggestedName)
    if (safeName === '') {
      this.deps.report('danger', 'Enter a name for the secret')
      return
    }
    try {
      const res = await this.deps.vault.createSecret({
        name: safeName,
        kind: vaultKindFor(this.candidate?.finding.kind ?? 'high-entropy'),
        value,
        resolve: true,
      })
      // The span may have shifted while the save was in flight: prefer the
      // recorded span when its text still matches, else the first
      // occurrence. The name comes from the RESPONSE, never the one sent.
      const doc = this.deps.editor.getDoc()
      let at = -1
      if (doc.slice(span.from, span.to) === value) {
        at = span.from
      } else {
        at = doc.indexOf(value)
      }
      if (at === -1) {
        this.deps.report('danger', 'The key is no longer in the line')
        return
      }
      this.deps.editor.applyReplacement(at, at + value.length, `{{secret:${res.name}}}`)
      this.deps.report('success', `Stored "${res.name}" in the vault.`)
    } catch (err) {
      this.deps.report('danger', storeErrorMessage(err))
    } finally {
      this.clearCandidate()
    }
  }

  // ── the recall resolution door ───────────────────────────────────────────

  /** A recalled masked row's redaction spans: the host hands them over when
   *  recall places the row in the editor. The spans map through the user's
   *  edits; the editor refuses to submit while any remain. */
  onRecalledRow(spans: ReadonlyArray<UnresolvedSpan>): void {
    this.deps.editor.setUnresolvedSpans(spans)
  }

  /** Enter on a masked recalled row: report why it cannot run and open the
   *  picker TARGETED at the first unresolved chip — picking a secret
   *  replaces that span with {{secret:NAME}}, which renders as the ordinary
   *  resolved chip. Repeat until none are left, then Enter runs. */
  openResolution(): boolean {
    const first = this.deps.editor.firstUnresolvedSpan()
    if (!first) return false
    this.resolveTarget = { from: first.from, to: first.to }
    this.deps.report(
      'warning',
      'The key was removed from this command when it was stored — type it in at the marker, or pick one from the vault.',
    )
    void this.picker.open('resolve')
    return true
  }

  /** The picker's setup offer: silent setup when the OS key is capable;
   *  otherwise the host's seam raises the setup dialog (the vault layer
   *  owns it). The vault's setup with a passphrase cannot be asked from the
   *  prompt — that would be a modal the passive contract forbids. */
  private async setupVault(): Promise<boolean> {
    const status = await this.deps.vault.status()
    if (status.state !== 'uninitialized') return false
    if (status.osKeyCapable) {
      await this.deps.vault.setup({})
      return false
    }
    if (this.deps.requestSetupDialog) {
      this.deps.requestSetupDialog()
      return true
    }
    throw new Error('vault-setup-requires-passphrase')
  }
}
