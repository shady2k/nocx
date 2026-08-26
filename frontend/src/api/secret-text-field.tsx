import { onCleanup, splitProps } from 'solid-js'
import { findReferences } from '../secret-reference'
import { createSecretPickerField } from '../ui/secret-picker-field'
import { LockIcon } from '../ui/icons'
import { TextField, type TextFieldMark, type TextFieldProps } from '../ui/text-field'
import type { SecretEntry, SecretPickerSource } from '../ui/secret-picker'

export type { SecretEntry } from '../ui/secret-picker'

export type VaultState = 'uninitialized' | 'sealed' | 'unsealed' | 'unknown'

export function secretMarks(
  value: string,
  entries: readonly SecretEntry[],
  vaultState: VaultState,
): TextFieldMark[] {
  return findReferences(value).map((reference) => {
    const entry = entries.find((candidate) => candidate.id === reference.name)
    if (entry !== undefined) {
      return {
        from: reference.from,
        to: reference.to,
        tone: 'secret' as const,
        displayText: entry.name,
        secretHandle: reference.name,
      }
    }
    if (vaultState === 'sealed') {
      return {
        from: reference.from,
        to: reference.to,
        tone: 'secret' as const,
        displayText: 'Vault locked — unlock to view',
        secretHandle: reference.name,
      }
    }
    if (vaultState === 'unsealed') {
      return {
        from: reference.from,
        to: reference.to,
        tone: 'unknown' as const,
        displayText: 'Secret not on this machine',
        secretHandle: reference.name,
      }
    }
    return {
      from: reference.from,
      to: reference.to,
      tone: 'reference' as const,
      secretHandle: reference.name,
    }
  })
}

export interface SecretTextFieldProps extends TextFieldProps {
  source?: SecretPickerSource
  onPickerReady?: (open: (() => void) | undefined) => void
  onSecretReference?: (handle: string, at: { x: number; y: number }, replace: () => void) => void
}

export function SecretTextField(props: SecretTextFieldProps) {
  const [pickerProps, fieldProps] = splitProps(props, [
    'source',
    'onPickerReady',
    'onSecretReference',
  ])
  // eslint-disable-next-line solid/reactivity -- injected dependency, never replaced
  const source = pickerProps.source
  // eslint-disable-next-line solid/reactivity -- injected dependency, never replaced
  const onPickerReady = pickerProps.onPickerReady
  /** This field's input element, by the id the surface gave it — the same
   *  lookup the caret and focus paths already do. */
  const inputEl = (): HTMLInputElement | HTMLTextAreaElement | null =>
    props.id
      ? (document.getElementById(props.id) as HTMLInputElement | HTMLTextAreaElement | null)
      : null

  const picker =
    source === undefined
      ? null
      : createSecretPickerField({
          source,
          // The panel is mounted on the body and would otherwise have no idea
          // where this field is; handing the input back is what puts the
          // panel beside it instead of above the top of the window
          // (nocx-vzdna).
          anchor: inputEl,
          value: () => String(props.value),
          onChange: (next, caret) => {
            props.onInput?.(next)
            // eslint-disable-next-line solid/reactivity -- queued DOM focus callback for this field
            queueMicrotask(() => {
              const input = inputEl()
              input?.focus()
              input?.setSelectionRange(caret, caret)
            })
          },
        })
  onCleanup(() => {
    onPickerReady?.(undefined)
    picker?.destroy()
  })

  const openAt = (from: number, to: number): void => {
    if (picker === null) return
    const input = inputEl()
    const current = String(props.value)
    const next = current.slice(0, from) + '@' + current.slice(to)
    props.onInput?.(next)
    picker.openAt(from + 1)
    queueMicrotask(() => {
      input?.focus()
      input?.setSelectionRange(from + 1, from + 1)
    })
  }
  const open = (): void => {
    const input = inputEl()
    const current = String(props.value)
    const caret = input?.selectionStart ?? current.length
    openAt(caret, caret)
  }

  return (
    <TextField
      {...fieldProps}
      // The lock exists exactly where the picker does. A lock on a field with
      // no vault behind it is a control that silently does nothing, which is
      // worse than not offering it (AGENTS.md: a soft degrade the UI
      // contradicts).
      //
      // The vault's STATE is not one of its conditions: a sealed vault is
      // precisely when this door is needed, so hiding it there would remove
      // the only way to ask. Pressing it asks — SecretPicker's explicit door
      // raises the unlock, or setup, rather than an offer row.
      trailingAction={
        picker !== null
          ? {
              ariaLabel: 'Store in vault',
              title: 'Store in vault',
              onClick: (selection) => picker.openForStore(selection),
              children: <LockIcon />,
            }
          : fieldProps.trailingAction
      }
      onMarkClick={(mark, at) => {
        if (mark.secretHandle !== undefined && props.onSecretReference) {
          // eslint-disable-next-line solid/reactivity -- mark callback runs only on explicit user activation
          props.onSecretReference(mark.secretHandle, at, () => openAt(mark.from, mark.to))
          return
        }
        props.onMarkClick?.(mark, at)
      }}
      onFocus={(event) => {
        fieldProps.onFocus?.(event)
        onPickerReady?.(open)
      }}
      onInput={(value) => {
        const caret = inputEl()?.selectionStart ?? value.length
        props.onInput?.(value)
        picker?.onInput(value, caret)
      }}
      onKeyDown={(event) => {
        if (picker?.onKeyDown(event)) return
        props.onKeyDown?.(event)
      }}
    />
  )
}
