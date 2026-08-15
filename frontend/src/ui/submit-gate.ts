/**
 * The submit gate — the kit's one answer to "how a form refuses a submit".
 *
 * `validation.ts` decides *whether* a value is wrong and *when* to show it.
 * This module owns what happens at the moment of refusal, which is a third,
 * separable question: reveal every failing field, focus the first one, and
 * announce the first failing rule's message — and how many need
 * attention when more than one fails. Surfaces used to each write their own
 * `valid() → revealAll() → showToast(firstError)` sequence; the message was
 * lost and the first invalid field sat unfocused. One owner, so a refused
 * submit behaves the same everywhere.
 *
 * It lives in its own module (rather than inside validation.ts) because it is
 * browser-bound: announcing goes through `toast.tsx`, whose component chain
 * touches `window` at import time, and validation.ts must stay importable in
 * a DOM-less test environment.
 */
import { showToast } from './toast'
import type { FormValidation } from './validation'

export interface SubmitGateOptions<K extends string> {
  /**
   * Called with the first failing field BEFORE the gate tries to focus it, so
   * a surface whose fields live on unopened panels (a Tabs editor) can open
   * the panel holding the field. May be async — focus waits for it to settle.
   */
  reveal?: (field: K) => void | Promise<void>
}

/**
 * Returns a function a submit handler calls first: `true` means the form
 * passes and the handler proceeds, `false` means it was refused. On a refusal
 * the gate reveals every failing field, focuses the first one, and announces
 * through the toast region — one message, in the region the whole app already
 * uses — the first failing rule's message, with how many fields need attention
 * when more than one fails. A count alone is not actionable, so the rule's own
 * message always leads.
 *
 * The gate never pretends it focused what it could not. Focus is only
 * attempted on a control that is present and not hidden (`closest('[hidden]')`
 * is empty); afterwards `document.activeElement` must actually be on it. A
 * control that is missing, hidden behind an unopened panel with no `reveal`
 * hook to open it, or otherwise unfocusable produces a message that says so.
 *
 *     const gate = createSubmitGate(v, {
 *       reveal: (field) => openSection(sectionFor(field)),
 *     })
 *     // in the submit handler:
 *     if (!(await gate())) return
 */
export function createSubmitGate<K extends string>(
  validation: FormValidation<K>,
  options: SubmitGateOptions<K> = {},
): () => Promise<boolean> {
  return async () => {
    if (validation.valid()) return true
    validation.revealAll()
    const countText = (n: number) =>
      n === 1 ? '1 field needs attention' : `${n} fields need attention`
    const first = validation.firstErrorField()
    if (first === undefined) {
      showToast({ level: 'warning', message: countText(validation.errorCount()) })
      return false
    }
    await options.reveal?.(first)
    // The reveal hook's contract allows it to be async — "may be async —
    // focus waits for it to settle" — and during an awaited reveal the world
    // is free to change: a lazily loaded panel can populate a field, reactive
    // rules can move under it. The gate must act on the state it can see
    // AFTER the await, never on the snapshot it read before it, so the whole
    // verdict is re-read here — never retained across the await.
    if (validation.valid()) {
      // Decision, written down: the submit is ALLOWED. The gate's contract is
      // a verdict on the current values — "true means the form passes" — and
      // they pass now. The alternative, cleanly abandoning, would make the
      // gate refuse a form whose values pass: a Save that silently does
      // nothing, the dead-click failure this gate exists to prevent. The user
      // asked for the submit, the reveal settled on a valid form, and the
      // submit proceeds.
      return true
    }
    // All three reads happen after the last await, on the same fresh state —
    // `firstError()` is the one source of the message, deciding which rule is
    // first in declaration order, so this module never re-derives the order
    // or reads the rules map itself. The first error may have moved while the
    // reveal settled; if its panel is not the one the hook opened, the focus
    // attempt fails and the announcement says so honestly.
    const message = validation.firstError()
    const count = validation.errorCount()
    // Read fresh, and with no fallback to the pre-await `first`. There is
    // nothing for a fallback to catch: `firstErrorField()` is undefined only
    // when every rule passes, and `valid()` returned false a few lines up. A
    // fallback that cannot fire is worse than none, because it tells the next
    // reader the opposite — that the fresh read is unreliable and the stale
    // value is a reasonable stand-in across the await. It is not, and that is
    // the defect this block exists to fix.
    const target = validation.firstErrorField()
    const controlId = target === undefined ? undefined : validation.controlId(target)
    const control = controlId !== undefined ? document.getElementById(controlId) : null
    const focusable = control !== null && control.closest('[hidden]') === null
    if (focusable) control.focus()
    const focused =
      focusable && (document.activeElement === control || control.contains(document.activeElement))
    // One failing field says only what is wrong: the rule's own message. The
    // count earns its place only when more than one field needs attention —
    // "Port must be between 1 and 65535 — 1 field needs attention" says the
    // same thing twice.
    let announcement: string
    if (message === undefined) {
      announcement = countText(count)
    } else if (count > 1) {
      announcement = `${message} — ${countText(count)}`
    } else {
      announcement = message
    }
    if (!focused) announcement = `${announcement} — could not focus the first field`
    showToast({ level: 'warning', message: announcement })
    return false
  }
}
