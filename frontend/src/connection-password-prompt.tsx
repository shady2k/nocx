/**
 * ConnectionPasswordPrompt — the renderer half of the connection-password
 * ask (the backend's RequestConnectionPassword, the same shape as the
 * vault unlock ask). Reuses the vault prompt SURFACE (the ui-kit Prompt
 * top-sheet) with a different meaning: this prompt asks for a CONNECTION
 * password, never the vault passphrase, and it names which connection and
 * account it is asking about (nocx-s8jn).
 *
 * Three outcomes, decided here and reported back over the wire:
 *   - submit with remember → the backend stores it as a vault secret the
 *     profile references (ADR-0017) so the next open is silent;
 *   - submit without remember → use once, store nothing;
 *   - cancel → the connection fails with the user's cancellation.
 * The backend decides where and whether the password is stored; this
 * component only reports the decision.
 */
import { createSignal, Show } from 'solid-js'
import { t } from './i18n/locale-model'
import { Button, Checkbox, Prompt, Stack, TextField } from './ui'
import type { ProfileClient } from './profiles'
import type { ConnectionsPasswordRequest } from './generated/connections.passwordRequest'

export interface ConnectionPasswordPromptProps {
  open: boolean
  /** The ask as the backend sent it: names the connection and account. */
  ask: ConnectionsPasswordRequest
  client: ProfileClient
  /** Fired when the prompt is fully resolved (submitted or cancelled). */
  onDone: () => void
}

export function ConnectionPasswordPrompt(props: ConnectionPasswordPromptProps) {
  const [password, setPassword] = createSignal('')
  const [remember, setRemember] = createSignal(false)
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal('')

  const title = () =>
    props.ask.connection
      ? `${t('connections.passwordPrompt.forHead', 'Password for ')}${props.ask.connection}`
      : t('connections.passwordPrompt.required', 'Password required')
  // The account the password belongs to: user@host, so the same host with
  // a different user is visibly a different password.
  const account = () => `${props.ask.user}@${props.ask.host}`

  const submit = async () => {
    if (!password()) {
      setError(t('connections.passwordPrompt.enterError', 'Enter the password for this connection'))
      return
    }
    setError('')
    setBusy(true)
    try {
      await props.client.passwordResolved({
        requestId: props.ask.requestId,
        outcome: 'submitted',
        password: password(),
        remember: remember(),
      })
      props.onDone()
    } catch (e) {
      setBusy(false)
      const message = (e as Error).message
      setError(message || t('connections.passwordPrompt.sendFailed', 'Could not send the password'))
    }
  }

  /** Cancelling is the user's reason: the connection fails with it. */
  const cancel = () => {
    props.client
      .passwordResolved({ requestId: props.ask.requestId, outcome: 'cancelled' })
      .catch(() => {})
    props.onDone()
  }

  return (
    <Prompt
      open={props.open}
      onClose={cancel}
      ariaLabel={title()}
      placement="top-sheet"
      title={title()}
      onSubmit={() => {
        if (busy()) return
        void submit()
      }}
      actions={
        <>
          <Button variant="primary" disabled={busy()} onClick={() => void submit()}>
            {t('connections.passwordPrompt.connect', 'Connect')}
          </Button>
          <Button variant="default" disabled={busy()} onClick={cancel}>
            {t('connections.passwordPrompt.cancel', 'Cancel')}
          </Button>
        </>
      }
    >
      <Stack>
        <p class="ui-vault-desc-text">
          {props.ask.reason} {t('connections.passwordPrompt.enterFor', 'Enter the password for')}{' '}
          <strong>{account()}</strong>.
        </p>
        <TextField
          id="connection-password"
          label={t('connections.passwordPrompt.password', 'Password')}
          type="password"
          value={password()}
          onInput={(v) => {
            setPassword(v)
            setError('')
          }}
          error={error()}
          autoFocus
        />
        <Show when={props.ask.connection}>
          <Checkbox
            variant="switch"
            checked={remember()}
            onChange={setRemember}
            label={t(
              'connections.passwordPrompt.remember',
              'Remember this password — the next open will not ask',
            )}
          />
        </Show>
      </Stack>
    </Prompt>
  )
}
