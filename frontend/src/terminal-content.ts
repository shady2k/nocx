// ═══════════════════════════════════════════════════════════════════════════
// TerminalContent — all terminal machinery behind the TabContent seam.
// Extracted from Tab so the chrome layer never touches a session or renderer.
// ═══════════════════════════════════════════════════════════════════════════

import { XtermRenderer } from './renderers/xterm'
import type { TerminalRenderer, MarkerAdapter } from './renderers/types'
import { LifecycleClient } from './lifecycle/client'
import {
  LifecycleKernel,
  shouldShowEditor,
  freezeBlock as kernelFreezeBlock,
} from './lifecycle/state'
import { LifecycleProjections } from './lifecycle/projections'
import { CommandEditor } from './editor'
import { shellExtensions } from './shell-highlight'
import { RecallOverlay, queryLedgerHistory, withSessionText } from './recall'
import { CompletionController } from './suggest/controller'
import { createShellProviders } from './suggest/providers'
import { CompletionDropdown } from './ui/completion-dropdown'
import type { FsComplete } from './generated/fs.complete'
import type { ShellComplete } from './generated/shell.complete'
import { ShellInputTarget, createRegistry, type InputTargetRegistry } from './input-target'
import { AgentInputTarget } from './agent-ask'
import { AgentClient } from './agent'
import {
  TargetIndicator,
  chipFromSelection,
  chipFingerprint,
  type ReferenceChip,
} from './ask-entry'
import {
  submitCommand,
  planSubmit,
  planSubmitSync,
  isSubmitFailure,
  type SubmitPlan,
} from './submit'
import { secretChipExtension } from './secret-chip'
import { secretCandidateExtension } from './secret-candidate'
import { unresolvedRedactionField } from './unresolved-redactions'
import { PromptVaultController } from './prompt-vault'
import { VaultClient } from './vault-client'
import { showToast } from './ui/toast'
import type { SessionIntegrationChanged } from './generated/session.integrationChanged'
import {
  IntegrationSilenceStore,
  integrationMessage,
  isDegraded,
  safeSilenceStorage,
  subscribeIntegrationChanged,
} from './integration/status'
import { mountIntegrationNotice } from './integration/notice'
import { BlockReceipt } from './ui/block-receipt'
import type { HistoryRecord } from './generated/history.record'
import { renderRecordedCommand } from './scrollback/blocks'
import { KIND_LABELS } from './secret-kind'
import { NATIVE_RESTORE } from './native-mode'
import { isInteractiveTransition, extractDestination } from './ssh-transition'
import { shouldCopy, type ClipboardAccess, type ClipboardGate } from './clipboard'
import type { ClipboardBanner } from './banner'
import { ScrollbackController } from './scrollback/controller'
import type { BlockRecord } from './scrollback/blocks'
import { CommandLedger } from './command-ledger'
import { recordCommand, queryHistory } from './history-client'
import { log, logDecision, isDecisionTracing } from './log'
import type { WSClient, SessionHandle } from './ipc'
import { showConfirm } from './ui/dialog'
import { hasOpenOverlays } from './ui/overlay/stack'
import {
  BaseTabContent,
  type TabHost,
  type ContentViewport,
  type ActiveOrigin,
} from './tab-content'
import { type ProfileClient } from './profiles'
import { RpcError } from './dispatcher'
import { secretReference } from './secret-reference'
import { LOCAL_TARGET_ID } from './ports-client'
import { DomainEnvironmentProjection } from './lifecycle/domain-environment'
import {
  deriveActions,
  fallsToConventionalGrid,
  shellStateFromLifecycle,
  type ShellState,
  type InputPresentation,
  type DesiredMode,
} from './capability'

// How long the grid must hold still before the PTY is told about it.
const RESIZE_SETTLE_MS = 80

/**
 * How long output is treated as the shell's answer to a resize rather than as
 * unread activity.
 *
 * Generous on purpose. Getting it wrong in one direction lights an indicator
 * that lies about a tab; getting it wrong in the other costs one missed
 * indicator on a tab the user resized a moment ago and is therefore watching.
 * Those are not symmetric.
 */
const RESIZE_ECHO_MS = 400

/**
 * Whether a settle call failed because the backend no longer holds the
 * capture — it was destroyed by the tab closing, the vault sealing, the
 * transport dropping, or the record that carried it failing.
 *
 * The distinction matters at the surface: this one can never succeed, so
 * the row goes and the receipt says the offer is gone. Every other failure
 * is worth another press.
 */
function isCaptureGone(err: unknown): boolean {
  return err instanceof RpcError && (err.code === -32010 || err.code === -32011)
}

/** Host key evidence from a failed open, mirroring the connections.test
 *  hostKey shape. The renderer echoes host+key back to
 *  connections.trustHostKey to accept. */
export interface HostKeyErrorEvidence {
  host: string
  knownHostsHost: string
  algorithm: string
  fingerprint: string
  storedFingerprint?: string
  key: string
  changed: boolean
  profileId?: string
}

function hostKeyEvidenceFromOpenError(
  err: unknown,
  profileId?: string,
): HostKeyErrorEvidence | null {
  if (!(err instanceof RpcError)) return null
  const data: unknown = err.data
  if (
    !data ||
    typeof data !== 'object' ||
    !('host' in data) ||
    typeof data.host !== 'string' ||
    !('knownHostsHost' in data) ||
    typeof data.knownHostsHost !== 'string' ||
    !('changed' in data) ||
    typeof data.changed !== 'boolean' ||
    !('algorithm' in data) ||
    typeof data.algorithm !== 'string' ||
    !('fingerprint' in data) ||
    typeof data.fingerprint !== 'string' ||
    !('key' in data) ||
    typeof data.key !== 'string'
  ) {
    return null
  }
  return {
    host: data.host,
    knownHostsHost: data.knownHostsHost,
    changed: data.changed,
    algorithm: data.algorithm,
    fingerprint: data.fingerprint,
    storedFingerprint:
      'storedFingerprint' in data && typeof data.storedFingerprint === 'string'
        ? data.storedFingerprint
        : undefined,
    key: data.key,
    profileId,
  }
}

/**
 * Whether `el` is somewhere the user types on purpose.
 *
 * Used to keep the terminal's document-level key rescue off other people's
 * fields. `isContentEditable` is checked too: a rich-text surface is a text
 * entry even though it is neither an input nor a textarea.
 */
function isTextEntry(el: Element | null): boolean {
  if (!(el instanceof HTMLElement)) return false
  if (el.isContentEditable) return true
  const tag = el.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT'
}

/** The host callbacks a tab may hand a TerminalContent. Named rather than
 *  positional: they are all optional functions, so any misalignment between
 *  them type-checks cleanly and fails only in front of a user. */
export interface TerminalContentHooks {
  /** Pushes the strip's optional second line — the tab's location, or ''
   *  when the title already says it. Only TerminalContent holds both halves
   *  of that question. */
  onSubtitleChange?: (subtitle: string) => void
  /** The session is an alias (not a saved profile) and can be adopted as a
   *  nocx connection. True = adoptable, false = not. */
  onAdoptabilityChange?: (adoptable: boolean) => void
  /** The environment degraded or became uncertain — integration declined at
   *  open, or markers stopped on an integrated session (nested ssh, docker
   *  exec). Tab chrome renders at most this small warning mark
   *  (nocx-4t37.2); the capability statement itself lives in the rail. */
  onWarningChange?: (warning: boolean, label?: string) => void
  /** The pane entered or left an environment, so the ports panel's target
   *  changed without the active tab changing (nocx-695k.3). */
  onPortsTargetChange?: () => void
  /** The activeOrigin() answer changed without the active tab changing:
   *  a verified OSC 7 cwd arrived, the pane entered or left an
   *  environment, or the session died — each changes the machine the
   *  tab speaks for, and origin-following surfaces (the Files panel's
   *  reveal) must hear about it. Named onActiveOriginChange, not
   *  onCwdChange: OSC 7 is one cause, and a cwd-only hook would
   *  silently miss the other three. */
  onActiveOriginChange?: () => void
  /** An SSH connection failed because the vault is sealed. */
  onVaultSealed?: () => void
  /** An SSH connection failed because the host key is unknown or changed.
   *  The promise resolves true only after the user explicitly trusted the
   *  backend-issued known_hosts identity; mount then retries the same open.
   *  Abort closes a pending decision when the tab is closed. */
  onHostKeyError?: (evidence: HostKeyErrorEvidence, signal: AbortSignal) => Promise<boolean>
  /** The reference picker's setup offer needs the setup dialog (no OS key):
   *  the vault layer owns it — wired by main.tsx to
   *  vaultController.openSetup. */
  onSetupVault?: () => void
  /** The reference picker's "Add a secret…" row: open the vault's own
   *  create dialog — wired by main.tsx to the Settings tab's Secrets page. */
  onCreateSecret?: (name: string) => void
  /** A question was refused because no endpoint is configured: open the
   *  endpoint editor so the refusal comes with its repair — wired by
   *  main.tsx to the Settings tab's Endpoints page. */
  onCreateEndpoint?: () => void
}

// No placeholder title — see the descriptor in tabs.ts for why. A tab with no
// name yet shows nothing rather than a word that is never the answer.
const FALLBACK_TITLE = ''

/**
 * Names a tab after its directory, the way every other terminal does.
 * Keeps the tail — the CSS ellipsis cuts from the right.
 */
function directoryLabel(cwd: string): string {
  const path = cwd.trim().replace(/\/+$/, '')
  if (!path) return FALLBACK_TITLE
  const parts = path.split('/').filter(Boolean)
  if (path === '~' || parts.length === 0) return path || FALLBACK_TITLE
  return parts.slice(-2).join('/')
}

/**
 * Tooltip for a cwd. When the value comes from session open (no OSC 7 yet)
 * the tooltip surfaces that fact (AD-5 fallback visibility).
 */
function cwdTooltip(cwd: string, fromOSC7: boolean): string {
  if (!cwd) return ''
  return fromOSC7 ? cwd : `${cwd} (initial cwd)`
}

/** The key as a person would write it (`ctrl+shift+P`), for the arbiter
 *  trace. Built only when decision tracing is on — see the arbiter. */
function keyLabel(e: KeyboardEvent): string {
  const mods = [e.ctrlKey && 'ctrl', e.metaKey && 'meta', e.altKey && 'alt', e.shiftKey && 'shift']
    .filter((m): m is string => Boolean(m))
    .concat(e.key)
    .join('+')
  return mods
}

/**
 * TerminalContent owns the renderer, session, editor, scrollback, command
 * ledger, lifecycle kernel, and PTY resize policy. It receives geometry
 * through viewportChanged() — it NEVER interprets container geometry itself.
 */
export class TerminalContent extends BaseTabContent {
  private renderer: TerminalRenderer | null = null
  private session: SessionHandle | null = null
  private editor: CommandEditor | null = null
  private shellTarget: ShellInputTarget | null = null
  /** The input-target registry (ADR-0004 §3): a submitted document routes
   *  through active().submit — never a branch on "which mode am I in".
   *  Shell is the default; the person switches it explicitly through the
   *  caret indicator — click, or the ⌘/Ctrl+Enter chord, which flips the
   *  target and sends nothing (nocx-4wtlh). Registration and routing ARE
   *  this slice. */
  private inputTargets: InputTargetRegistry | null = null
  private agentTarget: AgentInputTarget | null = null
  private scrollback: ScrollbackController | null = null
  private ledger: CommandLedger | null = null
  /** The vault RPC client, built over this tab's WS client (the shared
   *  dispatcher — the sealed-access seam it carries is already installed at
   *  the app root). */
  private vault: VaultClient | null = null
  /** The after-submit capture receipts, keyed by the frozen block each one
   *  is mounted in. Several can be open at once: an offer lives until it is
   *  answered, and deciding about a key is rarely the next thing anyone
   *  does — you run one more command first, and under the old
   *  one-at-a-time rule that lost the offer for good.
   *
   *  `receipt` is the newest of them: the one ⌘S acts on and the one the
   *  focus-bounce yields to. */
  private readonly receipts = new Map<HTMLElement, BlockReceipt>()
  private receipt: BlockReceipt | null = null
  private receiptBlockEl: HTMLElement | null = null
  /** Capture id → the block its receipt is mounted in, so a hover
   *  emphasises the chip in the RIGHT block when several are open. */
  private readonly receiptChipBlocks = new Map<string, HTMLElement>()
  /** Ledger record id → the block record captured at onComplete time (the
   *  same object freezeBlock mutates in place). The ack is an async round
   *  trip, so by the time it resolves the block is frozen — but the user
   *  may have submitted again or cleared the scrollback; the identity is
   *  checked against the live DOM, never looked up as "the most recent
   *  block". */
  private readonly pendingReceiptBlocks = new Map<number, BlockRecord>()
  /** Capture id → the redaction span of the chip in the block's command
   *  line, for the receipt's hover emphasis (chips carry the span; rows
   *  carry the capture id). */
  private readonly receiptChipSpans = new Map<string, { start: number; end: number }>()
  /** The prompt's vault surfaces: the '@' picker, the composition-time
   *  candidate, and the resolve-at-submit wiring. */
  private promptVault: PromptVaultController | null = null
  private completion: CompletionController | null = null
  private recall: RecallOverlay | null = null
  /** The reference chips in the input line (nocx-4wtlh): the frozen
   *  regions a question carries. A selection raises one; a question sent
   *  to Ask consumes them all; a cleared scrollback takes their blocks. The
   *  chips ARE the ask's payload — never re-derived from DOM selection at
   *  submit time (AD-8: selection is copy; the chip is the record). */
  private referenceChips: ReferenceChip[] = []
  /** Monotonic chip id source — ids are for dismissal and dedupe, never
   *  for anything the backend sees. */
  private _chipSeq = 0
  /** The caret indicator (ADR-0004 §3's UI chip): renders the active
   *  input target beside the caret and is the person's one explicit
   *  switch. Its label is pushed from the registry's change
   *  notification — nothing else may repaint it. */
  private indicator: TargetIndicator | null = null
  /** The document selectionchange listener: a selection inside a finished
   *  block's output raises a reference chip. Removed on dispose. */
  private readonly onSelectionChange = (): void => this.raiseChipFromSelection()
  private lifecycle = new LifecycleKernel()
  private _lifecycleUnsub: (() => void) | null = null
  private _lifecycleChangeUnsub: (() => void) | null = null
  /** The pending restoration episode (ADR-0024 decision 8): the fence the
   *  shell must write to the pty and the generation to acknowledge, captured
   *  from the lost fact. Non-null from the moment the channel is declared
   *  dead until the acknowledgement lands — the span in which this tab is
   *  neither an authenticated terminal nor advertised as a usable
   *  conventional one. */
  private _recovery: { fence: string; generation: string } | null = null
  /** True while the acknowledgement is in flight: the one-shot fence can
   *  be sighted more than once before the await resolves, and only the
   *  first sighting may claim the episode. */
  private _recoveryAcking = false
  /** The establishment generation whose acknowledgement is in flight, and the
   *  one whose acknowledgement the backend accepted. Replays are intentionally
   *  idempotent — the same projection may arrive from a live transition and
   *  again from the post-open or post-reattach replay — so a generation is
   *  claimed once while its ack is outstanding and permanently once it lands.
   *
   *  The two are kept apart because a FAILED ack must not count as one. An
   *  ack in flight when the socket drops is rejected by the dispatcher
   *  (rejectAllPending), and the backend never saw it: its pending ACCEPT is
   *  still unflushed, and the reattach replay carries that same generation
   *  because only a fresh shell hello mints a new one. Collapsing both states
   *  into "acknowledged" made the renderer suppress the one retry that could
   *  have completed the handshake, leaving the tab conventional until the
   *  accept expired. */
  private _establishmentAckInFlight: string | null = null
  private _establishmentAcked: string | null = null
  /** The disposable projections (ADR-0024 §5–§7, bead nocx-u7uh.7): the
   *  ledger, history and the block model, driven by this kernel. */
  private _projections: LifecycleProjections | null = null
  private _markers = new Map<number, MarkerAdapter>()
  private _globalKeydown: ((e: KeyboardEvent) => void) | null = null
  private _cwd = '~'
  /** True only when _cwd came from a verified OSC 7 report (AD-5): the one
   *  cwd a composition layer may hand to files.open as rootPath (D2). A
   *  session-open cwd is the provider's fallback question, not a claim.
   *
   *  `_cwd`, `_cwdVerified`, `_host` and `_user` are the VIEW of the
   *  domain-environment projection (lifecycle/domain-environment.ts): they
   *  always carry the ACTIVE domain's values, blanked over a lane gap —
   *  never ambient state, and never a suspended parent's values (ADR-0024
   *  §6, protocol §9). `_applyEnvironmentView` copies the projection's
   *  current view into these fields on every environment change. */
  private _cwdVerified = false
  /** True once the session's exit was observed — the tab is closing, and an
   *  origin that names this session would name a machine that is gone. */
  private _sessionExited = false
  private _host = ''
  /** The ssh user of `_host` ('' for local shells) — the location line's
   *  `user@host`, from the same projection view. */
  private _user = ''
  /** The domain-scoped environment projection (bead nocx-u7uh.11): cwd,
   *  host, the tab title and the completion scope follow the ACTIVE domain.
   *  Created at session open (it needs the session facts to seed the lane
   *  tier), detached at dispose. */
  private env: DomainEnvironmentProjection | null = null
  private _bufferType: 'normal' | 'alternate' = 'normal'
  private nativeMode = false
  private _disposed = false
  private mountAbortController: AbortController | null = null
  private resizeTimer: number | undefined
  /** Timestamp until which incoming data is the echo of a resize we sent. */
  private echoUntil = 0
  private host: TabHost | null = null

