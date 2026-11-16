// Backend inventory uncertainty is not an SSH connection state. This offer
// retries the saved pane's inventory and claim, never a connection replacement.
import { Show, createSignal } from 'solid-js'
import { render } from 'solid-js/web'

import { Button } from './ui/button'
import { Spinner } from './ui/spinner'
import { StatusCard } from './ui/status-card'
import { t } from './i18n/locale-model'

export interface SessionInventoryOfferHandle {
  set(message: string | null): void
  dispose(): void
}

export function mountSessionInventoryOffer(
  target: HTMLElement,
  message: string,
  onRetry: () => void,
): SessionInventoryOfferHandle {
  const host = document.createElement('div')
  host.className = 'nocx-attach-offer'
  target.appendChild(host)
  const [current, setCurrent] = createSignal<string | null>(message)
  const dispose = render(
    () => (
      <StatusCard
        tone="warning"
        title={
          current() === null
            ? t('reconnect.restoring', 'Restoring this session…')
            : t('reconnect.notAttached', 'This session is not attached')
        }
        description={
          current() ??
          'Input is paused while the existing session is being attached. No new shell is opened.'
        }
        action={
          <Show
            when={current() !== null}
            fallback={<Spinner label={t('reconnect.attaching', 'Attaching')} size="sm" />}
          >
            <Button variant="primary" onClick={onRetry}>
              {t('reconnect.retryAttach', 'Retry attachment')}
            </Button>
          </Show>
        }
      />
    ),
    host,
  )
  return {
    set: setCurrent,
    dispose() {
      dispose()
      host.remove()
    },
  }
}
