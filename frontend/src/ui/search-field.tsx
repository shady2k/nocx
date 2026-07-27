/**
 * SearchField — settings-search input, matched to the st-search-input CSS class.
 *
 * Justified by callers:
 * - settings.ts: st-search wrapper + st-search-input, type='text', placeholder, aria-label
 * - settings-content.ts: type='search' variant, st-search class on input
 *
 * Callers wrap this in their own container; the component renders only the
 * input itself with the st-search-input class, letting the parent own the
 * st-search wrapper.
 */

export interface SearchFieldProps {
  class?: string
  value: string
  onInput: (value: string) => void
  placeholder?: string
  ariaLabel?: string
  disabled?: boolean
}

export function SearchField(props: SearchFieldProps) {
  const onInput = (e: Event) => {
    const target = e.currentTarget as HTMLInputElement
    props.onInput(target.value)
  }

  return (
    <input
      type="search"
      class={props.class ?? ''}
      value={props.value}
      placeholder={props.placeholder ?? ''}
      aria-label={props.ariaLabel ?? undefined}
      disabled={props.disabled === true}
      onInput={onInput}
    />
  )
}