  // ── Capability rail (nocx-mlm7) ────────────────────────────────────
  /** The resolved destination mode from the open ack (raw|script|relay):
   *  the connection-scope default the capability control starts from. raw
   *  refuses every rewrite and remote write; relay is consent-gated. */
  private _policy: DesiredMode = 'script'
  /** The session's integration status, as the backend keeps revising it
   *  (nocx-dvql). It replaced the open ack's one-shot shellIntegrationReason,
   *  which could not report the two failures that matter most because both
   *  arrive after the ack: a handshake that expires ten seconds in, and a
   *  channel lost mid-session. null means the session never asked for
   *  integration and there is nothing to say about it. */
  private _integration: SessionIntegrationChanged | null = null
  /** The subscription to that status, dropped on dispose. */
  private _integrationUnsub: (() => void) | null = null
  /** The disposer for the mounted degraded-session card, when one is up. */
  private _noticeDispose: (() => void) | null = null
  /** The pane the card mounts over. */
  private _paneTarget: HTMLElement | null = null
  /** Which shells this machine has been told to stop raising cards for.
   *  Renderer presentation state — "has this person answered for this
   *  shell" — so it lives in localStorage rather than behind an RPC, and it
   *  survives a restart, which the clipboard banner's run-scoped suppression
   *  does not. Nothing else is remembered: a card the user merely closed is
   *  raised again by the next session (nocx-wfxz). */
  private readonly _silencedShells = new IntegrationSilenceStore(safeSilenceStorage())
  /** The reasons THIS tab has already raised a card for and the user has
   *  closed. In memory and per tab, which is the scope the persistent record
   *  deliberately does not cover: the next session asks again, but a status
   *  republished on this session — a reconnect re-announcing what it already
   *  said — must not push a card the user just closed back up. */
  private readonly _closedReasons = new Set<string>()
  /** The observed shell state (one of three independent axes). */
  private _shellState: ShellState = 'unsupported'
  /** The current input presentation. */
  private _presentation: InputPresentation = 'terminal'
  /** Whether an integration attempt failed. */
  private _integrationFailed = false
  /** Per-destination consent for in-band integration (nocx-atyf.3).
   *  Session-scoped: a hand-typed ssh to a host the user consented to
   *  integrates silently for the rest of this tab. */
  /** The environment degraded or became uncertain — integration declined at
   *  open, or markers stopped on an integrated session the user did not
   *  latch native. Tab chrome renders at most this mark. */
  private _degraded = false
  /** What the mark currently says, so a change of reason repaints it. */
  private _degradedLabel = ''

  // ── Title composition ────────────────────────────────────────────────
  // Title = programTitle || cwdTitle (no placeholder — nocx-83a)
  // Computed here so the host receives the final string.
  private programTitle = ''
  private cwdTitle = ''

  // Grid dimensions computed by the renderer from the last authoritative
  // viewport. Owned here so PTY resize policy lives with the content.
  cols = 0
  rows = 0

  // _readyPromise resolves true when the renderer mounts and the PTY session
  // opens; resolves false when mount() throws. Never rejects.
  private readonly _readyPromise: Promise<boolean>
  private _readyResolve!: (value: boolean) => void

  constructor(
    private readonly client: WSClient,
    private readonly clipboard: ClipboardAccess,
    private readonly gate: ClipboardGate,
    private readonly banner: ClipboardBanner,
    /** The profile/alias source for the completion host provider and the
     *  connection-offer flows. Null when unavailable (tests, raw-mode-only
     *  contexts). */
    private readonly profileClient: ProfileClient | null,
    private readonly onTooltipChange: (tooltip: string) => void,
    private readonly sshOpts?: {
      profileId: string
      host: string
      user?: string
      port?: number
    },
    /** The optional host callbacks, NAMED. They used to be four trailing
     *  positional parameters, and a caller that skipped the middle two put
     *  onSetupVault into the onAdoptabilityChange slot — so on a local tab
     *  the picker's "set up the vault" row answered Enter by calling
     *  nothing, and nothing could have caught it: every one of them is an
     *  optional function, so every misalignment type-checks. */
    private readonly hooks: TerminalContentHooks = {},
  ) {
    super()
    this._readyPromise = new Promise<boolean>((resolve) => {
      this._readyResolve = resolve
    })
  }

  /**
   * Resolves true when the renderer mounts and the PTY session opens;
   * resolves false when mount() throws. Never rejects. The initial-tab
   * health signal reads this — NOT a generic "first tab mounted" signal.
   */
  get ready(): Promise<boolean> {
    return this._readyPromise
  }

  /** Whether the pane walked into a child domain that names a remote host
   *  — the one state in which this tab cannot speak for the machine in
   *  front of it (nocx-695k.3, regressed by 6cba76fd). The session's own
   *  domain is the stack's ROOT (seeded from the lane tier at
   *  establishment, ADR-0024 §6); a child above it is a place we entered
   *  by hand, and remote discovery needs a MANAGED connection we own — a
   *  hand-typed `ssh` is a child process of the shell. A child that names
   *  no host (a local child — docker exec, su) is still on the machine
   *  the root speaks for, so only a child naming a remote host voids the
   *  scope. Reads the kernel's domain stack, not the projection view:
   *  the view carries host/user but no root-vs-child identity, and the
   *  root position is the only fact that distinguishes "on the profile's
   *  own host" from "walked onto another one" without comparing host
   *  strings. */
  private get _paneWalkedToRemoteChild(): boolean {
    return this.lifecycle.domainStack.length > 1 && this._host !== ''
  }

  /** The ports.* target this tab's session scopes to (nocx-wzc4.8): the
   *  reserved "local" for a local shell, the saved-profile id for a
   *  saved-profile SSH tab, null for an alias tab — an alias has no
   *  profile until it is adopted, so it has no valid ports scope. Null
   *  also while the pane walks on a remote host it reached by hand — the
   *  scope follows where the pane IS, never how it was opened. */
  get portsTargetId(): string | null {
    if (this._paneWalkedToRemoteChild) return null
    if (this.sshOpts === undefined) return LOCAL_TARGET_ID
    return this.sshOpts.profileId || null
  }

  /** Why portsTargetId is null, when it is null because the pane went
   *  somewhere we cannot enumerate: the user@host of the remote child the
   *  pane walked into — the same derivation the location line shows, so
   *  the panel names the host exactly as the block header does. '' when
   *  there is no such reason. */
  get portsUnavailableReason(): string {
    if (this._paneWalkedToRemoteChild) return this.locationLine()
    return ''
  }
  /** The machine this tab's content speaks for (B.9) — the session it was
   *  opened with, answered only while that session is live and in front.
   *  Null when there is no honest answer: no session yet, or a session
   *  that has exited. Inside a nested environment the origin answers with
   *  the ACTIVE domain's view instead — blank (cwd and host null) for a
   *  child that has not reported, never the local session's values, which
   *  would show one machine's files while the user acts on another's
   *  (§0, the same rule the ports target applies).
   *  `kind` is how the session was opened, never inferred from the cwd or
   *  the title. `cwd`, `cwdVerified` and `host` are the ACTIVE domain's
   *  view (lifecycle/domain-environment.ts) — they follow the domain the
   *  lane's published fact names as active, blank over a lane gap, and the
   *  host never claims a remote target a fact did not name (bead
   *  nocx-u7uh.11). `cwd` is the verified-flag pair from _cwd /
   *  _cwdVerified: the composition layer may hand a VERIFIED cwd to
   *  files.open as rootPath (D2) and must surface an unverified one (AD-5). */
  activeOrigin(): Omit<ActiveOrigin, 'tabId'> | null {
    if (this.session === null || this._sessionExited) return null
    return {
      sessionId: this.session.sessionId,
      kind: this.sshOpts === undefined ? 'local' : 'ssh',
      // '' is "no cwd yet" inside the live session (a session opened with
      // no cwd, a fresh domain that has not reported yet, or a lane gap).
      cwd: this._cwd === '' ? null : this._cwd,
      cwdVerified: this._cwdVerified,
      cwdFollow: true,
      host: this._host || null,
    }
  }
  /** Push the composed title to the host: program title, else the cwd label. */
  private pushTitle(): void {
    if (!this.host) return
    const title = this.programTitle || this.cwdTitle
    this.host.setTitle(title)
    // The location line earns a row only when the title is a name of its own.
    // With no program title the title IS the location, and a second line would
    // print the first one again.
    this.hooks.onSubtitleChange?.(this.programTitle ? this.locationLine() : '')
  }

  /** Where this tab is, following the ACTIVE domain (bead nocx-u7uh.11):
   *  `user@host` when the active domain has a host — the session-open ssh
   *  binding, or an ssh child domain's own published destination
   *  (nocx-ax79) — else the active domain's working directory. The pre-severance
   *  destination-from-the-submitted-line behaviour (owner, 2026-08-04,
   *  three times) is deliberately NOT re-added here: a parsed command line
   *  never populates an authenticated domain's identity, and a nested
   *  environment is a place with no directory until it tells us otherwise
   *  (nocx-695k.2). */
  private locationLine(): string {
    if (this._host) return this._user ? `${this._user}@${this._host}` : this._host
    return this._cwd
  }

  /** Copy the environment projection's current view into the fields every
   *  derivation reads (`_cwd`, `_cwdVerified`, `_host`, `_user`,
   *  `programTitle` — the completion scope, the origin answer, the ledger
   *  records and the title all read these), then push every surface that
   *  presents them. Called by the projection on every environment change:
   *  an active-domain switch, an OSC 7 cwd, an OSC 0/2 title. */
  private _applyEnvironmentView(): void {
    const view = this.env?.view()
    if (!view) return
    // The ports scope is derived from the ACTIVE domain's host and the
    // kernel's stack, so the answer must be captured BEFORE the view copy
    // (which moves the pane's values into `_host`/`_user`) and compared
    // afterwards — entering or leaving a remote child flips it without a
    // tab switch, and the panel must re-scope then and only then.
    const portsTargetBefore = this.portsTargetId
    const portsReasonBefore = this.portsUnavailableReason
    this._cwd = view.cwd
    this._cwdVerified = view.cwdVerified
    this._host = view.host
    this._user = view.user
    this.programTitle = view.programTitle
    this.cwdTitle = directoryLabel(view.cwd)
    this.editor?.setCwd(view.cwd)
    this.onTooltipChange(
      view.host
        ? `SSH ${view.user ? view.user + '@' : ''}${view.host}`
        : cwdTooltip(view.cwd, view.cwdVerified),
    )
    // The block header's `user@host`. Empty for a local shell, where the
    // machine is implied and printing it on every block would be noise;
    // present for an ssh child, which is exactly the case where it is not
    // implied — home/pi on a far host reads like /home/pi here. ONE
    // derivation, routed to both chips — the block header's frozen record
    // and the prompt's live destination must never disagree.
    const location = this._host ? this.locationLine() : ''
    this.scrollback?.blockManager.setLocation(location)
    this.editor?.setLocation(location)
    this.pushTitle()
    // The origin answer changed (cwd, host, or the active domain itself):
    // origin-following surfaces (the Files panel's reveal) follow it live,
    // not on the next tab switch.
    this.hooks.onActiveOriginChange?.()
    // The ports scope follows the pane (nocx-695k.3): entering or leaving
    // a remote child changes the target or the unavailable reason without
    // a tab switch, and the panel must re-scope immediately. Fired on
    // CHANGE only — a cwd-only view change on the same child (an OSC 7)
    // must not spin the panel's poll.
    if (
      this.portsTargetId !== portsTargetBefore ||
      this.portsUnavailableReason !== portsReasonBefore
    ) {
      this.hooks.onPortsTargetChange?.()
    }
  }

  // ── The ssh environment boundary (nocx-mlm7 P9) — SEVERED ────────────
  // The environment activation machinery (_withProfileOverlay,
  // _buildPendingAttempt, _takePendingAttempt, the ssh attempt lifecycle,
  // _classifyD/_enterEnvironment/_handleLocalD/_popEnvironment, and the
  // passport observation report) rode the byte stream — expected passport →
  // tagged A → B → local D. ADR-0024 §1 cuts every one of those paths:
  // nothing stream-derived may activate, suspend or restore an environment.
  // The migration bead (nocx-u7uh.11) reconnected the projection to
  // authenticated domains: the domain-environment projection
  // (lifecycle/domain-environment.ts) scopes cwd, host, title and
  // completion to the ACTIVE domain, and the passport machinery is deleted.
  // environment-commands.ts stays as the label classifier for environments
  // nocx could not integrate — it never names an authenticated domain.

  // ── TabContent ──────────────────────────────────────────────────────────

  private openRequestedSession(): Promise<SessionHandle> {
    if (!this.sshOpts) {
      return this.client.openSession(this.cols, this.rows)
    }
    if (this.sshOpts.profileId) {
      return this.client.openSSHSession(this.cols, this.rows, this.sshOpts.profileId)
    }
    return this.client.openSSHSessionByHost(
      this.cols,
      this.rows,
      this.sshOpts.host,
      this.sshOpts.user,
    )
  }

  private async openSessionWithHostKeyRecovery(signal: AbortSignal): Promise<SessionHandle> {
    for (;;) {
      try {
        return await this.openRequestedSession()
      } catch (err) {
        const evidence = hostKeyEvidenceFromOpenError(err, this.sshOpts?.profileId)
        if (!evidence || !this.hooks.onHostKeyError) {
          throw err
        }
        const accepted = await this.hooks.onHostKeyError(evidence, signal)
        if (!accepted) {
          throw new Error(`Host key was not trusted for ${evidence.host}`)
        }
        if (signal.aborted) {
          throw new Error('SSH open cancelled')
        }
      }
    }
  }

