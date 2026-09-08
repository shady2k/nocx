/**
 * Checkbox — boolean toggle, wired to the change event.
 *
 * Justified by callers:
 * - settings.ts: standalone input[type=checkbox] for toggle controls; filter checkbox in label
 * - connections.ts: checkboxField() helper — div.cm-field > label + input[type=checkbox]
 * - export-section.ts: show/hide password checkbox
 */
import { Show } from 'solid-js'

export interface CheckboxProps {
  checked: boolean
  /**
   * Called with the position the person just put the control in.
   *
   * IF THE WRITE CAN FAIL, RETURN ITS PROMISE. `checked` is the caller's state
   * and the control shows that state, never where the finger left it: when the
   * returned promise settles — or immediately, for a handler that answers
   * synchronously — the box is restored to `checked`. A write that succeeded
   * has already moved the state, so restoring writes the same value and
   * nothing moves; a write that failed moved nothing, and the control snaps
   * back instead of contradicting the toast beside it (nocx-845y4).
   *
   * A handler that returns nothing gets the synchronous half of that, which is
   * right for a handler that sets a signal and wrong for one that starts an
   * RPC and forgets it — the latter should return the promise so the control
   * stays where the person put it until the answer comes.
   *
   * THE PROMISE IS IN THE TYPE, and it has to be. A bare `=> void` would also
   * accept a promise-returning handler — TypeScript lets any return satisfy a
   * void return — but `no-misused-promises` reads that signature as "nobody
   * awaits this" and refuses the call site, which is exactly right for a slot
   * that drops what it is given and exactly wrong here. Saying the union is
   * how the kit declares that it does await it.
   *
   * The cost is that a handler returning something ELSE no longer passes, and
   * a Solid signal setter returns the value it was given. Those callers give
   * the arrow a block body, which is honest: they were never returning that
   * value on purpose.
   */
  onChange: (checked: boolean) => void | Promise<void>
  label?: string
  ariaLabel?: string
  disabled?: boolean
  /**
   * Which affordance to draw. Both are the same `input[type=checkbox]` — this
   * only changes the shape, never the semantics or the events.
   *
   * - `checkbox` (default): selection and filtering, where the user is marking
   *   something and the effect is scoped to the view.
   * - `switch`: a setting that takes effect the moment it is flipped. A tick
   *   box reads as "chosen, pending save"; these have nothing to save.
   */
  variant?: 'checkbox' | 'switch'
}

export function Checkbox(props: CheckboxProps) {
  let control: HTMLInputElement | undefined

  // The `checked` binding only re-runs when the caller's state CHANGES, so a
  // write that changed nothing leaves the DOM wherever the drag put it. This
  // is the other half: after the handler has had its say, the control is told
  // what the state actually holds.
  const showState = () => {
    if (control) control.checked = props.checked
  }

  const onChange = (e: Event) => {
    const target = e.currentTarget as HTMLInputElement
    const settled: unknown = props.onChange(target.checked)
    if (settled instanceof Promise) {
      // finally, not then(showState, showState): a handler whose promise
      // rejects is reporting a failure, and swallowing it here would hide the
      // one thing worth knowing. The kit restores the control either way.
      void settled.finally(showState)
      return
    }
    showState()
  }

  return (
    <label class="ui-checkbox" data-variant={props.variant ?? 'checkbox'}>
      <input
        ref={control}
        class="ui-checkbox__control"
        type="checkbox"
        role={props.variant === 'switch' ? 'switch' : undefined}
        checked={props.checked}
        aria-label={props.ariaLabel ?? undefined}
        disabled={props.disabled === true}
        onChange={onChange}
      />
      <Show when={props.label !== undefined}>
        <span>{props.label}</span>
      </Show>
    </label>
  )
}
