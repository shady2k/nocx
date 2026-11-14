// The pane's head: what its connection is doing, in one line, with the one
// action a person may take (design §5.8, nocx-oalo1.5.5).
//
// WHY IT IS A HEAD AND NOT A CARD. The connection is a persistent condition,
// so it is not a toast: a toast fades whether or not the condition has come
// back. It is also not a card in the flow, because a card takes the pane's
// space and the person is looking at the terminal. One line above the
// transcript says the stage, the time in it and — when the backend offers one —
// Reconnect; the fuller timeline is vision.
//
// IT IS THE TYPED `connection` VARIANCE OF THE COMMAND-BLOCK FRAME (design
// §5.8, "status in a head extends command-block-frame with a typed variance,
// never a repaint"): the same kit identity and anatomy as the block heads it
// stands above, with the one line this surface needs. The component owns no
// colours and no padding of its own; the frame's stylesheet does.
//
// THE RENDERER TICKS THE TIMER LOCALLY. stateMs is the time in the state at
// OBSERVATION, never a duration to wait out; a head that refreshed only when
// the backend published would show a frozen clock between observations. The
// tick is presentation of a fact the backend already stated.
//
// THE BUTTON IS THE BACKEND'S OFFER. `reconnect` present means Reconnect is
// shown, absent means nothing is offered; no policy lives here, and a press is
// one call to the backend (`pane.reconnect`) with the epoch of the observation
// that offered it. The count it carries is how many shells pressing would end,
// said before the press rather than after it.

import { Show, createEffect, createRoot, createSignal } from 'solid-js'
import { render } from 'solid-js/web'

import type { PaneConnection } from '../generated/pane.connection'
import { connectionCondition } from '../connection-condition'
import type { ConnectionStore } from './connection-store'
import { createCommandBlockFrame } from '../ui/command-block-frame'
import { Button } from '../ui/button'
import { AlertTriangleIcon, CheckIcon, PlugIcon } from '../ui/icons'
import { Spinner } from '../ui/spinner'
import { t } from '../i18n/locale-model'

export interface ConnectionHeadHandle {
  /** The strip itself, so the pane can place it once its scrollback exists. */
  readonly element: HTMLElement
  dispose(): void
}

type Tone = 'busy' | 'ok' | 'warn' | 'bad' | 'muted'

interface HeadFacts {
  /** `starting` is the absence of an observation, not a connection state: the
   *  head is painted on the first frame but claims nothing until the backend
   *  speaks. */
  state: PaneConnection['transport']['state'] | 'starting'
  text: string
  tone: Tone
  busy: boolean
  offer: { endsShells: number; shellOnly: boolean } | null
  /** The stage word, when the state is one that has stages. It comes from the
   *  observation and nowhere else. */
  stage?: string
}

const STAGE_WORDS: Record<string, string> = {
  resolve: 'resolving the route',
  tcp: 'reaching the host',
  'ssh-version': 'waiting for the SSH answer',
  'key-exchange': 'agreeing encryption keys',
  'sign-in': 'signing in',
  shell: 'opening a shell',
}

const PERSON_WORDS: Record<string, string> = {
  'host-key': 'Waiting for you to trust the host key',
  'helper-consent': 'Waiting for you to allow the nocx helper',
  'vault-unlock': 'Waiting for you to unlock the vault',
  passphrase: 'Waiting for the key\u2019s passphrase',
  password: 'Waiting for the password',
  'second-factor': 'Waiting for the second factor',
  'key-signature': 'Waiting for the key\u2019s signature; it may need a touch',
}

const FAILED_WORDS: Record<string, string> = {
  'host-key-changed': 'the host key changed, and nothing signs in over a new one',
  'auth-rejected': 'the server rejected the sign-in',
  'no-auth-method': 'nothing to sign in with \u2014 this connection has no key, agent or password',
  'plain-only': 'this connection needs plain ssh',
  'profile-gone': 'the saved connection is gone',
}

const ENDED_WORDS: Record<string, string> = {
  released: 'the connection closed',
  silence: 'the connection was closed after going silent',
  tainted: 'the connection was closed after a stuck write',
  'lost-link': 'the connection was lost',
}

/** mm:ss, the clock the design draws for a state that lasts. */
function clock(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const minutes = Math.floor(total / 60)
  const seconds = total % 60
  return `${minutes}:${String(seconds).padStart(2, '0')}`
}

/** The one line. `elapsed` is the observation's stateMs plus the time since it
 *  was observed; a state that is not a wait carries no clock. */