  async mount(target: HTMLElement, host: TabHost, signal: AbortSignal): Promise<void> {
    if (this._disposed) return
    this.host = host
    // The pane the degraded-session card overlays. Captured here because
    // the status can arrive at any point in the session's life, long after
    // mount returned.
    this._paneTarget = target

    // Wire the signal: if the tab is disposed during mount, abort.
    if (signal.aborted) {
      this._readyResolve(false)
      return
    }
    this.mountAbortController = new AbortController()
    const onAbort = () => this.mountAbortController!.abort()
    signal.addEventListener('abort', onAbort, { once: true })

    try {
      // Wait for pane to become visible and have proper dimensions.
      await new Promise((resolve) => requestAnimationFrame(resolve))

      if (signal.aborted) {
        this._readyResolve(false)
        return
      }

      log.info('nocx: creating renderer')
      const renderer = new XtermRenderer()

      // ── DOM scrollback controller ───────────────────────────────────────
      this.scrollback = new ScrollbackController({
        pane: target,
        renderer,
        now: () => performance.now(),
        // The renderer owns this tab's OSC 636 store; the scrollback's frozen
        // headers and the editor below must judge against the same instance.
        snapshotStore: renderer.snapshotStore,
        // A `clear` took every block: the reference chips die with their
        // blocks — a chip whose block is gone would point at nothing
        // (AGENTS.md: a soft degrade the UI contradicts is how a feature
        // that does not exist survives a release).
        onClear: () => this.clearReferenceChips(),
      })

      log.info('nocx: mounting renderer')
      await renderer.mount(this.scrollback.mountTarget)

      if (signal.aborted) {
        renderer.dispose()
        this.scrollback.dispose()
        this._readyResolve(false)
        return
      }

      log.info('nocx: renderer mounted', { cols: renderer.cols, rows: renderer.rows })
      this.cols = renderer.cols
      this.rows = renderer.rows

      // ── Command ledger (ADR-0008, severed) ──────────────────────────────
      // The completion projection's app-owned half: records are opened at
      // the app-owned submit (ADR-0024 §5) and read back; nothing completes
      // them — the marker cycle is deleted — so the persistence seam
      // (recordCommand) has no terminal caller and history recording is
      // unavailable. The migration bead reconnects completion to
      // authenticated domain events.
      this.ledger = new CommandLedger({ now: () => Date.now() })

      // ── Wire input ownership BEFORE opening the session ─────────────────
      // Completion (design §8.7–§8.9): the dropdown + ghost text surface,
      // composed here so the editor stays passive. Providers: command names
      // from the OSC 636 snapshot (this renderer's own — correct on a remote
      // host, it is the running shell's answer), history over the control
      // plane (environment-scoped, the directory rung), and local filesystem
      // paths — active only when this tab's session is a local shell, so a
      // local path can never masquerade as a remote one (§8.5).
      this.completion = new CompletionController({
        providers: createShellProviders({
          store: renderer.snapshotStore,
          queryHistory: (cwd, host) => queryHistory(this.client, 'directory', cwd, host),
          completeFs: (text, cwd) => this.client.call<FsComplete>('fs.complete', { text, cwd }),
          // The remote completion adapter (nocx-w7h.15): active only on
          // remote sessions, where it asks the remote shell's own
          // completion machinery — paths from the remote filesystem,
          // command names, and command-specific completions from bash
          // completion functions.
          completeShell: (params) => this.client.call<ShellComplete>('shell.complete', params),
          sessionId: () => this.session?.sessionId ?? '',
          // The host provider is built inside createShellProviders (the
          // assembly it routes is plain code, not the DOM-bound quick-connect
          // module); this tab's ProfileClient is handed through, absent when
          // no connection manager is wired.
          profileClient: this.profileClient ?? undefined,
        }),
        dropdown: new CompletionDropdown({
          onHover: (index) => this.completion?.select(index),
          onPick: (index) => this.completion?.acceptIndex(index),
        }),
        env: () => ({ isLocal: !this.sshOpts, cwd: this._cwd, host: this._host }),
        recallIsOpen: () => this.recall?.isOpen ?? false,
      })
      // The DOCUMENT-level layer, shared by both targets: the
      // vault-reference chip (a decoration, not a language), the quiet
      // composition-time candidate mark, and the unresolved-redaction field
      // a recalled masked row registers in. None of them is about shell
      // syntax, so a question keeps them; and they are built ONCE, so
      // switching targets reconfigures the editor with the same state
      // fields rather than minting fresh ones under the same document.
      const documentLayer = [
        secretChipExtension(),
        secretCandidateExtension(),
        unresolvedRedactionField,
      ]
      this.shellTarget = new ShellInputTarget(
        (text: string) => renderer.paste(text),
        (data: string) => this.session!.send(data),
        // The target carries the shell's editor extensions through the §8.8
        // seam: the shell highlighter and the completion surface — the two
        // that ARE about commands — on top of the shared document layer.
        [
          ...shellExtensions(renderer.snapshotStore),
          ...this.completion.extensions(),
          ...documentLayer,
        ],
      )
      const vault = new VaultClient(this.client)
      this.vault = vault

      // The input-target registry (ADR-0004 §3): both targets registered,
      // shell active by default. The submit path below routes through
      // active().submit — the editor stays passive and never branches on
      // the mode. The agent target is constructed with this tab's seams:
      // the session id is read per submit (a reconnect mints a new
      // session — the target must never capture against a stale one).
      this.inputTargets = createRegistry((target) => {
        // The line-start indicator renders the registry's active target
        // and nothing else repaints it: this notification IS its refresh
        // signal (ask-entry.ts). The indicator derives its own WORD from
        // the target id — the registry's label stays the registry's.
        this.indicator?.set(target.id, target.label)
        // And the editor wears the target's own layer: the shell's
        // highlighting and completion surface belong to the shell, so a
        // question typed at Ask is plain prose — never `Привет!` painted
        // as a command with an operator in it. One authority decides
        // both — the registry — and the editor stays passive.
        this.editor?.setTargetExtensions(target.editorExtensions?.() ?? [])
      })
      this.inputTargets.register(this.shellTarget)
      this.agentTarget = new AgentInputTarget({
        dispatcher: this.client.dispatcher,
        sessionId: () => this.session?.sessionId ?? '',
        cwd: () => this._cwd,
        // The ask's payload is the reference chips in the input line —
        // never re-derived from DOM selection at submit time (AD-8:
        // selection is copy; the chip is the record).
        chips: () => this.referenceChips,
        // A new answer block, kept at the bottom of the view — the same
        // rule a command's output lives by, which the ask path never had:
        // nothing scrolled when a block was ADDED, so a question landed
        // below the fold whenever the transcript already filled the pane
        // (and looked fine whenever it did not, which is why it read as
        // intermittent). The controller's scrollToBottom is a no-op while
        // the person has scrolled away to read, so this follows without
        // ever yanking the view out from under them.
        openAnswer: (question, cwd) => {
          const handle = this.scrollback!.blockManager.addAnswerBlock(question, cwd)
          this.scrollback?.scrollToBottom()
          // The streamed answer grows the block, and growth is the same
          // situation as arrival: stay at the bottom unless the reader has
          // gone elsewhere.
          return {
            ...handle,
            append: (text: string) => {
              handle.append(text)
              this.scrollback?.scrollToBottom()
            },
            close: (status: 'success' | 'failure', error?: string) => {
              handle.close(status, error)
              this.scrollback?.scrollToBottom()
            },
          }
        },
        // The no-endpoint refusal is visible in the product, never only in
        // a log (AGENTS.md: a soft degrade the UI contradicts is how a
        // feature that does not exist survives a release).
        onRefusal: (message) => showToast({ level: 'warning', message }),
        // The typed readiness fact behind a refusal (agent.status), so the
        // target never has to read the reason out of the message text.
        status: () => new AgentClient(this.client.dispatcher).status(),
        // No endpoint: the toast says what is wrong and this opens where it
        // is fixed. A refusal with nowhere to go is how a person concludes
        // the feature is broken rather than unconfigured.
        onNoEndpoint: () => this.hooks.onCreateEndpoint?.(),
        // A question's editor layer: the DOCUMENT-level surfaces only. The
        // shell highlighter and the completion surface stay with the shell
        // — prose is not a command and must not be painted as one — while
        // the vault chip, its candidate mark and the unresolved-redaction
        // field are language-agnostic and keep working, so an inserted
        // reference is still a chip and not raw text in a question.
        editorExtensions: () => documentLayer,
      })
      this.inputTargets.register(this.agentTarget)
      // The caret indicator + its toggle: the person's one explicit
      // switch. Clicking the chip (or ⌘/Ctrl+Enter) flips the active target;
      // the registry's notification repaints the label. Ordinary use
      // never touches it — it is the confirmation that Enter goes to the
      // shell (nocx-4wtlh).
      this.indicator = new TargetIndicator(() => {
        const current = this.inputTargets?.active().id
        if (!this.inputTargets || !current) return
        this.inputTargets.setActive(current === 'shell' ? 'agent' : 'shell')
      })

      this.editor = new CommandEditor(
        {
          // The resolve half of ADR-0021, BEFORE the atomic handoff: a line
          // with references is resolved through vault.resolveLine; the
          // RESOLVED line goes to the PTY, the reference-intact line to the
          // ledger and history.record. A sealed vault or an unresolved name
          // is reported and the draft stays — never a silent send of a
          // broken line (the editor's beforeSubmit seam keeps the draft on
          // false). A plain line resolves SYNCHRONOUSLY (planSubmitSync) —
          // an ordinary Enter keeps its no-gap atomic handoff. A recalled
          // masked row is refused first: the draft stays and resolution
          // opens on the first chip (ADR-0021's consequence).
          beforeSubmit: (doc) => {
            if (this.promptVault?.openResolution()) return false
            const sync = planSubmitSync(doc)
            if (sync) {
              // ADR-0024 §1 (the projection bead nocx-u7uh.7): the typed-ssh
              // rewrite is integration-sensitive and is gated on the KERNEL
              // — rewriteAuthority() is true only at a live authenticated
              // prompt (lifecycle/state.ts), never on a stream latch. The
              // rewrite input (the launcher path and the per-attempt
              // environment id) is not available since the
              // shell.launcherCommand RPC was deleted with the marker
              // latch (nocx-u7uh.1), so the line goes out unchanged —
              // fail-open — until a rewrite input exists.
              return sync
            }
            return planSubmit(doc, (line) => vault.resolveLine(line)).then((verdict) => {
              if (isSubmitFailure(verdict)) {
                this.reportSubmitFailure(verdict)
                return false
              }
              return verdict
            })
          },
          submit: (doc: string, plan?: SubmitPlan) => {
            // Routing, ADR-0004 §3: the registry decides where the document
            // goes. A QUESTION is not a command — the shell orchestration
            // below (keyboard handoff, ledger record, running block,
            // lifecycle attempt) belongs to the shell target only. The
            // agent target owns its whole flow (frame mint, ask, answer
            // block), so the handoff never runs for it: the grid keeps its
            // keys, the flow gains no phantom running block, and no
            // attempt is opened for prose (nocx-x8s2.2).
            const active = this.inputTargets!.active()
            if (!active.routesToShell) {
              // The target surfaces its own refusal (onRefusal → the
              // toast); the rethrow is for programmatic callers, so the
              // fire-and-forget path swallows it.
              void active.submit(doc, { targetId: active.id }).catch(() => {})
              // The chips in the line are consumed: they rode this
              // question. The target reads them SYNCHRONOUSLY at the top of
              // submit (before its first await), so the clear after the
              // call can never eat them.
              this.clearReferenceChips()
              return
            }
            // The atomic handoff transfers input ownership to the grid at
            // the moment the editor gives it up — not when the running fact
            // lands (an RPC round trip later). The editor already hid itself
            // in commit; a grid that is not yet the keyboard's owner drops
            // keys typed into the gap, so a program waiting on stdin (read,
            // ssh, less) starves with no editor and no input surface
            // (nocx-u7uh.23, nocx-yb5y).
            // _syncLifecycleOwnership reconciles this on every fact.
            //
            // Both lines, always in this order and never one without the
            // other: the keyboard changes hands HERE, and from here until
            // the command's own bytes have gone out the grid's bytes queue
            // behind them (holdRawUntilSubmitted). Handing the keyboard over
            // without the queue is what turned a dropped keystroke into a
            // reordered one — measured, the letters of the next input
            // arriving at the pty ahead of the command that was going to
            // read them.
            this.takeKeyboardToGrid()
            this.holdRawUntilSubmitted()
            const recordLine = plan?.recordLine ?? doc
            // Where the command RUNS, captured before anything below can
            // change it. Entering an environment blanks `_cwd` (we know the
            // host, not the remote directory), and the ledger and the block
            // both read it further down — so `ssh pi@…` was recorded with no
            // directory and vanished from a history scoped to "this
            // directory". The command ran here, whatever it goes on to do.
            const submitCwd = this._cwd
            // An empty line is a bare newline: no execution, no attempt, no
            // ledger record (CommandLedger.open refuses empty commands) and
            // no block. The shell still gets its newline — a conventional
            // terminal stays conventional.
            const write = (): void => {
              // Detach the queue BEFORE the command goes out, flush it after.
              //
              // Both halves matter and the order is the whole point. The
              // command is delivered through renderer.paste, and a paste is
              // itself an onData — so a queue still armed here would swallow
              // the command and put it BEHIND the keys that were waiting for
              // it, which is the same reordering with the operands swapped
              // (measured: a bare `\r` reaching the pty ahead of its own
              // command line).
              const held = this.takeHeldRaw()
              try {
                submitCommand(doc, {
                  focusGrid: () => this.takeKeyboardToGrid(),
                  sendDoc: (d) => {
                    void active.submit(d, { targetId: active.id })
                  },
                })
              } finally {
                // In a `finally`, and fail-open: a write that threw sent
                // nothing, and holding the keys anyway would swallow them
                // for the rest of the session. Late at a prompt is a line
                // the user can see and erase; silently gone is not.
                for (const data of held) this.session?.send(data)
              }
            }
            if (recordLine === '') {
              write()
              return
            }
            // SEVERED (ADR-0024): the ssh attempt binding (expected passport
            // id, tagged A→B entry, local-D completion) and the
            // environment-entry heuristic (docker, su, …) are deleted with
            // the marker cycle — nothing stream-derived may activate an
            // environment, and without a completion there is nothing to
            // restore. The submitted line still opens a ledger record and a
            // running block (the app-owned ordering ADR-0024 §5 keeps).
            if (this.ledger) {
              let markerLine: () => number | undefined = () => undefined
              const rec = this.ledger.open(recordLine, submitCwd, this._host, () => markerLine())
              const m = renderer.registerMarker()
              if (m) {
                markerLine = () => m.line()
                this._markers.set(rec.id, m)
                m.onDispose(() => {
                  this.ledger?.dispose(rec.id)
                  this._markers.delete(rec.id)
                })
              }
            }
            this.scrollback?.maybeClear(recordLine)
            // The running block opens at the app-owned submit — before any
            // bytes and before any fact can arrive — so the published
            // running fact (which the backend emits BEFORE the RPC response,
            // inside SubmitAttempt) always finds the block it binds to
            // (ADR-0024 §5, §7). That ordering is why the block's CREATION
            // line is the prompt line, and why its OUTPUT range starts one
            // row later (nocx-4yhi): the bytes go out after this call, and
            // the shell's echo of the typed command lands on the creation
            // line itself. The header already shows the command; a body
            // that repeats it is the defect — so the range and the
            // creation time are two different things, and the record
            // carries both.
            if (this.scrollback && this.renderer) {
              const startLine = this.renderer.cursorLine()
              this.scrollback.beginBlock(recordLine, submitCwd, startLine, startLine + 1)
            }
            const st = this.lifecycle.state
            if (st.kind !== 'prompt_ready') {
              // No live domain: nothing to attach the app-owned text to. The
              // shell's own start (if any) opens a shell-originated attempt
              // and the block binds to it — a conventional terminal stays
              // conventional, and the privacy rule holds either way.
              write()
              return
            }
            // ADR-0024 decision 5: the app-owned attempt opens BEFORE the
            // bytes that can cause the shell's own start are written to the
            // pty; the later authenticated start attaches to it and replaces
            // nothing. Fail-open: a refused attempt (the domain lost its
            // prompt mid-typing) must never swallow the command — the bytes
            // still go out and the session stays conventional.
            void new LifecycleClient(this.client.dispatcher)
              .submitAttempt({
                domain: st.domain.id,
                command: recordLine,
                cwd: submitCwd,
                host: this._host,
              })
              .then(write, write)
          },
          // A bare newline: the shell gets its keystroke and answers with a
          // fresh prompt. Deliberately not routed through the submit path —
          // no attempt is opened and no ledger record is written, because
          // nothing was executed (ADR-0024 §5).
          submitEmpty: () => this.session?.send('\r'),
          cancel: () => this.session?.send('\x03'),
          // A taller editor is a shorter scrollback. Keep the bottom of the
          // transcript where it belongs — just above the editor — instead of
          // letting it slide underneath.
          resized: () => this.scrollback?.scrollToBottom(),
          /** Drive the completion and vault surfaces (the candidate's
           *  detection, the picker's passive filter). */
          onInputChange: (text) => {
            // A keystroke aborts the completion query in flight and starts a
            // fresh one (design §8.9.2); the ghost text re-anchors.
            this.completion?.onDocChanged()
            this.promptVault?.onDocChanged(text)
          },
          /** A programmatic clear (submit, Esc, Ctrl-C): the vault surfaces
           *  hold stale findings over a cleared line. */
          onDocCleared: () => {
            this.promptVault?.reset()
          },
          /** '@' at a word start — the reference picker's passive trigger.
           *  Opening it closes the completion dropdown: the surfaces never
           *  stack (the mutual-exclusion rule). */
          onSecretPicker: (triggerPos) => {
            this.completion?.dismiss()
            this.promptVault?.onSecretPicker(triggerPos)
          },
          /** Up on the first line (or an empty draft): no further caret
           *  movement, so open the recall overlay (design §8.10 v6). */
          onUpAtTop: () => {
            void this.recall?.open('directory')
          },
          /** Tab opens the completion dropdown (§8.7's decided option 1). */
          onTab: () => this.completion?.open(),
          /** The save chord: a live receipt's primary action outranks the
           *  composition-time candidate; ⇧⌘S moves focus into the receipt
           *  for review. Returns whether anything was triggered (the editor
           *  consumes the chord either way). */
          onSave: (shift) => {
            // The chord acts on the NEWEST unanswered receipt; with none
            // open it falls through to the composition-time candidate.
            if (this.receipt) {
              if (shift) this.receipt.enterReview()
              else this.receipt.saveAll()
              return true
            }
            return this.promptVault?.saveCandidate() ?? false
          },
          /** Whether a commit performs the shell handoff (ADR-0004 §2
           *  step 1: the editor hides itself before anything is sent). The
           *  TARGET declares what it is (routesToShell); this reads the
           *  registry — the one authority — so the agent target's question
           *  keeps the editor on screen for the next one (nocx-wmy4). */
          handoffToShell: () => this.inputTargets?.active().routesToShell ?? true,
          // ⌘/Ctrl+Enter: the explicit switch (ADR-0004 §3) — same flip as
          // clicking the caret indicator, and the only thing the chord
          // does. Asking is plain Enter with Ask active.
          onToggleTarget: () => {
            const current = this.inputTargets?.active().id
            if (!this.inputTargets || !current) return
            this.inputTargets.setActive(current === 'shell' ? 'agent' : 'shell')
          },
          onDismissChip: (id) => {
            this.referenceChips = this.referenceChips.filter((c) => c.id !== id)
            this.editor?.setReferenceChips(this.referenceChips)
          },
        },
        // The language is chosen HERE, not inside the editor. CommandEditor
        // must stay language-agnostic (ADR-0010 §Decision 3): the agent target
        // wants prose on this same surface, and an editor that defaults to
        // shell would have to be edited to gain one — exactly what ADR-0004
        // §3 exists to prevent. The seam (design §8.8) carries the layer: the
        // target supplies its extensions, the editor never hard-codes them.
        //
        // Only the STABLE layer is passed here. The target's own layer goes
        // through setTargetExtensions below, because it changes when the
        // person switches targets.
        [
          // The caret indicator (nocx-4wtlh): composed at the root, outside
          // the target's layer — it renders the registry's ACTIVE target, so
          // it belongs to neither target alone and must survive every swap.
          this.indicator.extension(),
        ],
      )
      // The layer of the target Enter goes to right now (shell at start).
      this.editor.setTargetExtensions(this.inputTargets.active().editorExtensions?.() ?? [])
      this.editor.mount(target)
      this.completion.attach(this.editor, this.editor.root)
      this.promptVault = new PromptVaultController({
        editor: this.editor,
        vault,
        report: (level, message) => showToast({ level, message }),
        requestSetupDialog: () => this.hooks.onSetupVault?.(),
        requestCreateSecret: (name) => this.hooks.onCreateSecret?.(name),
      })
      this.promptVault.mount()

      // ── Recall overlay (Provenance Recall, design §8.10) ────────────────
      // The history palette above the prompt. Rows are served by the store
      // over the control plane (history.query, source=store); when the
      // store cannot answer, the overlay falls back to the in-memory ledger
      // with source=session, which the panel labels "this session only" —
      // presenting one session as all history is the same lie as marking
      // every command green. The editor's key arbiter gives the overlay
      // first refusal while it is open; navigating previews into the
      // editor, and Enter executes through the editor's own submit path
      // (nocx-w7h.5).
      this.recall = new RecallOverlay({
        editor: this.editor,
        query: async (scope, text) => {
          try {
            const page = await queryHistory(this.client, scope, this._cwd, this._host, text)
            // A command run in THIS session comes back as it was run, not as
            // the store had to keep it (nocx-xkve.4). Recall only — the
            // completion provider above keeps reading the store, so ghost
            // text and candidates stay masked.
            return withSessionText(page, this.ledger)
          } catch {
            return queryLedgerHistory(this.ledger, scope, this._cwd, this._host, text)
          }
        },
        // A recalled masked row cannot run as written (ADR-0021): the
        // overlay reports the row's redaction spans every time it places
        // text in the editor (preview, insert, draft restore), the editor
        // renders them as unresolved chips, and the beforeSubmit seam
        // refuses to run the command while any remain — opening resolution
        // on the first chip. This is the door people will actually walk
        // through: nobody plans to store a secret in advance, they hit a
        // command that cannot run.
        onDocContent: (doc, redactions) =>
          this.promptVault?.onRecalledRow(
            redactions.map((r) => ({ from: r.start, to: r.end, kind: r.kind })),
          ),
      })
      this.recall.mount(this.editor.root)
      // ONE arbiter chain (design §8.9.4 — three surfaces, one keyboard),
      // and the OWNERSHIP it implements, stated as decisions rather than
      // as an evaluation order (nocx-mlm7 — the third instance of two
      // surfaces owning one input; the order alone reads as accident and
      // regresses):
      //
      //   - The explicit recall shortcut (Ctrl/Cmd+R) opens recall from
      //     anywhere — even under an open dropdown or picker — and while
      //     recall is open, its keys (arrows, Enter, Esc, typing) belong
      //     to it. Opening any surface closes the others: the surfaces
      //     never stack.
      //   - The vault picker, while open, owns its keys (arrows, Enter,
      //     Tab, Esc): completion is dismissed under it.
      //   - While the completion dropdown is open with a selectable list,
      //     bare ArrowUp/ArrowDown belong to IT — its footer says
      //     "↑ ↓ to navigate". Recall's bare-Up gesture (up at the top of a
      //     single-line draft opens recall) therefore applies only when no
      //     such dropdown is open; completion.ownsArrows is the same
      //     decision in both places. The gesture is NOT removed, and the
      //     dropdown is NOT closed early to make room for it — the keys
      //     are the dropdown's while it is open.
      //   - Esc closes exactly one surface per press, in the same order:
      //     recall, picker, completion.
      this.editor.setKeyArbiter((e) => {
        // Every decision here is traced (off by default — `window.nocxDebug
        // = true` in devtools): which surface got the key and why is the
        // diagnosis for "my key did the wrong thing", and the chain's
        // evaluation order is the accident that reads as ownership unless
        // the decision is stated. The gate is checked BEFORE any field is
        // built: with tracing off, a keystroke costs nothing but the check.
        // Recall first: the shortcut opens it from anywhere, and an open
        // recall owns its keys. Opening it closes the other surfaces.
        const recallWasOpen = this.recall!.isOpen
        const consumed = this.recall!.handleKey(e)
        if (this.recall!.isOpen) {
          this.completion?.dismiss()
          this.promptVault?.closePicker()
        }
        if (consumed) {
          if (isDecisionTracing()) {
            logDecision('arbiter', {
              surface: 'recall',
              key: keyLabel(e),
              why: recallWasOpen ? 'recall is open; it owns its keys' : 'recall shortcut',
            })
          }
          return true
        }
        // The picker outranks completion: while it is open its keys
        // (arrows, Enter, Tab, Esc) belong to it, and a Right/End ghost
        // accept must never insert completion text into the line under the
        // picker.
        if (this.promptVault!.isPickerOpen) this.completion?.dismiss()
        if (this.promptVault!.handleKey(e)) {
          if (isDecisionTracing()) {
            logDecision('arbiter', {
              surface: 'picker',
              key: keyLabel(e),
              why: 'picker is open; it owns its keys',
            })
          }
          return true
        }
        // The ownership decision above, applied: an open selectable
        // dropdown owns bare ArrowUp/ArrowDown — its footer promises
        // navigation — so they are routed to it explicitly and can never
        // fall through to the editor's recall gesture. Everything else
        // (Enter, Tab, Esc, Right/End, typing) still goes to the
        // completion controller's ordinary handling.
        if (this.completion!.ownsArrows(e)) {
          if (isDecisionTracing()) {
            logDecision('arbiter', {
              surface: 'completion',
              key: keyLabel(e),
              why: 'dropdown is open; bare arrows belong to it',
            })
          }
          return this.completion!.handleKey(e)
        }
        const handled = this.completion?.handleKey(e) ?? false
        if (handled) {
          if (isDecisionTracing()) {
            logDecision('arbiter', {
              surface: 'completion',
              key: keyLabel(e),
              why: 'completion consumed the key',
            })
          }
          return true
        }
        if (isDecisionTracing()) {
          logDecision('arbiter', {
            surface: 'editor',
            key: keyLabel(e),
            why: 'no surface claimed the key',
          })
        }
        return false
      })

      if (signal.aborted) {
        this.recall?.destroy()
        this.recall = null
        this.completion?.destroy()
        this.promptVault?.destroy()
        this.promptVault = null
        this.completion = null
        this.editor.dispose()
        renderer.dispose()
        this.scrollback.dispose()
        this._readyResolve(false)
        return
      }
      renderer.onCommandMarker((marker) => {
        // SEVERED (ADR-0024 §1): PTY output is render-only. OSC 133 A/B
        // partition prompt bytes from output bytes for rendering and interop
        // with other tools; C and D have no meaning to nocx. No stream
        // sequence may grant keyboard ownership, open or complete an attempt,
        // assign an exit status, persist history, open or freeze a block,
        // activate an environment or enable rewriting — every one of those
        // paths was deleted with the marker cycle (nocx-u7uh.1).
        logDecision('marker observed', { kind: marker.kind, exitCode: marker.exitCode })
      })

      renderer.onBufferChange((type) => {
        this._bufferType = type
        // The buffer is its own axis (ADR-0024 §6): a renderer-owned
        // presentation fact, never an authority. The kernel tracks it
        // independently of the lifecycle, so entering or leaving the
        // alternate buffer can never restore ownership.
        this.lifecycle.setBuffer(type)
        if (type === 'alternate') {
          this.scrollback?.enterFullscreen()
        } else {
          // A conventional terminal stays unstructured: the scrollback-block
          // model never takes over (ADR-0024 §4). exitFullscreen first —
          // setUnstructured declines while an alt-screen program owns the
          // pane.
          this.scrollback?.exitFullscreen()
          if (
            fallsToConventionalGrid(
              shellStateFromLifecycle(this.lifecycle.state, this.lifecycle.domainStack),
            )
          ) {
            this.scrollback?.setUnstructured()
          } else if (this.lifecycle.state.kind === 'running') {
            // A command still in flight (the completion fact has not
            // landed): the command cycle owns the live region, so keep it
            // up — the authenticated completion freezes the block and
            // collapses it. A ready prompt (prompt_ready) stays on
            // exitFullscreen's idle layout, the structured block
            // presentation the live domain entitles the session to
            // (nocx-u7uh.26).
            this.scrollback?.setRunning()
          }
        }
      })

      // Match the shell's one-shot recovery fence in the render stream — an
      // explicit rendezvous, never a grid inspection or a pattern-matched
      // prompt (decision 1 carve-out). Only after BOTH the fence matched
      // and the conventional presentation is applied (the lost fact already
      // revoked authority and routed input raw) does the tab acknowledge.
      renderer.onRecoveryFence((hex) => {
        if (this._recovery && hex === this._recovery.fence) void this._ackRecovery()
      })
      this._lifecycleChangeUnsub = this.lifecycle.onChange(() => {
        this._syncLifecycleOwnership()
        this._updateCapability()
        // A conventional terminal stays unstructured: the scrollback-block
        // model never takes over (ADR-0024 §4). Conventional means no live
        // authenticated domain — Native, Lost, and a Desynchronized domain
        // (decision 9: a desynchronized domain is not live; its terminal
        // stays visible and its events are quarantined). With a live domain
        // (prompt_ready / running) the command cycle owns the live region —
        // beginBlock, freezeFromAttempt and the fullscreen path — and an
        // unconditional setUnstructured here tore that layout down on every
        // fact, so no block ever showed (nocx-u7uh.25). A handover — the
        // parent suspended for the ssh handshake, or the child's domain
        // closed and the parent has not yet reclaimed the lane — is NOT
        // conventional: the lane still has a domain waiting below, and
        // dropping the structure there is what showed the previous
        // attempt's output as one continuous terminal (nocx-mlyu).
        if (
          fallsToConventionalGrid(
            shellStateFromLifecycle(this.lifecycle.state, this.lifecycle.domainStack),
          )
        ) {
          this.scrollback?.setUnstructured()
        } else if (this.lifecycle.state.kind === 'prompt_ready') {
          // A ready prompt with a live domain presents the structured idle
          // layout (nocx-u7uh.27): the block model is the presentation an
          // integrated session is entitled to (ADR-0024 §4), and nothing
          // else would move the pane off the conventional grid it was left
          // in — the first prompt after integration, or the prompt after a
          // desynchronized episode comes back, used to stay flat until the
          // first command opened a block. 'running' is deliberately absent:
          // the command cycle owns the live region there (beginBlock,
          // freezeFromAttempt and the fullscreen path), and forcing idle
          // mid-command would collapse it. setIdle declines while an
          // alt-screen program owns the pane.
          this.scrollback?.setIdle()
        }
      })

      // ── The disposable projections (ADR-0024 §5–§7, bead nocx-u7uh.7) ──
      // The ledger, history and the block model consume the kernel and hold
      // no lifecycle state of their own: on every kernel change the
      // projections reconcile from state. The block port is the DOM half;
      // the history port persists ONLY completed app-owned records — a
      // shell-originated attempt's line, which may carry a literal
      // password, opens no ledger record and persists nowhere (the
      // command-text decision this bead owns).
      this._projections = new LifecycleProjections(
        this.lifecycle,
        this.ledger,
        {
          bindBlock: (attempt) => {
            // The running block opened at the app-owned submit binds to the
            // published attempt (ADR-0024 §5 attachment semantics).
            this.scrollback?.blockManager.bindAttempt(attempt.id)
          },
          openBlock: (attempt) => {
            // A shell-originated attempt: the block gives a native-mode
            // command structure; its text is already on the terminal and
            // never persists (the command-text decision).
            if (!this.scrollback || !this.renderer) return
            // No outputStart override here — unlike the app-owned submit,
            // this block opens at the cursor line at fact time, AFTER the
            // echo: the user typed the command at the shell and it was
            // echoed as they typed, and the running fact (which the shell
            // emits as the command starts) lands on or past the echo line.
            // outputStart therefore defaults to startLine (nocx-4yhi).
            this.scrollback.beginBlock(
              attempt.command || '(empty)',
              this._cwd,
              this.renderer.cursorLine(),
            )
            this.scrollback.blockManager.bindAttempt(attempt.id)
          },
          freezeBlock: (attempt) => {
            // ADR-0024 §7: the visual freeze is authorized only by the
            // authenticated completion (kernel derivation). The render-fence
            // rendezvous is bead nocx-u7uh.8; the freeze lands on the event
            // for now, at the current output end.
            if (!this.scrollback || !this.renderer) return
            if (!kernelFreezeBlock(attempt, attempt.domain)) return
            this.scrollback.freezeFromAttempt(attempt, this.renderer.cursorLine())
          },
          abandonBlock: (attempt) => {
            // The attempt went unknown (loss, closure, native escape): the
            // block freezes as abandoned — never successful.
            if (!this.scrollback || !this.renderer) return
            this.scrollback.abandonAttempt(attempt, this.renderer.cursorLine())
          },
          enterBlock: () => {
            // The far session began: the local `ssh` block ends here with
            // no exit status — the process is alive and reports its own at
            // the local D (nocx-95kt) — and the running slot is freed for
            // the far host's blocks. Wired here for the first time: the
            // block manager has always had freezeEntered and nothing ever
            // called it, so every ssh block ran forever (nocx-z5k9).
            if (!this.scrollback || !this.renderer) return
            this.scrollback.enterBlock(this.renderer.cursorLine())
          },
          abandonPending: () => {
            // The block opened at the submit and its domain ended before any
            // attempt arrived — `exit` is the case, and the start frame it
            // would have needed dies with the shell (nocx-mlyu). Nothing can
            // complete it, so it freezes as unknown rather than climbing.
            if (!this.scrollback || !this.renderer) return
            this.scrollback.abandonUnbound(this.renderer.cursorLine())
          },
        },
        (rec, attempt) =>
          recordCommand(this.client, rec, attempt).then((ack) => {
            if (ack) {
              const block = this.scrollback?.blockManager.blockForAttempt(attempt.id)
              this.attachRecordedAck(rec.id, block, ack)
            }
            return ack
          }),
      )
      this._projections.attach()

      // The kernel starts Native and onChange may not fire for the initial
      // state: present the session with the terminal visible from the first
      // byte, and the editor hidden (a conventional terminal, no ownership).
      this._syncLifecycleOwnership()
      this.scrollback?.setUnstructured()

      // ── Focus bounce (P0-4) ────────────────────────────────────────────
      target.addEventListener('focusin', () => {
        if (!this.editor?.isVisible) return
        const active = document.activeElement
        // The receipt's review mode is the ONE place in this design where
        // focus leaves the editor: ⇧⌘S parks it in the name fields. The
        // bounce must yield to it, or the caret snaps straight back and the
        // receipt cannot be edited at all.
        if (
          active &&
          (this.editor.rootContains(active) ||
            this.scrollback?.xtermLiveContainer.contains(active) ||
            this.receipt?.root.contains(active))
        )
          return
        this.editor.focus()
      })

      // Click anywhere on the editor card and the prompt takes focus — the
      // card's padding and chrome are all "the prompt" as far as a user is
      // concerned. Except a control the user clicked ON PURPOSE: the vault
      // offer's name field and its buttons live inside this root, so an
      // unconditional focus() bounced the caret straight back to the prompt
      // and the field could not be typed into at all. Same guard the keydown
      // path uses — a nested form control owns its own focus and its own
      // keys.
      this.editor.root.addEventListener('click', (e) => {
        const target = e.target as HTMLElement | null
        if (target?.closest('input, textarea, select, button')) return
        this.editor?.focus()
      })

      this._globalKeydown = (e: KeyboardEvent) => {
        // Read the flag the chrome set, not the class it rendered (nocx-fttm).
        if (!target.isConnected || !this._active) return
        if (this.scrollback && this.scrollback.selectedBlockId !== null) {
          if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
            e.preventDefault()
            this.scrollback.deselectBlocks()
            if (this.editor?.isVisible) {
              this.editor.focus()
              this.editor.insertText(e.key)
            }
            return
          }
          if (e.key === 'Escape') {
            this.scrollback.deselectBlocks()
            e.preventDefault()
            return
          }
        }
        // Paste (Cmd/Ctrl+V) belongs to the same rescue policy: wherever in
        // the pane the user clicked — a frozen block, the scrollback, the
        // running grid — the paste must reach the command editor, and it
        // must never reach the shell as a literal \x16. The same isTextEntry
        // guard keeps other surfaces' paste to themselves (a settings field,
        // quick connect, a dialog). When the overlay is open, its arbiter
        // runs before this document listener on the editor's own keys and
        // decides first; when the editor itself has focus this branch is
        // skipped by the guard and the editor's own paste path runs.
        // Focus-only, deliberately: leave the keydown uncancelled so the
        // browser emits its paste event at the now-focused editor and CM6's
        // own paste inserts at the caret — reading the clipboard here would
        // bypass CM6 and fight the gesture.
        if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'v') {
          if (isTextEntry(document.activeElement)) return
          if (this.editor?.isVisible) {
            this.scrollback?.deselectBlocks()
            this.editor.focus()
          }
          return
        }
        // Escape with the editor on screen but out of focus: the editor's
        // own capture listener only sees keys that traverse its surface, so
        // after a click elsewhere — a frozen block, the scrollback, the
        // chrome — the key never reaches it and the draft survives an
        // Escape, which reads as "Esc does nothing". The same rescue policy
        // as the typing path below: somebody else's text control keeps its
        // Escape (an input clears itself, a dialog closes itself), the
        // overlay stack owns Escape while a modal is up, and the block
        // action menu closes itself. The editor routes the key through its
        // own decision order — the recall arbiter first, so an open overlay
        // dismisses and restores its captured draft instead of having the
        // draft cleared under it. When the editor itself has focus this
        // branch is skipped by the rootContains guard and the editor's own
        // handling (which stops propagation) decides.
        if (e.key === 'Escape' && this.editor?.isVisible) {
          const active = document.activeElement
          if (active && this.editor.rootContains(active)) return
          // A click on the live grid parks focus in xterm's hidden
          // textarea — the terminal's own surface, not somebody else's
          // field: while the editor is up the grid is read-only dead
          // space, so the rescue runs here too (when the editor is hidden
          // this branch never runs and the key reaches the shell as
          // before).
          const onLiveGrid =
            active !== null && this.scrollback?.xtermLiveContainer.contains(active) === true
          if (!onLiveGrid && isTextEntry(active)) return
          if (hasOpenOverlays()) return
          if (document.querySelector('.cmd-overflow-menu')) return
          if (this.editor.handleExternalEscape(e)) e.preventDefault()
          return
        }
        if (!this.editor?.isVisible) return
        if (e.key.length !== 1 || e.ctrlKey || e.metaKey || e.altKey) return
        const active = document.activeElement
        if (
          active &&
          (this.scrollback?.xtermLiveContainer.contains(active) || this.editor.rootContains(active))
        )
          return
        // Somebody else's text control has the focus — the tab strip's filter, a
        // settings field, a dialog. This handler is on `document`, so it sees
        // every keystroke in the window, and the rescue it performs (pull focus
        // into the prompt so typing "just works" after a click on the pane) is
        // exactly wrong when the user is deliberately typing somewhere else: the
        // first character lands in the field, focus jumps, and the rest goes to
        // the shell. Whitelisting the editor and the grid was not enough, because
        // any control OUTSIDE the terminal is equally not ours.
        if (isTextEntry(active)) return
        // A control the user deliberately focused keeps its keys. The rescue
        // is for a click on the pane followed by typing; a focusable control
        // — something a user could have TABBED to — is the opposite of a pane
        // click, and stealing its keys breaks the control: Space on a focused
        // button must activate the button, never type into the prompt
        // (nocx-nak2 — the Section disclosure's Space, and every button's,
        // was being eaten; the disclosure is operable with Enter and Space
        // only because this stands down).
        // `[tabindex]:not([tabindex="-1"])` is this repository's existing
        // spelling of "focusable by the user" — page.tsx, prompt.tsx and
        // dialog.tsx all use it, and AD-8 says the concept gets one owner.
        // A bare `[tabindex]` would also match tabindex="-1", which is
        // programmatically focusable and NOT tabbable: the tab strip's
        // roving items and CollectionRow's inactive rows carry it, and
        // matching them would stand the rescue down exactly when a user
        // clicked a tab and started typing — the case it exists for.
        if (
          active !== null &&
          active.matches(
            'button, select, a[href], [role="button"], [tabindex]:not([tabindex="-1"])',
          )
        )
          return
        // Focus-only, deliberately. The keydown's target was fixed when it was
        // dispatched — this keystroke started outside the editor, so the event
        // never reaches the editor's own keydown listener. The design contract
        // is: move focus synchronously, then leave the browser's native
        // insertion uncancelled, and let the character arrive as the native
        // default action of the keydown that is still in flight — it targets
        // whatever is focused when it runs, which is the contentDOM this
        // focus() just made active. So this path must NOT do what the block
        // path above does: preventDefault() would throw the native insertion
        // away, and insertText() on top of it would risk a second copy of the
        // same character. The asymmetry is the point — the block path's event
        // target is a block, never an editing host, so it has no native
        // insertion to lean on. (How faithfully the native path lands is
        // engine-dependent and is verified in a real browser, not in jsdom,
        // which performs no native insertion at all.)

        this.editor.focus()
      }
      document.addEventListener('keydown', this._globalKeydown)

