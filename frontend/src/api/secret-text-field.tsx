import { onCleanup, splitProps } from 'solid-js'
import { findReferences } from '../secret-reference'
import { createSecretPickerField } from '../ui/secret-picker-field'
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
    return {
      from: reference.from,
      to: reference.to,
      tone: vaultState === 'sealed' ? ('secret' as const) : ('unknown' as const),
      displayText:
        vaultState === 'sealed' ? 'Vault locked — unlock to view' : 'Secret not on this machine',
      secretHandle: reference.name,
    }
  })
}

export interface SecretTextFieldProps extends TextFieldProps {
  source?: SecretPickerSource
  onPickerReady?: (open: () => void) => void
  onSecretReference?: (
    handle: string,
    at: { x: number; y: number },
    replace: () => void,
  ) => void
}

export function SecretTextField(props: SecretTextFieldProps) {
  const [pickerProps, fieldProps] = splitProps(props, [
    'source',
    'onPickerReady',
    'onSecretReference',
  ])
  const source = pickerProps.source
  const onPickerReady = pickerProps.onPickerReady
  const picker =
    source === undefined
      ? null
      : createSecretPickerField({
          source,
          value: () => String(props.value),
          onChange: (next, caret) => {
            props.onInput?.(next)
            queueMicrotask(() => {
              const input = props.id
                ? document.getElementById(props.id) as HTMLInputElement | HTMLTextAreaElement | null
                : null
              input?.focus()
              input?.setSelectionRange(caret, caret)
            })
          },
        })
  onCleanup(() => picker?.destroy())

  const openAt = (from: number, to: number): void => {
    if (picker === null) return
    const input = props.id
      ? document.getElementById(props.id) as HTMLInputElement | HTMLTextAreaElement | null
      : null
    const current = String(props.value)
    const next = current.slice(0, from) + '@' + current.slice(to)
    props.onInput?.(next)
    picker.onInput(next, from + 1)
    queueMicrotask(() => {
      input?.focus()
      input?.setSelectionRange(from + 1, from + 1)
    })
  }
  const open = (): void => {
    const input = props.id
      ? document.getElementById(props.id) as HTMLInputElement | HTMLTextAreaElement | null
      : null
    const current = String(props.value)
    const caret = input?.selectionStart ?? current.length
    openAt(caret, caret)
  }

  return (
    <TextField
      {...fieldProps}
      onMarkClick={(mark, at) => {
        if (mark.secretHandle !== undefined && props.onSecretReference) {
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
        const input = props.id
          ? document.getElementById(props.id) as HTMLInputElement | HTMLTextAreaElement | null
          : null
        const caret = input?.selectionStart ?? value.length
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
