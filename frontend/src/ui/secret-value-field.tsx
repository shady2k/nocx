/**
 * SecretValueField — the one control in this product a secret VALUE is typed
 * into, and the whole of what it exists to guarantee: THE VALUE PASSES
 * THROUGH.
 *
 * IT KEEPS NOTHING. The value lives in a signal for exactly as long as the
 * person is typing it and is cleared the moment the write is accepted — no
 * caller is handed a draft of it, nothing writes it into a form, and no
 * surface that mounts this ever has a signal a credential sits in. That is
 * what lets the API side go on saying "there is no field in this file a value
 * could be typed into" (design §8): this field is not part of any file.
 *
 * `type="password"` because the value is a credential on a screen somebody
 * else may be looking at, and it clears on success rather than showing what
 * was stored: the backend answers nothing back (ADR-0011), and a field still
 * holding the value afterwards would be this component keeping a copy the
 * backend deliberately did not return.
 *
 * A REFUSAL KEEPS WHAT WAS TYPED. It is the exact opposite of the clear, and
 * for one reason: emptying the field when the write was refused costs the
 * person the value they just pasted, which they may not have anywhere else.
 * So `onSubmit` REJECTING is how a caller says "not stored" — a promise that
 * resolves is taken as "it landed", and the field empties.
 *
 * It began inside the environments page, where it was the only way to give a
 * declared secret variable a value. The Auth tab needs the same act on the
 * same store, and a second field with its own clearing rules would have been
 * two vocabularies for the one moment a credential is on screen — so it moved
 * here and both surfaces place it.
 * An external placement receives only the submit action through
 * `onSubmitAction`; it never receives the value or a draft. The default keeps
 * the action beside the field, while a dialog can place its own Button in the
 * footer and invoke that action.
 */
import { createSignal, onMount, Show } from 'solid-js'
import { Button } from './button'
import { TextField } from './text-field'

export interface SecretValueFieldProps {
  /** The input's id, so the surface around it can address the field. */
  id: string
  /** The accessible name. There is no visible label: the row this sits in
   *  already says which variable is being given a value. */
  ariaLabel: string
  placeholder: string
  /** The action's words. 'Store' unless a surface has a better verb. */
  actionLabel?: string
  /** The action's tooltip — the surface names the variable and where it
   *  goes, because only the surface knows either. */
  title: string
  /**
   * The value carried from the act that opened this field. It seeds the
   * internal signal once at construction and is never read again; this does
   * not break IT KEEPS NOTHING because the value was typed one act earlier in
   * the same act, and this field is where it lands rather than a second place
   * it lives.
   */
  initialValue?: string
  /**
   * Refused for a reason OUTSIDE the field — a variable with no name to
   * store it under. The field still takes the text: what is refused is the
   * write, and a person who has already pasted a token should not lose it to
   * a state they can still fix.
   */
  disabled?: boolean
  /** Where the value goes, exactly once. A rejection is a refusal and its
   *  message is shown on the field; resolving means it landed. */
  onSubmit: (value: string) => Promise<unknown> | undefined
  /**
   * Receives the submit action for an external button. This callback carries
   * an action, never the value or a draft, so the field keeps its ownership.
   */
  onSubmitAction?: (action: SecretValueFieldAction) => void
  /** Keep the action beside the field (default) or expose it to its surface. */
  actionPlacement?: 'inline' | 'external'
}

export interface SecretValueFieldAction {
  /** Invoke the field's submit path without receiving its value. */
  invoke: () => void
  /** Whether invoking now would be a no-op. */
  disabled: () => boolean
}

export function SecretValueField(props: SecretValueFieldProps) {
  const [value, setValue] = createSignal(props.initialValue ?? '')
  const [busy, setBusy] = createSignal(false)
  const [refused, setRefused] = createSignal('')
  const isDisabled = (): boolean => value() === '' || busy() || props.disabled === true

  const submit = (): void => {
    const typed = value()
    if (isDisabled()) return
    setBusy(true)
    setRefused('')
    void Promise.resolve(props.onSubmit(typed))
      .then(() => {
        setValue('')
      })
      .catch((err: unknown) => {
        setRefused(err instanceof Error ? err.message : String(err))
      })
      .finally(() => setBusy(false))
  }
  const submitAction: SecretValueFieldAction = {
    invoke: submit,
    disabled: isDisabled,
  }
  // The external surface receives this action, never `typed`: the value remains
  // owned by this component even when its button lives in a dialog footer.
  onMount(() => props.onSubmitAction?.(submitAction))

  return (
    <div class="ui-secret-value-field">
      <TextField
        id={props.id}
        ariaLabel={props.ariaLabel}
        type="password"
        placeholder={props.placeholder}
        value={value()}
        error={refused() !== '' ? refused() : undefined}
        onInput={setValue}
      />
      <Show when={(props.actionPlacement ?? 'inline') === 'inline'}>
        <Button disabled={isDisabled()} title={props.title} onClick={submit}>
          {props.actionLabel ?? 'Store'}
        </Button>
      </Show>
    </div>
  )
}