      this.scrollback?.scrollbackArea.addEventListener('mousedown', (e) => {
        if (!(e.target as HTMLElement).closest('.cmd-block')) {
          this.scrollback?.deselectBlocks()
        }
      })

      // ── DOM block copy-on-select (P0-5) ───────────────────────────────
      // Frozen output only. Copy-on-select is the terminal's convention and it
      // belongs to text you can only read: selecting output is how you take
      // it. In the EDITOR the same gesture means the opposite — you select in
      // order to replace — so copying there overwrites the clipboard with the
      // very text about to be deleted. The owner selected part of a header to
      // paste a key over it and the key was gone. Explicit Ctrl/Cmd+C still
      // copies from the editor; nothing takes the clipboard unasked.
      this.scrollback?.scrollbackArea.addEventListener('mouseup', () => {
        const sel = window.getSelection()
        if (!sel || sel.isCollapsed) return
        const text = sel.toString()
        if (!text) return
        if (!this.scrollback?.scrollbackArea.contains(sel.anchorNode)) return
        if (shouldCopy(text)) {
          this.clipboard.writeText(text).catch((e) => {
            console.warn('nocx: clipboard write failed (block selection)', e)
          })
        }
      })

      // ── The authenticated lifecycle (ADR-0024 §6, decision 7) ──────────
      // The published fact is the ONLY input to the lifecycle kernel: no
      // stream sequence reaches it, and the eslint Rule 9 boundary forbids
      // the import that would create a path. The backend routes facts to
      // this session's lane; the kernel adopts the first lane and rejects
      // the rest.
      const lifecycleSubscription = new LifecycleClient(
        this.client.dispatcher,
      ).subscribeLifecycleChanged((fact) => {
        // ADR-0024 decision 8: a lost fact carrying a recovery contract
        // opens a restoration episode — the channel died while the shell
        // was reachable, and the shell will restore its visible native
        // prompt at the next prompt boundary, writing the one-shot fence.
        // From this instant until the acknowledgement lands, the session
        // is neither an authenticated terminal nor advertised as a usable
        // conventional one (the capability rail is suppressed below; the
        // editor holds no authority and offers none). A native fact ends
        // the episode.
        if (fact.lifecycle === 'lost' && fact.recovery) {
          this._recovery = { fence: fact.recovery.fence, generation: fact.recovery.generation }
        } else if (fact.lifecycle === 'native') {
          this._recovery = null
        }
        // The kernel applies the fact and notifies onChange on a real
        // change; the ownership sync runs there, once.
        this.lifecycle.applyFact(fact)
        // ADR-0024 decision 9: the establishment is acknowledged only
        // AFTER the presentation is committed — applyFact above is what
        // makes the editor available (ownership syncs on its onChange).
        // The backend flushes the pending accept, and the shell may
        // suppress its native prompt, ONLY on this acknowledgement for
        // this exact generation. Without it the handshake times out and
        // the session stays conventional with a visible prompt, which is
        // the fail-open direction: no window in which the prompt is
        // suppressed and no editor exists.
        if (
          fact.lifecycle === 'prompt_ready' &&
          fact.generation &&
          fact.generation !== this._establishmentAckInFlight &&
          fact.generation !== this._establishmentAcked &&
          this.session
        ) {
          const generation = fact.generation
          this._establishmentAckInFlight = generation
          new LifecycleClient(this.client.dispatcher)
            .establishAck(
              this.session.sessionId,
              fact.lane,
              fact.domain ?? '',
              fact.epoch ?? 0,
              generation,
            )
            .then(() => {
              // Only a landed acknowledgement retires the generation. The
              // backend has flushed the accept, so a later replay of the
              // same projection needs no second ack.
              this._establishmentAcked = generation
              if (this._establishmentAckInFlight === generation) {
                this._establishmentAckInFlight = null
              }
            })
            .catch((e: unknown) => {
              // Release the claim: this generation was NOT acknowledged, and
              // a replay carrying it again — the reattach case, where only a
              // fresh shell hello would have minted a new one — is the retry
              // that can still complete the handshake.
              //
              // A refusal is usually the backend's own bookkeeping (stale
              // generation, superseded establishment, replaced subscriber),
              // and then the replay simply does not come. Retrying costs one
              // refused call in that case and recovers the session in the
              // case that matters, so releasing is the safe direction.
              //
              // The MESSAGE, not just the error object: five distinct
              // backend rules all refuse with -32603, and logging the
              // error alone rendered as `{"code":-32603,"name":"RpcError"}`
              // — identical for every one of them. A reader could see that
              // the handshake had been refused and never which rule did it,
              // which is how the cause of six failing specs stayed
              // "unknown" across three triage rounds (nocx-cbtc). The
              // backend names the rule in its own log; this is the half a
              // trace carries.
              if (this._establishmentAckInFlight === generation) {
                this._establishmentAckInFlight = null
              }
              log.warn('nocx: establishment acknowledgement refused', {
                reason: e instanceof Error ? e.message : String(e),
                generation,
                lane: fact.lane,
                domain: fact.domain ?? '',
                epoch: fact.epoch ?? 0,
              })
            })
        }
      })
      this._lifecycleUnsub = lifecycleSubscription.unsubscribe
      const session = await this.openSessionWithHostKeyRecovery(signal)