function factsFor(
  observation: PaneConnection | null,
  startedAt: number,
  now: number,
  offerEnds: number | null,
): HeadFacts {
  if (observation === null) {
    // NOTHING TOLD, NOTHING CLAIMED (ADR-ba6xet1b): no stage and no clock
    // until the backend publishes its first observation.
    return {
      state: 'starting',
      text: t('connection.connecting', 'Connecting'),
      tone: 'busy',
      busy: true,
      offer: null,
    }
  }
  const transport = observation.transport
  const observed = Date.parse(observation.observedAt)
  const elapsed = transport.stateMs + (Number.isFinite(observed) ? now - observed : 0)
  const offer =
    observation.reconnect === undefined
      ? null
      : { endsShells: offerEnds ?? 0, shellOnly: observation.shellFailure !== undefined }
  const ends =
    offer !== null && offer.endsShells > 0
      ? ` \u00b7 ${t('connection.endsShells', 'ends {n} shell(s)', { n: offer.endsShells })}`
      : ''
  switch (transport.state) {
    case 'opening':
      return {
        state: 'opening',
        text: `${t('connection.connecting', 'Connecting')} \u00b7 ${clock(elapsed)} \u00b7 ${
          STAGE_WORDS[transport.stage] ?? transport.stage
        }`,
        tone: 'busy',
        busy: true,
        offer,
        stage: transport.stage,
      }
    case 'waiting_person':
      return {
        state: 'waiting_person',
        text: PERSON_WORDS[transport.person] ?? t('connection.waiting', 'Waiting for you'),
        tone: 'busy',
        busy: false,
        offer,
      }
    case 'connected':
      // A HEALTHY TRANSPORT WITH A FAILED SHELL IS NOT "CONNECTED". The
      // refusal is the backend's own message and the offer beside it retries
      // only the shell; saying "Connected" here would hide the pane's one
      // problem behind a green word.
      if (observation.shellFailure !== undefined) {
        return {
          state: 'connected',
          text: `${t('connection.shellFailed', 'Shell failed')} \u00b7 ${
            observation.shellFailure.message
          }`,
          tone: 'bad',
          busy: false,
          offer,
        }
      }
      return {
        state: 'connected',
        text: `${t('connection.connected', 'Connected')} \u00b7 ${(elapsed / 1000).toFixed(1)} s`,
        tone: 'ok',
        busy: false,
        offer,
      }
    case 'stalled': {
      const answers =
        transport.lookAnsweredAt === undefined
          ? ''
          : ` \u00b7 ${t('connection.answersNew', 'the server answers new connections')}`
      return {
        state: 'stalled',
        text: `${t('connection.noAnswer', 'No answer for')} ${clock(elapsed)}${answers}${ends}`,
        tone: 'warn',
        busy: false,
        offer,
      }
    }
    case 'reconnecting':
      return {
        state: 'reconnecting',
        text: `${t('connection.waitingServer', 'Waiting for the server')} \u00b7 ${t(
          'connection.attempt',
          'attempt {n}',
          { n: transport.attempt },
        )} \u00b7 ${clock(elapsed)}${ends}`,
        tone: 'busy',
        busy: true,
        offer,
      }
    case 'failed':
      return {
        state: 'failed',
        text: `${t('connection.failed', 'Failed')} \u00b7 ${
          FAILED_WORDS[transport.cause] ?? transport.cause
        }${ends}`,
        tone: 'bad',
        busy: false,
        offer,
      }
    case 'ended':
      return {
        state: 'ended',
        text: `${t('connection.ended', 'Connection ended')} \u00b7 ${
          ENDED_WORDS[transport.cause] ?? transport.cause
        }${ends}`,
        tone: 'bad',
        busy: false,
        offer,
      }
    case 'plain':
      return {
        state: 'plain',
        text: `plain ssh \u00b7 ${clock(elapsed)}`,
        tone: 'muted',
        busy: false,
        offer,
      }
  }
}

/** The mark beside the sentence, from the state's own tone. The kit's icons
 *  and spinner, never a hand-drawn glyph: one vocabulary for one meaning. */
function HeadMark(props: { facts: HeadFacts }) {
  return (
    <Show
      when={!props.facts.busy}
      fallback={<Spinner size="sm" label={t('connection.busy', 'Connecting')} />}
    >
      <Show
        when={props.facts.tone === 'ok'}
        fallback={
          <Show
            when={props.facts.tone === 'bad' || props.facts.tone === 'warn'}
            fallback={
              <span class="ui-connection-head__icon">
                <PlugIcon />
              </span>
            }
          >
            <span class="ui-connection-head__icon">
              <AlertTriangleIcon />
            </span>
          </Show>
        }
      >
        <span class="ui-connection-head__icon">
          <CheckIcon />
        </span>
      </Show>
    </Show>
  )
}

export interface ConnectionHeadDeps {
  readonly store: ConnectionStore
  /** Called on every press. The press's epoch is read from the store at the
   *  moment of the press, never captured at mount. */
  readonly onReconnect: () => void
}

