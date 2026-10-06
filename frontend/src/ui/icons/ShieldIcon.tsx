import { Show, type Component } from 'solid-js'

/** Sandbox shield: outline is Off; check and question mark state facts. */
const ShieldIcon: Component<{ enforced?: boolean; uncertain?: boolean }> = (props) => (
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6z" />
    <Show when={props.enforced}>
      <path d="m8 12 3 3 5-6" />
    </Show>
    <Show when={props.uncertain}>
      <path d="M10 10a2 2 0 1 1 3 1.7c-.7.4-1 .8-1 1.8" />
      <circle cx="12" cy="17" r=".5" fill="currentColor" stroke="none" />
    </Show>
  </svg>
)

export default ShieldIcon