      if (signal.aborted) {
        session.close()
        this.editor.dispose()
        renderer.dispose()
        this.scrollback.dispose()
        this._readyResolve(false)
        return
      }

      this.session = session
      lifecycleSubscription.bindSession(session.sessionId)
      log.info('nocx: session opened', { sid: session.sessionId, cwd: session.cwd || '' })

      // The open ack carries the resolved launch policy and the refusal
      // reason (nocx-4t37.2): the capability control starts from the
      // backend's own resolution, never from a second fetch that could
      // disagree with it.
      this._policy = session.desiredMode ?? 'script'
      // The integration axis is a SUBSCRIPTION, not a field of the ack: the
      // backend revises it as it learns (starting → integrated, or →
      // conventional with a reason, or → lost). Subscribed before anything
      // else touches the session so the first status — which the server
      // sends immediately after the ack — cannot arrive unheard.
      this._integrationUnsub = subscribeIntegrationChanged(this.client.dispatcher, (fact) => {
        if (fact.sessionId !== session.sessionId) return
        this._applyIntegration(fact)
      })
      // The statement is OBSERVED: until the first marker arrives, an auto
      // session honestly reads "Native input" — the launcher may be
      // mid-start, and the first prompt flips it to command blocks.
      this._updateCapability()
      // The domain-scoped environment projection (bead nocx-u7uh.11):
      // cwd, host, the tab title and the completion scope follow the ACTIVE
      // domain. The lane tier is seeded from the session-open facts — the
      // provider's cwd guess (unverified, AD-5) and the ssh binding — and
      // the root domain inherits them at establishment, so integration
      // takes over seamlessly. A fresh CHILD domain starts blank and is
      // populated by its own reports, exactly as the parent was.
      this.env = new DomainEnvironmentProjection(this.lifecycle, () => this._applyEnvironmentView())
      this.env.seedLane({
        cwd: session.cwd || '',
        cwdVerified: false,
        host: this.sshOpts?.host || '',
        user: this.sshOpts?.user ?? '',
        isLocal: !this.sshOpts,
        programTitle: this.sshOpts?.host || '',
      })
      this.env.attach()
      // The origin answer changed from null to a live session: an
      // already-active tab whose session (re)opens must push the change
      // without a tab switch, the same as a cwd or an environment change.
      // _applyEnvironmentView fires it after every field activeOrigin()
      // reads is initialised (the projection's first reconcile applies the
      // lane seed above).
      this._applyEnvironmentView()

      // Signal adoptability for alias tabs (no saved profile yet).
      // Must come after the session opens so adoption is only offered
      // to sessions that actually connected — a failed connect never
      // reaches this point (it throws to the outer catch).
      if (this.sshOpts && !this.sshOpts.profileId) {
        this.hooks.onAdoptabilityChange?.(true)
      }
      this.pushTitle()

