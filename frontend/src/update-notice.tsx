import { type Component, type Accessor, Switch, Match, createSignal } from 'solid-js'
import { render } from 'solid-js/web'
import { ApplyUpdate } from '../wailsjs/go/main/WailsApp'
import { Button } from './ui/button'

export type UpdateState =
  | { kind: 'hidden' }
  | { kind: 'available'; version: string; notesUrl: string }
  | { kind: 'downloading' }
  | { kind: 'pending'; version: string }
  | { kind: 'error'; message: string }

export interface UpdateNoticeController {
  showAvailable: (version: string, notesUrl: string) => void
  showDownloading: () => void
  showPendingRestart: (version: string) => void
  showError: (message: string) => void
}

export function mountUpdateNotice(bar: HTMLElement): UpdateNoticeController {
  const host = document.createElement('div')
  bar.append(host)

  const [state, setState] = createSignal<UpdateState>({ kind: 'hidden' })

  const apply = () => {
    void (async () => {
      setState({ kind: 'downloading' })
      try {
        await ApplyUpdate()
        setState({ kind: 'pending', version: '' })
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err)
        setState({ kind: 'error', message: msg })
      }
    })()
  }

  render(() => <UpdateNoticeView state={state} onApply={apply} />, host)

  return {
    showAvailable: (version: string, notesUrl: string) =>
      setState({ kind: 'available', version, notesUrl }),
    showDownloading: () => setState({ kind: 'downloading' }),
    showPendingRestart: (version: string) => setState({ kind: 'pending', version }),
    showError: (message: string) => setState({ kind: 'error', message }),
  }
}

interface UpdateNoticeViewProps {
  state: Accessor<UpdateState>
  onApply: () => void
}

const UpdateNoticeView: Component<UpdateNoticeViewProps> = (props) => {
  const className = () => {
    const k = props.state().kind
    if (k === 'hidden' || k === 'available') return 'update-notice'
    return `update-notice ${k}`
  }

  const hidden = () => props.state().kind === 'hidden'

  return (
    <div class={className()} hidden={hidden()}>
      <Switch fallback={null}>
        <Match when={props.state().kind === 'available'}>
          <span>
            nocx {(props.state() as Extract<UpdateState, { kind: 'available' }>).version} available
          </span>
          {' · '}
          <a
            href={(props.state() as Extract<UpdateState, { kind: 'available' }>).notesUrl}
            target="_blank"
            rel="noopener"
            class="update-notes-link"
          >
            release notes
          </a>{' '}
          <Button class="update-apply-btn" onClick={() => props.onApply()}>
            Update
          </Button>
        </Match>
        <Match when={props.state().kind === 'downloading'}>
          <span>Downloading update…</span>
        </Match>
        <Match when={props.state().kind === 'pending'}>
          <span>
            nocx {(props.state() as Extract<UpdateState, { kind: 'pending' }>).version} installed
            {' — '}restart to apply
          </span>
        </Match>
        <Match when={props.state().kind === 'error'}>
          <span>
            Update failed: {(props.state() as Extract<UpdateState, { kind: 'error' }>).message}
          </span>
        </Match>
      </Switch>
    </div>
  )
}