/**
 * Mount the head at the top of one pane.
 *
 * The frame is built once and rewritten by observation, which is how the
 * terminal's other imperative chrome works (ADR-0012): the pane owns a vanilla
 * subtree and this module owns the small part that changes.
 */
export function mountConnectionHead(
  target: HTMLElement,
  deps: ConnectionHeadDeps,
): ConnectionHeadHandle {
  const frame = createCommandBlockFrame({ variant: 'connection' })
  // THE FRAME'S IDENTITY, NOT A BLOCK'S. `.cmd-header` belongs to command
  // blocks and every surface that asks for "the header" takes the first one;
  // a pane head wearing it would answer for a block it is not. The typed
  // variance is the contract (design §5.8), so the base class is dropped and
  // the connection variant carries its own anatomy.
  frame.header.classList.remove('cmd-header')
  frame.header.classList.add('ui-connection-head')
  frame.header.dataset.testid = 'connection-head'
  frame.metaRow.className = 'ui-connection-head__line'
  frame.status.className = 'ui-connection-head__action'
  frame.header.append(frame.metaRow, frame.status)

  const [observation, setObservation] = createSignal<PaneConnection | null>(
    deps.store.current()?.observation ?? null,
  )
  const [pressed, setPressed] = createSignal(false)
  const startedAt = Date.now()
  const [now, setNow] = createSignal(startedAt)
  const tick = window.setInterval(() => setNow(Date.now()), 1000)

  const facts = (): HeadFacts => {
    const offer = deps.store.offer()
    return factsFor(observation(), startedAt, now(), offer?.endsShells ?? null)
  }

  const unsubscribe = deps.store.onChange(() => {
    setObservation(deps.store.current()?.observation ?? null)
    // A NEW OBSERVATION IS A NEW ANSWER: the press that was in flight has been
    // answered (or the connection moved on), so the button is live again.
    setPressed(false)
  })

  const textHost = document.createElement('span')
  textHost.className = 'ui-connection-head__text'
  textHost.setAttribute('role', 'status')
  textHost.setAttribute('aria-live', 'polite')
  frame.metaRow.appendChild(textHost)

  const markHost = document.createElement('span')
  markHost.className = 'ui-connection-head__mark'
  frame.metaRow.insertBefore(markHost, textHost)

  const actionHost = document.createElement('span')
  frame.status.appendChild(actionHost)

  const press = (): void => {
    if (pressed()) return
    setPressed(true)
    deps.onReconnect()
  }

  const disposeMark = render(() => <HeadMark facts={facts()} />, markHost)
  const disposeText = render(() => <>{facts().text}</>, textHost)
  const disposeButton = render(
    () => (
      <Show when={facts().offer !== null}>
        <Button
          variant="primary"
          size="sm"
          disabled={pressed()}
          title={offerTitle(facts().offer)}
          ariaLabel={offerLabel(facts().offer)}
          onClick={press}
        >
          {offerLabel(facts().offer)}
        </Button>
      </Show>
    ),
    actionHost,
  )

  // The frame declares its state and tone for the stylesheet and for a spec
  // that has to say WHICH part of the promise failed. The effect tracks the
  // signals `facts()` reads — a new observation and the one-second clock — so
  // the attributes follow the same answer the sentence does.
  const applyState = (): void => {
    const current = facts()
    frame.header.dataset.state = current.state
    frame.header.dataset.tone = current.tone
    frame.header.dataset.condition = connectionCondition(observation())
    if (current.stage === undefined) delete frame.header.dataset.stage
    else frame.header.dataset.stage = current.stage
    if (observation()?.shellFailure !== undefined) {
      frame.header.dataset.shellFailure = 'true'
    } else {
      delete frame.header.dataset.shellFailure
    }
  }
  const disposeState = createRoot((dispose) => {
    createEffect(applyState)
    return dispose
  })

  target.insertBefore(frame.header, target.firstChild)

  return {
    element: frame.header,
    dispose(): void {
      window.clearInterval(tick)
      unsubscribe()
      disposeState()
      disposeMark()
      disposeText()
      disposeButton()
      frame.header.remove()
    },
  }
}

function offerLabel(offer: HeadFacts['offer']): string {
  if (offer?.shellOnly === true) return t('connection.retryShell', 'Retry shell')
  return t('connection.reconnect', 'Reconnect')
}

function offerTitle(offer: HeadFacts['offer']): string {
  if (offer?.shellOnly === true) {
    return t('connection.retryShellTitle', 'Open a new shell on this connection')
  }
  if (offer === null || offer.endsShells === 0) {
    return t('connection.reconnectTitle', 'Reconnect this connection')
  }
  return t('connection.reconnectEnds', 'Reconnect ends {n} shell(s) on this connection', {
    n: offer.endsShells,
  })
}