      session.onData((data: string) => {
        log.debug('nocx: session data received', { length: data.length })
        renderer.write(data)
        this.scheduleLiveResize()
        if (this._bufferType === 'normal' && Date.now() >= this.echoUntil) {
          host.requestAttention()
        }
      })

      // Keyboard → PTY: xterm.js fires onData for every keystroke when stdin
      // is enabled (setReadOnly(false)). The editor captures keys while it is
      // visible and the terminal is read-only, so these only arrive in RAW mode.
      //
      // Held, never dropped, while a submitted command is still on its way to
      // the pty: the keyboard changed hands at the commit and the command
      // goes out an RPC later, so these bytes belong AFTER it (_heldRaw).
      renderer.onData((data: string) => {
        if (this._heldRaw !== null) {
          this._heldRaw.push(data)
          return
        }
        this.session?.send(data)
      })
      session.onExit((sid: string) => {
        log.info('nocx: session exited', { sid })
        // The session is gone: an origin naming it would name a machine
        // that no longer exists (B.9).
        this._sessionExited = true
        this.hooks.onActiveOriginChange?.()
        this.lifecycle.reset()
        this._disposeAllMarkers()
        host.requestClose()
      })
      session.onInputStalled(() => {
        // The backend is dropping what this tab sends. Say so: a terminal
        // that swallows keystrokes in silence is indistinguishable from one
        // that is simply ignoring the person at it (nocx-o2le).
        log.warn('nocx: session input stalled', { sid: session.sessionId })
        showToast({
          level: 'danger',
          message: 'This connection has stopped accepting input — keystrokes are being dropped.',
        })
      })
      session.onReset(() => {
        renderer.reset()
        this.lifecycle.reset()
        this._disposeAllMarkers()
      })

      renderer.onTitle((title: string) => {
        // An OSC 0/2 title is attributed to the ACTIVE domain (the stream
        // has no writer identity; the domain that was active when the bytes
        // arrived is the only honest attribution) and the projection's
        // change callback re-composes the tab title.
        this.env?.recordTitle(title.trim())
      })
      renderer.onCwd(({ path }) => {
        // An OSC 7 report is the shell's verified claim of where it is:
        // the one cwd the composition layer may hand to files.open (D2).
        // Scoped to the ACTIVE domain — a child's report never touches the
        // parent's record, and the parent's values return only on the
        // parent's authenticated activation (bead nocx-u7uh.11).
        this.env?.recordCwd(path)
      })
      renderer.onBell(() => {
        host.requestAttention()
      })
      // ── Clipboard ────────────────────────────────────────────────────
      renderer.onSelectionChange((text) => {
        if (shouldCopy(text)) {
          this.clipboard.writeText(text).catch((e) => {
            console.warn('nocx: clipboard write failed (selection)', e)
          })
        }
      })

      renderer.onClipboardWrite((text) => {
        if (this.gate.granted) {
          this.clipboard.writeText(text).catch((e) => {
            console.warn('nocx: clipboard write failed (OSC 52)', e)
          })
          return
        }
        if (this.gate.suppressed) return
        if (this.banner.shown) return
        void this.banner.show().then((choice) => {
          if (choice === 'allow') {
            this.gate.allow()
            this.clipboard.writeText(text).catch((e) => {
              console.warn('nocx: clipboard write failed (OSC 52)', e)
            })
          } else if (choice === 'suppress') {
            this.gate.suppress()
          }
        })
      })

      // Paste on right-click AND middle-click.
      const doPaste = async () => {
        try {
          const text = await this.clipboard.readText()
          if (!text) return
          if (this.editor?.isVisible) {
            this.editor.insertText(text)
            return
          }
          if (text.includes('\n') && this._bufferType === 'normal') {
            const confirmed = await showConfirm('Paste multi-line text?', 'Paste', 'Cancel')
            if (!confirmed) return
          }
          renderer.paste(text)
        } catch (e) {
          console.warn('nocx: clipboard read failed (paste)', e)
        }
      }

      target.addEventListener('contextmenu', (e: MouseEvent) => {
        e.preventDefault()
        void doPaste()
      })

      target.addEventListener('mousedown', (e: MouseEvent) => {
        if (e.button === 1) {
          e.preventDefault()
          void doPaste()
        }
      })

      renderer.onResize((cols: number, rows: number) => {
        if (cols === this.cols && rows === this.rows) return
        this.cols = cols
        this.rows = rows
        clearTimeout(this.resizeTimer)
        this.resizeTimer = window.setTimeout(() => {
          // A resize makes the shell redraw its prompt, and that redraw arrives
          // on `session.onData` looking exactly like output the user has not
          // seen. It is not: we asked for it. Switching the strip from vertical
          // to horizontal resizes every pane at once, so every inactive tab lit
          // its activity indicator for something the user did to the WINDOW
          // rather than to any tab (nocx-6w4z).
          this.echoUntil = Date.now() + RESIZE_ECHO_MS
          session.sendResize(cols, rows)
        }, RESIZE_SETTLE_MS)
      })

      this.renderer = renderer

      // ── Native-mode escape (Ctrl/Cmd+Shift+.) ─────────────────────────
      target.addEventListener('keydown', (e: KeyboardEvent) => {
        if (e.key === '.' && (e.metaKey || e.ctrlKey) && e.shiftKey) {
          e.preventDefault()
          e.stopPropagation()
          this.enterNativeMode()
        }
      })

      this._mounted = true
      // The reference-chip seam: a document selection inside a finished
      // block's output raises a chip (nocx-4wtlh). Registered at the end
      // of mount so the editor and the scrollback exist; removed on
      // dispose.
      document.addEventListener('selectionchange', this.onSelectionChange)
      this._readyResolve(true)
      log.info('nocx: terminal content ready', {
        renderer: 'xterm',
        sid: session.sessionId,
      })

