/**
 * FileInput — file selection, drawn by the kit.
 *
 * The first version was the bare native control (ADR-0014 / nocx-dcsx), on the
 * platform-first reasoning that ran through the whole kit. It is the one primitive
 * where that trade did not hold up: `input[type=file]` draws its own button and
 * labels it in the *browser's* language, so an English settings form showed a
 * Russian "Выбор файла" next to kit buttons in English. That is not a styling gap
 * the way a native select's arrow is — it is the wrong words, and no CSS reaches
 * them.
 *
 * So the native input stays (it is what opens the picker and what the label's
 * `for` points at) but is visually hidden, and the kit draws the trigger: a
 * Button, and the chosen file's name beside it.
 *
 * The input is hidden with the clip-rect technique rather than `display: none` or
 * `hidden`, both of which take it out of the accessibility tree and out of the tab
 * order — the control would then be reachable only by mouse. It keeps focus, and
 * `file-input.css` draws that focus onto the Button next to it.
 *
 * No `class` prop — identity is always `.ui-file-input` on the wrapper.
 */
import { createSignal, createEffect, on } from 'solid-js'
import { Button } from './button'

export interface FileInputProps {
  accept?: string
  onChange?: (file: File | null) => void
  disabled?: boolean
  ariaLabel?: string
  id?: string
  /** Trigger label. Defaults to "Choose file…". */
  buttonLabel?: string
  /** When this key changes, the selected file is cleared. */
  resetKey?: number
}

export function FileInput(props: FileInputProps) {
  const [fileName, setFileName] = createSignal<string | null>(null)
  let input!: HTMLInputElement

  // Reset the input when resetKey changes (deferred — skip initial mount).
  createEffect(
    on(
      () => props.resetKey,
      () => {
        if (input) input.value = ''
        setFileName(null)
      },
      { defer: true },
    ),
  )

  const onChange = (e: Event) => {
    const target = e.currentTarget as HTMLInputElement
    const file = target.files?.[0] ?? null
    setFileName(file?.name ?? null)
    props.onChange?.(file)
  }

  return (
    <div class="ui-file-input">
      <input
        ref={input}
        type="file"
        class="ui-file-input__native"
        accept={props.accept}
        disabled={props.disabled === true}
        aria-label={props.ariaLabel}
        id={props.id}
        onChange={onChange}
      />
      <Button
        variant="default"
        disabled={props.disabled === true}
        onClick={() => input.click()}
      >
        {props.buttonLabel ?? 'Choose file…'}
      </Button>
      <span class="ui-file-input__name" data-empty={fileName() === null ? 'true' : undefined}>
        {fileName() ?? 'No file selected'}
      </span>
    </div>
  )
}