      // B.5: replay the latest viewport after async mount completes.
      // The presentation layer delivers viewports via viewportChanged;
      // if one was buffered during mount, apply it now through the
      // renderer's fitViewport path.
      if (this._latestViewport) {
        this.viewportChanged(this._latestViewport)
      }
    } catch (err) {
      // Vault-sealed errors should surface as Unlock dialog, not generic error.
      if (err instanceof RpcError) {
        const data = err.data as { reason?: string } | undefined
        if (data?.reason === 'vault-sealed') {
          this.hooks.onVaultSealed?.()
          this._readyResolve(false)
          return
        }
      }
      const notice = document.createElement('pre')
      notice.className = 'pane-error'
      notice.textContent = `Terminal failed to start:\n\n${err instanceof Error ? err.message : String(err)}`
      target.replaceChildren(notice)
      this._readyResolve(false)
      log.error('nocx: terminal content failed', { error: String(err) })
    }
  }

  // ── Live-region sizing ────────────────────────────────────────────────

  private liveResizeFrame = 0

  /**
   * Re-measure the live region on the next frame.
   *
   * Coalesced to one animation frame because a busy command delivers dozens of
   * chunks per frame and every one of them would otherwise read the grid and
   * write a style — a layout thrash on the hot path, for a height that can only
   * be painted once per frame anyway.
   */
  private scheduleLiveResize(): void {
    if (this.liveResizeFrame !== 0) return
    this.liveResizeFrame = requestAnimationFrame(() => {
      this.liveResizeFrame = 0
      if (this._disposed || !this.renderer || !this.scrollback) return
      // Height first, refit second, and the order is the whole point. Reaching
      // the ceiling collapses the editor, which grows the scroller — so the
      // usable height is only correct AFTER this call. Refitting first meant
      // the grid stayed at the old size until the next chunk of output arrived,
      // and `top` refreshes every three seconds: the pane visibly re-laid
      // itself several seconds after the program started.
      this.scrollback.setLiveHeight(this.renderer.liveContentHeight())
      this.refitIfResized()
    })
  }

  // ── B.5 viewport delivery ─────────────────────────────────────────────

  private _latestViewport: ContentViewport | null = null
  private _mounted = false

  viewportChanged(viewport: ContentViewport): void {
    if (this._disposed) return
    this._latestViewport = viewport
    // Pass the authoritative viewport to the renderer (B.5).
    // The renderer computes cols/rows from its own cell metrics.
    if (this._mounted && this.renderer) {
      this.renderer.fitViewport(this.usableViewport(viewport))
    }
  }

  /**
   * The delivered viewport, less the chrome the grid can never be shown in.
   *
   * B.5 says this class does not interpret container geometry, and it still
   * does not: the pane's box is handed to it. What it subtracts is its OWN
   * furniture — the editor is a flex sibling inside the pane, so the scroller
   * that displays the grid is shorter than the pane by exactly the editor's
   * height.
   *
   * Measured while `top` ran: the pane was 682px, the editor 76, the scroller
   * 606 — and the grid had been fitted to the full 682, producing a 665px
   * screen. `top` filled all of its rows and the bottom four had nowhere to be
   * drawn. Clamping the live region cannot fix that; it only decides where the
   * clipping happens. The grid has to be the size of the space it is shown in
   * (nocx-6w4z).
   *
   * The width is the same statement one axis over, and it was left unmade until
   * nocx-vydj. The delivered width is `pane.getBoundingClientRect().width`, a
   * BORDER box — it counts the `padding: 0 10px` on `.pane`, which is breathing
   * room around the text and not space the grid may use. `cols` was therefore
   * computed from 20px that do not exist, and the last columns were laid out
   * past the right edge of `.xterm-inner`, whose `overflow: hidden` cut them
   * mid-glyph.
   *
   * That it read as a Wails-only defect is the scrollbar gutter: measured at a
   * 1232px pane, `.scrollback-area` is 1212 wide in both engines, but its
   * clientWidth is 1202 in Chromium and 1212 in WebKit, because
   * `scrollbar-gutter: stable` reserves in one and is ignored by the other. Same
   * build, same grid, two different overhangs — 20px in a browser, 10 in
   * WKWebView. Neither is correct, and subtracting a constant for the padding
   * would have fixed only the browser.
   *
   * `clientWidth` of the scroller answers both at once: it is the content box,
   * so the pane's padding is already gone, and it excludes the scrollbar
   * whether or not the engine reserved one.
   */
  private lastFitHeight = 0

  /**
   * Re-fit the grid when the space it is shown in has changed size.
   *
   * `viewportChanged` only fires when the PANE's geometry changes, and the
   * things that resize the grid's home are inside the pane: the editor
   * appearing, and the editor being taken away again when a program fills the
   * pane. Neither is a change the pane itself ever sees. The very first fit therefore ran while the
   * editor was still `display: none`, took the whole pane, and was never
   * revisited: 682px of grid living in a 606px scroller, four rows permanently
   * below the fold.
   *
   * No loop: fitting changes the row count, which changes the PTY size, which
   * produces output, which lands back here — and the usable height is the same,
   * so nothing refits.
   */
  private refitIfResized(): void {
    const v = this._latestViewport
    if (!v || !this.renderer) return
    const usable = this.usableViewport(v)
    if (usable.height === this.lastFitHeight) return
    this.lastFitHeight = usable.height
    this.renderer.fitViewport(usable)
  }

  private usableViewport(viewport: ContentViewport): ContentViewport {
    const area = this.scrollback?.scrollbackArea
    // Zero before first layout — the delivered box is the better guess then,
    // and the next viewport delivery corrects it. Each axis falls back on its
    // own: jsdom reports 0 for both, a real pane mid-layout can report one.
    //
    // While a command runs, the height is the live region's CAP, not the bare
    // scroller: setLiveHeight clamps the live box to scroller minus the
    // running block's header, and a grid fitted to the full scroller is taller
    // than the box that displays it — its last rows are clipped by the box's
    // overflow and nothing scrolls them, so the bottom of a tall inline TUI
    // (its composer) is unreachable (nocx-zn4d). Fitting the grid to the same
    // cap makes the box and the grid agree; output past the grid goes into
    // xterm's own scrollback and is reachable through its viewport. Outside
    // `running` the cap is null and the delivered/scroller height applies.
    const cap = this.scrollback?.runningLiveCap
    const height = cap ?? (area && area.clientHeight > 0 ? area.clientHeight : viewport.height)
    const width = area && area.clientWidth > 0 ? area.clientWidth : viewport.width
    return { ...viewport, width, height }
  }

  /**
   * Focus whichever surface owns input right now.
   *
   * At the prompt that is the editor, and the grid is deliberately read-only
   * while the editor is up (`setReadOnly(true)` on the lifecycle change). So
   * focusing the renderer unconditionally parked the caret in a widget that
   * drops every keystroke — and neither focus-bounce path rescues it, because
   * both stand down when the focus is already inside the live xterm container,
   * which is exactly where `renderer.focus()` puts it.
   *
   * This is why a freshly created tab typed fine and a tab you switched back to
   * did not: the new tab's `editor.show()` focuses its own textarea, while
   * `TabManager.activate()` ends with `tab.focus()` and took that focus away
   * again on every return.
   */
  focus(): void {
    if (this.editor?.isVisible) {
      this.editor.focus()
      return
    }
    this.renderer?.focus()
  }

  /**
   * A pane is hidden with a CSS class rather than unmounted, so while it is
   * hidden the WebGL texture atlas behind it goes stale and its glyphs come
   * back drawn from coordinates that have moved (nocx-e27). Repaint on the
   * way in.
   *
   * The repaint hangs off setVisible rather than off an activation call in
   * TabManager because visibility is owned here: the seam refactor 21fd7f6a
   * carried the method across and left `tab.refreshAtlas()` behind, and the
   * fix sat unreachable until the corruption was reported again (nocx-jfgb).
   * A caller that has to remember is a caller that forgets.
   */
  setVisible(visible: boolean): void {
    super.setVisible(visible)
    if (visible) this.renderer?.refreshAtlas()
  }

  // ── Capability rail (nocx-4t37.2) ─────────────────────────────────────
  // The pane-level rail above the pending command: one chip stating what is
  // true right now (native input / command blocks / enhanced input), one
  // popover of actions opened from it. The rail is NOT tab chrome — the
  // capability changes several times INSIDE one tab (ssh from inside ssh,
  // docker exec, sudo, a TUI), and it matters exactly where Enter is about
  // to be pressed.

  /** Derive the three axes, resolve authorisation + eligibility, and
   *  update the editor's recovery chip (nocx-atyf.2). The shell state is
   *  the KERNEL's word (ADR-0024 §6, the projection bead nocx-u7uh.7): a
   *  live authenticated domain is 'integrated', Native is 'unsupported', a
   *  lost or desynchronized domain is 'lost'. The input presentation is a
   *  presentation fact — what the user sees. Nothing stream-derived reaches
   *  either axis. */
  private _updateCapability(): void {
    const shellState: ShellState = shellStateFromLifecycle(
      this.lifecycle.state,
      this.lifecycle.domainStack,
    )
    const presentation: InputPresentation = this.editor?.isVisible ? 'editor' : 'terminal'
    // The open-ack integration decline is still worth the tab's warning
    // mark: it is the backend's own fact about this session, not a
    // stream-derived claim.
    const degraded = isDegraded(this._integration)
    const label = integrationMessage(this._integration)?.title ?? ''
    if (
      shellState === this._shellState &&
      presentation === this._presentation &&
      degraded === this._degraded &&
      label === this._degradedLabel
    )
      return
    this._shellState = shellState
    this._presentation = presentation
    this._degraded = degraded
    this._degradedLabel = label
    // The mark carries the reason's own wording (nocx-5uu5): one table
    // decides what a reason is called, and every surface that shows one —
    // this mark, the card, the details dialog — reads it. The label is part
    // of the dedupe above, because conventional → lost changes what the
    // mark means without changing whether it is showing.
    this.hooks.onWarningChange?.(degraded, label)
    this._renderRecovery()
  }

  /** Apply one published integration status (nocx-dvql / nocx-5uu5).
   *
   *  Two surfaces, and they are deliberately different in kind. The tab's
   *  mark is a STATE: it is on for exactly as long as the session stays
   *  degraded, so a user who looks at the strip an hour later still sees it.
   *  The card is an EVENT, raised once per session per reason and answered by
   *  the user or not at all — the only thing that outlives the session is the
   *  user pressing "Don't show again for this shell" (nocx-wfxz). Closing a
   *  card remembers nothing beyond this tab, because a card closed before the
   *  reader worked out what it meant has not been read.
   *
   *  A session that never asked for integration never receives a status at
   *  all, so neither surface has anything to draw: absence is how
   *  "conventional by design" is expressed, and there is nothing here that
   *  needs to special-case it. */
  private _applyIntegration(fact: SessionIntegrationChanged): void {
    if (this._disposed) return
    this._integration = fact
    this._updateCapability()
    if (!isDegraded(fact)) {
      // Recovered, or never failed. The card belongs to the state that
      // raised it and goes with it — and the terminal gets the space back,
      // so the live region is re-measured on the way out too.
      this._dropIntegrationNotice()
      return
    }
    this._maybeShowIntegrationNotice(fact)
  }

  /** Take the card down and give the pane back to the terminal. */
  private _dropIntegrationNotice(): void {
    if (!this._noticeDispose) return
    this._noticeDispose()
    this._noticeDispose = null
    this.scheduleLiveResize()
  }

  /** Raise the degraded-session card, unless the user has already answered
   *  for this shell — or closed this very card in this tab.
   *
   *  Nothing is written here. The card being drawn used to record the (shell,
   *  reason) pair on this machine, which meant a glance spent it and the next
   *  session with the same shell and the same reason said nothing at all
   *  (nocx-wfxz). The only persistent record is the one the user asks for. */
  private _maybeShowIntegrationNotice(fact: SessionIntegrationChanged): void {
    const target = this._paneTarget
    const reason = fact.reason ?? 'unknown'
    if (!target || this._noticeDispose) return
    if (this._closedReasons.has(reason)) return
    if (this._silencedShells.isSilenced(fact.shell)) return
    this._noticeDispose = mountIntegrationNotice(target, {
      fact,
      copy: (text) => this.clipboard.writeText(text),
      onSuppressShell: () => {
        this._silencedShells.silenceShell(fact.shell)
        this._closedReasons.add(reason)
        this._dropIntegrationNotice()
      },
      onDismiss: () => {
        this._closedReasons.add(reason)
        this._dropIntegrationNotice()
      },
    })
    // The card takes its height off the top of the pane (nocx-rzvq), so the
    // scroller is smaller than the grid fitted to it. Same re-measure the
    // editor appearing and disappearing already goes through — the pane's
    // own box is unchanged, so the viewport observer never fires for this.
    this.scheduleLiveResize()
  }

  /** Acknowledge the restoration (ADR-0024 decision 8) once the shell's
   *  one-shot recovery fence matched and the conventional presentation is
   *  applied. The acknowledgement is deliberately narrow — session identity
   *  and the generation; the backend validates it against the episode it
   *  opened, and the transition permits only Lost → Native. Cleared on
   *  success; a refusal (session died, episode superseded) is a fact about
   *  the session, and the exit path closes the tab. */
  private async _ackRecovery(): Promise<void> {
    if (!this._recovery || this._recoveryAcking || !this.session || this._sessionExited) return
    const rec = this._recovery
    this._recoveryAcking = true // claim the episode: exactly one ack per fence
    try {
      await new LifecycleClient(this.client.dispatcher).recoverAck(
        this.session.sessionId,
        rec.generation,
      )
      this._recovery = null
    } catch (e) {
      // A refusal is a fact about the episode (session died, generation
      // superseded, lane no longer lost): the pending guard stays, and the
      // exit path closes the tab. Re-arming lets a replayed episode retry.
      log.warn('nocx: recovery acknowledgement refused', { error: e })
      this._recoveryAcking = false
    }
  }

  /** Update the editor's recovery chip: the single action when one exists,
   *  hidden when none apply. The chip IS the action — one click, no
   *  popover (nocx-atyf.2). */
  private _renderRecovery(): void {
    if (!this.editor) return
    // ADR-0024 decision 8, the interval: while a restoration episode is
    // pending — the channel died and nocx has not yet matched the shell's
    // recovery fence — the session is neither an authenticated terminal nor
    // advertised as a usable conventional one, and no editor is offered at
    // any point inside the span.
    if (this._recovery) {
      this.editor.setRecoveryAction(null, () => {})
      return
    }
    const eligible = this.lifecycle.buffer === 'normal'
    const authorized = this._policy !== 'raw'
    const actions = deriveActions({
      shellState: this._shellState,
      presentation: this._presentation,
      // The renderer cannot tell bootstrap from installed delivery without
      // an accepted observation (the passport that used to carry it is
      // deleted); markers present means the launcher delivered by one of
      // the script carriers.
      observedDelivery: 'none',
      authorized,
      eligible,
    })
    if (actions.length === 0) {
      this.editor.setRecoveryAction(null, () => {})
      return
    }
    const action = actions[0]
    // SEVERED (ADR-0024 §4): the integrate/retry actions are deleted with
    // the in-band tier; the surviving editor-presentation actions both
    // restore the editor presentation.
    this.editor.setRecoveryAction(action.label, () => this._restoreEditor())
  }
  private _restoreEditor(): void {
    this.nativeMode = false
    // The kernel is the authority; the sync shows the editor because the
    // axis says PromptReady — and the user's own escape latch is now off.
    this._syncLifecycleOwnership()
    this._updateCapability()
  }

  /** The current input presentation, exposed for context menu and palette
   *  (nocx-atyf.5). 'editor' means the nocx command editor is active;
   *  'terminal' means conventional terminal input is in use. */
  get presentation(): InputPresentation {
    return this._presentation
  }

  /** The observed shell state, exposed for the e2e and unit seams. */
  get shellState(): ShellState {
    return this._shellState
  }

  /** Switch to terminal input for this session (nocx-atyf.5). The escape
   *  hatch — the editor hides, keys route raw, and the prompt is restored.
   *  Session-scoped; a new session starts with the default. */
  switchToTerminalInput(): void {
    if (this.nativeMode) return
    this.enterNativeMode()
  }

  /** Switch back to the nocx command editor (nocx-atyf.5). Only works when
   *  the shell is integrated and the prompt is trusted. */
  switchToEditorInput(): void {
    if (!this.nativeMode) return
    this._restoreEditor()
  }

  get policy(): DesiredMode {
    return this._policy
  }

  /** Ask the user once whether to enable the command editor for this
   *  destination (nocx-atyf.3). The answer is remembered per destination
   *  so later transitions are silent. */

  /** The native-mode escape (ADR-0004 §1, nocx-4ff.9): latch native input —
   *  the editor never shows again this session, keys route raw, and the
   *  remote prompt is restored to visible (the one-way guarantee that the
   *  user is never trapped). This is the capability popover's "Use native
   *  input" action AND the Ctrl/Cmd+Shift+. chord — one path, not two.
   *
   *  One-way on purpose: the remote marker-only prompt contract stops
   *  emitting markers when it is unset (zsh removes its precmd hook; bash
   *  stops the A marker), so a fresh integration — not a presentation
   *  toggle — is what returns to command blocks. The capability chip still
   *  states "Native input" and the popover offers nothing while latched. */
  /** Insert a vault secret where the user is actually typing (nocx-fk32).
   *  The target is chosen by who owns input, and the difference is the
   *  point:
   *
   *  - The EDITOR owns the prompt: the REFERENCE goes into the draft, never
   *    the value. That is ADR-0021's whole argument — a command carrying a
   *    reference moves to another machine and resolves that machine's
   *    secret, while a command carrying a pasted key is both dead and
   *    dangerous — and the draft already renders it as the secret chip.
   *    Nothing resolves until submit.
   *  - The TERMINAL owns input (a password prompt, an ssh handshake, sudo,
   *    mysql -p): there is no reference machinery on the other side, so the
   *    value is resolved and written to the pty. vault.resolveLine exists
   *    for exactly this — 'the resolved value goes to the caller for the
   *    PTY write and nowhere else'. The newline goes WITH it (owner,
   *    2026-08-10): picking a secret at a password prompt IS the answer to
   *    that prompt, and making the user press Enter afterwards adds a step
   *    to every use to guard a case — the pane not being at a prompt at all
   *    — that the user ruled out the moment they opened the picker.
   *
   *  Nothing here reads the byte stream to decide anything (AD-6): the
   *  question 'who owns input' is answered by the input presentation, which
   *  the lifecycle axis owns. Returns what happened, so the caller can say
   *  so — a password prompt echoes nothing, and an insert the user cannot
   *  see is an insert they will do twice. */
  async insertSecret(name: string): Promise<'reference' | 'value' | 'unavailable'> {
    if (this.editor?.isVisible === true) {
      this.editor.insertText(secretReference(name))
      return 'reference'
    }
    if (!this.session || !this.vault) return 'unavailable'
    const resolved = await this.vault.resolveLine(secretReference(name))
    // An unresolved name must never be written as literal text: the far
    // side would receive `{{secret:…}}` as the password. resolveLine reports
    // every reference it could not resolve, and this line has exactly one.
    if (resolved.refs.some((r) => !r.resolved)) return 'unavailable'
    this.session.send(resolved.line + '\n')
    return 'value'
  }

  /** The grid becomes the keyboard's owner: writable AND focused, in one
   *  step. Both halves or neither — that is the whole rule, and it is here
   *  rather than at each call site because it was split once and the split
   *  is what nocx-yb5y was.
   *
   *  The editor gives the keyboard up SYNCHRONOUSLY at commit (clearDoc →
   *  hide), and a display:none host drops the browser's focus to <body>. So
   *  every path that hides the editor has to hand the keyboard on in the
   *  same step. `setReadOnly(false)` alone did that for writability and left
   *  focus to ride along with the deferred paste — which at a live prompt
   *  waits on the lifecycle.submitAttempt round trip. For that whole round
   *  trip nobody owned the keyboard: keys reached <body> and were gone, with
   *  no editor to show them and no grid to send them. The user typing into a
   *  program already reading stdin lost the letters and kept the Enter, so an
   *  ssh password prompt was answered with an empty line, silently.
   *
   *  Idempotent, deliberately: the deferred `focusGrid` runs this again after
   *  the round trip, and it must be the SAME operation rather than a second
   *  rule about who owns input (AD-8). */
  private takeKeyboardToGrid(): void {
    this.renderer?.setReadOnly(false)
    this.renderer?.focus()
  }

  /** Raw bytes the grid produced after the keyboard changed hands but before
   *  the submitted command reached the pty — null when nothing is in flight,
   *  which is the ordinary state.
   *
   *  ADR-0024 §5 puts the lifecycle.submitAttempt round trip BETWEEN the
   *  editor's commit and the pty write, and that is deliberate: the attempt
   *  must open before the bytes that can cause the shell's own start. The
   *  keyboard, meanwhile, changes hands synchronously at commit — it has to,
   *  or the keys typed into the gap reach nobody. So there is a real window
   *  in which the grid owns the keyboard and the command it follows has not
   *  been sent, and bytes crossing it would arrive at the pty AHEAD of the
   *  command. Measured: `hello\r` reaching the shell before `read x; …\r`,
   *  which runs `hello` as a command and leaves `read` waiting for a line
   *  nobody typed. */
  private _heldRaw: string[] | null = null

  /** Start holding: from here until releaseHeldRaw, the grid's bytes queue.
   *  A second submit inside the window keeps the existing queue — the order
   *  is the invariant, and re-arming would publish the earlier one twice. */
  private holdRawUntilSubmitted(): void {
    this._heldRaw ??= []
  }

  /** Stop holding and hand back what was held, for the caller to send after
   *  the command. Detaching and flushing are two steps on purpose — the
   *  command's own bytes travel between them (see `write`).
   *
   *  Nothing bounds this window with a timer, and nothing should: the write
   *  is attempted on BOTH settlements of the attempt RPC (`.then(write,
   *  write)`), and the dispatcher rejects every pending call when the socket
   *  closes. The only way to stay held is a live socket whose backend never
   *  answers — where the queued bytes had nowhere to go either, and which
   *  the session's own input-stalled warning is what reports. */
  private takeHeldRaw(): string[] {
    const held = this._heldRaw ?? []
    this._heldRaw = null
    return held
  }

  private enterNativeMode(): void {
    this.nativeMode = true
    this.editor?.hide()
    this.takeKeyboardToGrid()
    this.session?.send(NATIVE_RESTORE)
    this._updateCapability()
  }

  /** The editor owns keys because the lifecycle axis says PromptReady
   *  (ADR-0024 §6): shouldShowEditor reads the axis, and the buffer axis
   *  gates presentation only — it can never restore authority. While the
   *  editor is up the grid is read-only, so keys land in the composed
   *  command, not the (disabled) grid. The native escape is a presentation
   *  latch, not an authority: the user's own choice keeps the editor hidden
   *  even at a kernel-authenticated prompt (the input-router projection),
   *  and only a fresh integration or an explicit switch back shows it. */
  private _syncLifecycleOwnership(): void {
    const editor = this.editor
    if (editor === null) return
    const show =
      shouldShowEditor(this.lifecycle.state) &&
      this.lifecycle.buffer === 'normal' &&
      !this.nativeMode
    // The grid's writability follows ownership, not the visibility
    // transition: the editor hides ITSELF at submit (the atomic handoff),
    // so by the time the running fact lands `editor.isVisible` is already
    // false and a transition-gated setReadOnly(false) never runs — the grid
    // stays read-only, typed input is dropped, and a program waiting on
    // stdin (read, ssh, less) hangs with no editor and no input surface
    // (nocx-u7uh.23). The invariant: editor shown ⟺ grid read-only.
    if (show) {
      if (!editor.isVisible) editor.show()
      this.renderer?.setReadOnly(true)
    } else {
      if (editor.isVisible) editor.hide()
      this.renderer?.setReadOnly(false)
    }
  }

  /** A document selection landed (or moved): if it is a real selection
   *  inside one FINISHED block's output, freeze it into a reference chip.
   *  Nothing else happens — the active target does not move, the shell is
   *  not armed, the selection itself is untouched (copy keeps working).
   *  Reselecting the identical region (same block, same rows) is a no-op;
   *  a selection inside the editor's own draft is never a reference.
   *  selectionchange fires on every caret move, so the guard is the
   *  fingerprint, not the event. */
  private raiseChipFromSelection(): void {
    const sel = window.getSelection()
    if (!sel || sel.isCollapsed) return
    const anchor = sel.anchorNode
    if (anchor && anchor.parentElement?.closest('.nocx-editor')) return
    const chip = chipFromSelection(sel)
    if (!chip) return
    const existing = this.referenceChips.find((c) => chipFingerprint(c) === chipFingerprint(chip))
    if (existing) return
    const label = this.referenceChipLabel(chip)
    this.referenceChips = [
      ...this.referenceChips,
      {
        id: `ref-${(this._chipSeq = (this._chipSeq ?? 0) + 1)}`,
        label,
        blockEl: chip.blockEl,
        rowStart: chip.rowStart,
        rowEnd: chip.rowEnd,
      },
    ]
    this.editor?.setReferenceChips(this.referenceChips)
  }

  /** The chip's name: the block's command and the covered row range —
   *  the block names itself, the rows say what part is frozen. */
  private referenceChipLabel(chip: {
    blockEl: HTMLElement
    rowStart: number
    rowEnd: number
  }): string {
    const header = chip.blockEl.querySelector<HTMLElement>('.cmd-header-text')
    const name = header?.textContent?.trim() || 'block'
    const rows =
      chip.rowEnd - chip.rowStart === 1
        ? `row ${chip.rowStart + 1}`
        : `rows ${chip.rowStart + 1}–${chip.rowEnd}`
    return `${name} · ${rows}`
  }

  /** Drop every reference chip: a question sent to Ask consumed them, or
   *  a `clear` took their blocks. The editor's strip follows. */
  private clearReferenceChips(): void {
    if (this.referenceChips.length === 0) return
    this.referenceChips = []
    this.editor?.clearReferenceChips()
  }

  dispose(): void {
    this._disposed = true
    this.mountAbortController?.abort()
    this._lifecycleUnsub?.()
    this._lifecycleUnsub = null
    this._integrationUnsub?.()
    this._integrationUnsub = null
    this._noticeDispose?.()
    this._noticeDispose = null
    this._lifecycleChangeUnsub?.()
    this._lifecycleChangeUnsub = null
    this._projections?.detach()
    this._projections = null
    this.env?.detach()
    this.env = null
    if (this._globalKeydown) {
      document.removeEventListener('keydown', this._globalKeydown)
      this._globalKeydown = null
    }
    this.session?.close()
    this.renderer?.dispose()
    this.editor?.dispose()
    this.recall?.destroy()
    this.recall = null
    this.scrollback?.dispose()
    this.destroyReceipt()
    this.clearReferenceChips()
    document.removeEventListener('selectionchange', this.onSelectionChange)
    this.indicator = null
    this.promptVault?.destroy()
    this.promptVault = null
    this.completion?.destroy()
    this.completion = null
    this._disposeAllMarkers()
    this.ledger = null
    this.host = null
  }

  private _disposeAllMarkers(): void {
    for (const m of this._markers.values()) m.dispose()
    this._markers.clear()
  }

  // ── the after-submit receipt (ADR-0021, the receipt round) ──────────────

  /** The history.record ack crossed: paint the block with what was stored
   *  and, when captures came back, attach the receipt to THAT block. The
   *  block identity was captured at onComplete time; a block that is gone
   *  by now (cleared scrollback, disposed tab, or never frozen) drops the
   *  receipt silently. */
  private attachRecordedAck(
    _recId: number,
    block: BlockRecord | null | undefined,
    ack: HistoryRecord,
  ): void {
    if (!block) return
    const blockEl = block.el
    // A disconnected element means the scrollback was cleared or the tab
    // disposed; the capture died with them on the backend anyway, so the
    // receipt goes with it.
    if (!blockEl.isConnected || !blockEl.classList.contains('cmd-block')) return
    // Still logically running: the completion has not landed for this
    // record at all, and nothing here can attach to a block that has not
    // finished.
    if (block.status === 'running') return
    // FINISHED, BUT NOT YET REDRAWN. This used to read the DOM class, which
    // is a different question and the wrong one: the logical freeze lands on
    // the authenticated completion while the visual freeze waits up to
    // FENCE_DEFER_MS for the fence bytes, and until it runs the element
    // still says cmd-block-running. So a block that had finished perfectly
    // well was refused, and the receipt was dropped in silence — no retry,
    // nothing in the UI, nothing in the log. For a user: run a command
    // carrying a key, have the backend capture it, and watch nothing offer
    // to save it. It surfaced as one webkit e2e failure in five CI runs,
    // where a cold first render widened the window enough for the ack to
    // land inside it (nocx-ggha).
    //
    // Parked rather than applied, because the visual freeze REPLACES el and
    // would discard anything written now. `_freezeVisual` runs this the
    // instant the boundary lands, and the re-entry passes the check above.
    if (blockEl.classList.contains('cmd-block-running')) {
      block.afterVisualFreeze = () => this.attachRecordedAck(_recId, block, ack)
      return
    }
    if (ack.redactions.length > 0) {
      renderRecordedCommand(blockEl, ack.maskedCommand, ack.redactions)
    }
    if (ack.captures.length === 0) return
    // One receipt per block: a re-recorded block replaces its own, never
    // anybody else's.
    this.receipts.get(blockEl)?.destroy()
    this.receiptBlockEl = blockEl
    for (const c of ack.captures) {
      this.receiptChipSpans.set(c.id, { start: c.redaction.start, end: c.redaction.end })
      this.receiptChipBlocks.set(c.id, blockEl)
    }
    const receipt = new BlockReceipt(
      ack.captures.map((c) => ({
        captureId: c.id,
        kindLabel: KIND_LABELS[c.redaction.kind],
        maskedValue:
          c.redaction.prefix !== '' || c.redaction.suffix !== ''
            ? `${c.redaction.prefix}...${c.redaction.suffix}`
            : '***',
        suggestedName: c.suggestedName,
      })),
      {
        onSaveAll: (rows) => void this.saveReceiptRows(receipt, blockEl, rows),
        onDismiss: (captureId) => void this.dismissReceiptRow(receipt, blockEl, captureId),
        onHover: (captureId) => this.emphasiseChip(captureId),
        onExitReview: () => this.editor?.focus(),
      },
    )
    this.receipts.set(blockEl, receipt)
    this.receipt = receipt
    receipt.mount(blockEl)
  }

  /** A receipt is finished with (every row saved or dismissed): forget it,
   *  and hand `receipt` to whichever one is still open, newest first. */
  private retireReceipt(blockEl: HTMLElement): void {
    this.receipts.delete(blockEl)
    if (this.receiptBlockEl === blockEl) this.receiptBlockEl = null
    let newest: BlockReceipt | null = null
    let newestEl: HTMLElement | null = null
    for (const [el, r] of this.receipts) {
      newest = r
      newestEl = el
    }
    this.receipt = newest
    if (this.receiptBlockEl === null) this.receiptBlockEl = newestEl
  }

  /** Tear every receipt down — the tab is going away, and the backend
   *  destroys its captures on the same event. */
  private destroyReceipt(): void {
    for (const r of this.receipts.values()) r.destroy()
    this.receipts.clear()
    this.receipt = null
    this.receiptBlockEl = null
    this.receiptChipSpans.clear()
    this.receiptChipBlocks.clear()
  }

  /** The receipt's primary action: settle every row still in play. The
   *  capture id is the idempotency key — a partial failure keeps the row
   *  and a retry of the SAME id finishes the owed rewrite without minting
   *  a second secret. The name shown is the RESPONSE's, never the one
   *  sent. */
  private async saveReceiptRows(
    receipt: BlockReceipt,
    blockEl: HTMLElement,
    rows: ReadonlyArray<{ captureId: string; name: string }>,
  ): Promise<void> {
    // Saving into a vault that does not exist yet cannot work, and the
    // receipt is the moment the person actually wants one — sending them to
    // Settings to come back afterwards guarantees they will not, and the
    // capture dies meanwhile. So Save sets the vault up first: silently when
    // the machine has an OS key, otherwise through the vault layer's own
    // setup dialog, which then owns the rest of the flow.
    if (!(await this.ensureVaultForSave())) return
    for (const row of rows) {
      if (!this.vault) continue
      try {
        const res = await this.vault.captureSave({ captureId: row.captureId, name: row.name })
        if (res.partial) {
          showToast({
            level: 'warning',
            message: `"${res.name}" saved, but the history rewrite is still owed — retry to finish it.`,
          })
          receipt.markFailed(
            row.captureId,
            `"${res.name}" is saved — the history rewrite is still owed; retry to finish it`,
          )
          continue
        }
        showToast({ level: 'success', message: `Stored "${res.name}" in the vault.` })
        if (receipt.removeRow(row.captureId)) this.retireReceipt(blockEl)
      } catch (err) {
        // A capture the backend no longer holds cannot be retried, so the
        // row must go rather than sit there offering an action that will
        // fail every time. Anything else is worth another attempt.
        if (isCaptureGone(err)) {
          showToast({
            level: 'warning',
            message: 'This offer is no longer held — the command stays stored masked.',
          })
          if (receipt.removeRow(row.captureId)) this.retireReceipt(blockEl)
          continue
        }
        showToast({
          level: 'danger',
          message: 'Could not save the secret — the command stays stored masked.',
        })
        receipt.markFailed(row.captureId, 'could not save — try again')
      }
    }
  }

  /** Make the vault able to receive a secret, or say why it cannot.
   *
   *  Returns true when the save may proceed. False means the flow moved
   *  somewhere else — the setup dialog is up, or the vault is locked — and
   *  the receipt stays as it is, so the same Save finishes the job once the
   *  person comes back. */
  private async ensureVaultForSave(): Promise<boolean> {
    if (!this.vault) return false
    let status
    try {
      status = await this.vault.status()
    } catch {
      return true // let the save itself report the real failure
    }
    if (status.state === 'unsealed') return true
    if (status.state === 'sealed') {
      showToast({ level: 'warning', message: 'Unlock the vault to save this key.' })
      return false
    }
    if (status.osKeyCapable) {
      try {
        await this.vault.setup({})
        return true
      } catch {
        showToast({
          level: 'danger',
          message: 'Could not set the vault up — the key was not saved.',
        })
        return false
      }
    }
    // No OS key: setting up needs a passphrase, and a passphrase needs a
    // dialog the vault layer owns. The receipt survives it.
    this.hooks.onSetupVault?.()
    return false
  }

  /** A row's drop control: dismiss that capture (and suppress its
   *  fingerprint for the session). A failed dismiss keeps the row. */
  private async dismissReceiptRow(
    receipt: BlockReceipt,
    blockEl: HTMLElement,
    captureId: string,
  ): Promise<void> {
    if (!this.vault) return
    try {
      await this.vault.captureDismiss(captureId)
      if (receipt.removeRow(captureId)) this.retireReceipt(blockEl)
      // Say what the refusal actually means. Dismissing suppresses THIS
      // value for the rest of the application session, so the same key in
      // the same command will not ask again — a consequence worth stating
      // once rather than letting it read as the feature having broken.
      showToast({
        level: 'info',
        message: 'Dismissed — this key will not be offered again in this session.',
      })
    } catch (err) {
      if (isCaptureGone(err)) {
        // Already gone: the user's intent is satisfied either way.
        if (receipt.removeRow(captureId)) this.retireReceipt(blockEl)
        return
      }
      showToast({
        level: 'danger',
        message: 'Could not dismiss the offer — try again.',
      })
    }
  }

  /** Hovering a receipt row emphasises that row's chip in the block's
   *  command line — and only that one. Chips carry their redaction span
   *  (data-redaction-start/end), stamped by renderRecordedCommand; the
   *  receipt row carries the capture id, mapped back here to the span. */
  private emphasiseChip(captureId: string | null): void {
    const blockEl =
      captureId === null ? this.receiptBlockEl : (this.receiptChipBlocks.get(captureId) ?? null)
    if (!blockEl) return
    const span = captureId === null ? undefined : this.receiptChipSpans.get(captureId)
    const chips = blockEl.querySelectorAll<HTMLElement>('.ui-secret-chip[data-redaction-start]')
    for (const chip of chips) {
      const matches =
        span !== undefined &&
        chip.dataset.redactionStart === String(span.start) &&
        chip.dataset.redactionEnd === String(span.end)
      chip.classList.toggle('ui-secret-chip--emphasised', matches)
    }
  }

  /** A submit was refused: an unresolved name or a sealed vault must not
   *  silently send a broken line (ADR-0021). The report lands where the
   *  user is looking; the editor's beforeSubmit seam kept the draft. The
   *  sealed case is rare — the dispatcher's unlock seam normally raises the
   *  prompt and retries; reaching here means it was cancelled or absent. */
  private reportSubmitFailure(failure: {
    reason: 'unresolved' | 'sealed' | 'error'
    names?: ReadonlyArray<string>
    message?: string
  }): void {
    if (failure.reason === 'unresolved') {
      const names = (failure.names ?? []).join(', ')
      showToast({
        level: 'danger',
        message: `Unknown secret${(failure.names ?? []).length === 1 ? '' : 's'}: ${names}. The command was not sent.`,
      })
      return
    }
    if (failure.reason === 'sealed') {
      showToast({
        level: 'danger',
        message: 'The vault is locked. Unlock it and run the command again.',
      })
      return
    }
    showToast({
      level: 'danger',
      message: failure.message ?? 'Could not resolve the command. It was not sent.',
    })
  }

  // ── Connection offer on hand-typed ssh block (nocx-pu4.7) ─────────────

  /** After a block freezes, offer to save the destination as a managed
   *  connection — but only for an interactive ssh to a host that is NOT
   *  already a saved profile and NOT previously dismissed. The receipt
   *  follows the BlockReceipt contract: block-attached, non-modal, no
   *  focus steal, no expiry. */
  private async _maybeOfferConnection(): Promise<void> {
    if (!this.profileClient) return
    const blocks = this.scrollback?.blockManager.blocks
    if (!blocks || blocks.length === 0) return

    const last = blocks[blocks.length - 1]
    if (!isInteractiveTransition(last.command)) return

    const dest = extractDestination(last.command)
    if (!dest) return

    // Already a saved profile? No offer.
    if (await this._isKnownProfile(dest)) return

    // Already dismissed? No offer.
    if (await this._isDismissed(dest)) return

    // Already showing for this destination on another block? No duplicate.
    for (const [, r] of this.receipts) {
      if (r instanceof BlockReceipt) continue
    }

    const hostPart = dest.includes('@') ? dest.split('@')[1] : dest

    const receipt = BlockReceipt.forConnection(dest, hostPart, {
      onSave: (name) => void this._saveConnection(dest, name, last.el, receipt),
      onDismiss: () => void this._dismissConnectionOffer(dest, last.el, receipt),
    })

    this.receipts.set(last.el, receipt)
    this.receiptBlockEl = last.el
    receipt.mount(last.el)
  }

  /** Check whether a destination already has a saved SSH profile. */
  private async _isKnownProfile(dest: string): Promise<boolean> {
    if (!this.profileClient) return false
    try {
      const profiles = await this.profileClient.listProfiles()
      // A destination "user@host" matches a profile whose host equals the
      // host part, or user@host equals host (quick-connect profiles).
      const hostPart = dest.includes('@') ? dest.split('@')[1] : dest
      return profiles.some(
        (p) => p.type === 'ssh' && (p.options.host === dest || p.options.host === hostPart),
      )
    } catch {
      // Fail-open: if we cannot list profiles, do not nag.
      return true
    }
  }

  // ── Dismissal persistence via settings (nocx-pu4.7) ──────────────────

  private static readonly DISMISSED_KEY = 'nocx.connectionOffers.dismissed'
  private _dismissedCache: Set<string> | null = null

  /** Load dismissed destinations from settings. Cached per tab: a new tab
   *  picks up the latest value at its first ssh. */
  private async _loadDismissed(): Promise<Set<string>> {
    if (this._dismissedCache) return this._dismissedCache
    if (!this.profileClient) return new Set()
    try {
      const snap = await this.profileClient.getSnapshot()
      const raw = snap.values[TerminalContent.DISMISSED_KEY]
      if (typeof raw === 'string') {
        this._dismissedCache = new Set(JSON.parse(raw) as string[])
        return this._dismissedCache
      }
    } catch {
      // Settings unavailable — no persisted dismissals, but the offer
      // can still be made and dismissed for this session.
    }
    this._dismissedCache = new Set()
    return this._dismissedCache
  }

  private async _isDismissed(dest: string): Promise<boolean> {
    const dismissed = await this._loadDismissed()
    return dismissed.has(dest)
  }

  private async _persistDismissal(dest: string): Promise<void> {
    if (!this.profileClient) return
    const dismissed = await this._loadDismissed()
    dismissed.add(dest)
    try {
      await this.profileClient.setSetting(
        TerminalContent.DISMISSED_KEY,
        JSON.stringify([...dismissed]),
      )
    } catch {
      // Settings write failed — the destination is still dismissed for
      // this session, but will be offered again on restart.
    }
  }

  private async _dismissConnectionOffer(
    dest: string,
    blockEl: HTMLElement,
    receipt: BlockReceipt,
  ): Promise<void> {
    await this._persistDismissal(dest)
    receipt.destroy()
    this.receipts.delete(blockEl)
    if (this.receiptBlockEl === blockEl) this.receiptBlockEl = null
    showToast({
      level: 'info',
      message: `Dismissed — ${dest} will not be offered again as a connection.`,
    })
  }

  private async _saveConnection(
    dest: string,
    name: string,
    blockEl: HTMLElement,
    receipt: BlockReceipt,
  ): Promise<void> {
    if (!this.profileClient) return
    try {
      const hostPart = dest.includes('@') ? dest.split('@')[1] : dest
      const userPart = dest.includes('@') ? dest.split('@')[0] : undefined
      const profile = await this.profileClient.createProfile({
        id: '',
        type: 'ssh',
        name,
        options: {
          host: hostPart,
          ...(userPart ? { user: userPart } : {}),
        },
      })
      receipt.destroy()
      this.receipts.delete(blockEl)
      if (this.receiptBlockEl === blockEl) this.receiptBlockEl = null
      showToast({
        level: 'success',
        message: `Saved "${profile.name}" as a connection.`,
      })
    } catch (err) {
      showToast({
        level: 'danger',
        message: `Could not save connection: ${err instanceof Error ? err.message : String(err)}`,
      })
      receipt.markFailed(`conn-offer:${dest}`, 'could not save — try again')
    }
  }
}
