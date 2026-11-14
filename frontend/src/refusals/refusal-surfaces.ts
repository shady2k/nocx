// The refusal surfaces, in one table (ADR-8eej3e66 stage 1).
//
// Stage 1 removes the one sentence — "The terminal is busy … Try again in a
// moment" — and fixes nothing underneath it: the backend still refuses, the
// wire discriminator still arrives, and refusals still happen. What changes is
// where a person meets one. Every call is made by somebody for some reason,
// and that reason decides what a refusal may become:
//
// - A PERSON's call answers something they just did. Its refusal is shown
//   where they did it: pending cleared, what they typed or chose kept, the
//   failure named, and a way to ask again. A toast raised at the moment of
//   their own action is one of the kit's two answers for that (ui/README.md:
//   "a message about an action does not live in the document flow"); a panel
//   with its own state uses that state instead.
// - A BACKGROUND call is work a surface does on its own account — a store
//   scoping itself to the tab in front, a watcher, a poll, a carrier. Its
//   refusal is quiet: nothing appears over the person, and the work is asked
//   again by its own next trigger or when the panel is shown. The record is
//   the backend's (`control action refused`, and Copy diagnostics names the
//   admission and its holders) plus the renderer's own log line.
//
// THE COMPLETENESS TEST IS THE POINT OF THIS FILE. `refusal-surfaces.test.ts`
// scans every `dispatcher.call('…')` in `src/` and refuses a method this table
// does not name, a method named twice, and a person-facing row that says
// "nothing is shown". A new surface therefore cannot fall through silently:
// adding a call is adding a row, and the row forces the decision.

/** Who a call of this row is made by. `mixed` means the same methods serve
 *  both, and the caller names the origin per call (see `note`). */
type RefusalOriginRuling = 'person' | 'background' | 'mixed'

/** What a person sees when one of these calls is refused, and how they ask
 *  again. `background` says nothing is shown, and only a background row may
 *  say it. */
type RefusalReport =
  | { readonly kind: 'own-state'; readonly where: string; readonly retry: string }
  | { readonly kind: 'action-toast'; readonly where: string; readonly retry: string }
  | { readonly kind: 'background'; readonly where: string }

export interface RefusalSurface {
  /** The JSON-RPC methods this row answers for, exactly as they are spelled
   *  in the source. */
  readonly methods: readonly string[]
  readonly origin: RefusalOriginRuling
  readonly report: RefusalReport
  /** Why the calls that are not a person's are not, when it is not obvious
   *  from the method name. */
  readonly note?: string
}

export const REFUSAL_SURFACES: readonly RefusalSurface[] = [
  // ── files scoping and watching
  {
    methods: ['files.open', 'files.list', 'files.watch', 'files.visible', 'files.close'],
    origin: 'mixed',
    report: {
      kind: 'own-state',
      where: 'files-store: phase failed + openError, watchFailed, or the directory state row',
      retry: 'the header Refresh, the directory’s Retry, or the panel being shown again',
    },
    note: "The store's own scoping (open/list/watch/visible/close) is BACKGROUND; the same methods serve a person's expand, Show next, header Refresh and Retry, and those are PERSON. The store names the origin per call.",
  },
  // ── files person actions
  {
    methods: [
      'files.stat',
      'files.read',
      'files.reveal',
      'files.upload',
      'files.uploadCancel',
      'files.download',
      'files.downloadCancel',
    ],
    origin: 'person',
    report: {
      kind: 'action-toast',
      where: 'files-view / upload and download flows: the action’s own outcome, named',
      retry: 'the same control',
    },
  },
  // ── git
  {
    methods: ['git.open', 'git.status', 'git.close'],
    origin: 'mixed',
    report: {
      kind: 'own-state',
      where: 'git-store: mutationRefused banner; the poll keeps no failure at all',
      retry: 'the banner’s Retry, or the next poll',
    },
    note: "The panel's poll and the pane's branch source are BACKGROUND (git.status from the poll, git.open/git.close from where/branch-source.ts); every other status read and every open/close the panel makes is PERSON.",
  },
  // ── git mutations and reads
  {
    methods: [
      'git.diff',
      'git.log',
      'git.stage',
      'git.unstage',
      'git.stageAll',
      'git.unstageAll',
      'git.commit',
      'git.headMessage',
      'git.remote',
      'shell.openUrl',
    ],
    origin: 'person',
    report: {
      kind: 'own-state',
      where:
        'git-store: mutationRefused banner for a refused mutation; mutationError + the stale banner for a failure',
      retry: 'the banner’s Retry, or Refresh',
    },
  },
  // ── backup and restore
  {
    methods: ['backup.create', 'backup.preview', 'backup.restore', 'backup.saveToFile'],
    origin: 'person',
    report: {
      kind: 'own-state',
      where: 'backup-restore-section: previewRefused with its sentence, or the action’s own toast',
      retry: 'Preview again, or the same control',
    },
  },
  // ── vault and secrets
  {
    methods: [
      'vault.status',
      'vault.setup',
      'vault.unseal',
      'vault.seal',
      'vault.changePassphrase',
      'vault.regenerateRecovery',
      'vault.setDefaultProvider',
      'vault.setAutoSeal',
      'vault.resetPreview',
      'vault.reset',
      'vault.inventory',
      'vault.createSecret',
      'vault.renameSecret',
      'vault.replaceSecret',
      'vault.deleteSecret',
      'vault.resolveLine',
      'vault.activity',
      'vault.unlockResolved',
      'secrets.detect',
      'secrets.captureSave',
      'secrets.captureDismiss',
      'secrets.savePassword',
      'secrets.saveKeyMaterial',
      'secrets.saveKeyPassphrase',
      'secrets.usage',
    ],
    origin: 'person',
    report: {
      kind: 'action-toast',
      where: 'vault.tsx / settings: the section’s own error line or the action’s own toast',
      retry: 'the same control',
    },
  },
  // ── profiles, groups, connections, settings
  {
    methods: [
      'profiles.list',
      'profiles.create',
      'profiles.update',
      'profiles.delete',
      'profiles.effective',
      'profiles.patch',
      'profiles.moveImpact',
      'profiles.importTabby',
      'profiles.tabbyPreview',
      'profiles.tabbyExecute',
      'groups.list',
      'groups.create',
      'groups.update',
      'groups.delete',
      'groups.impact',
      'groups.apply',
      'connections.test',
      'connections.trustHostKey',
      'connections.setIntegrationMethod',
      'connections.passwordResolved',
      'sessions.status',
      'sshConfig.aliases',
      'sshConfig.path',
      'settings.describe',
      'settings.getSnapshot',
      'settings.set',
      'settings.reset',
      'settings.secretSet',
      'settings.secretDelete',
      'settings.secretExists',
      'roles.list',
      'roles.assign',
      'roles.setDefault',
      'endpoints.list',
      'endpoints.create',
      'endpoints.update',
      'endpoints.delete',
      'endpoints.probe',
      'app.about',
      'app.diagnostics',
    ],
    origin: 'person',
    report: {
      kind: 'action-toast',
      where:
        'the settings page or connection surface that asked: its own error line or the action’s own toast',
      retry: 'the same control',
    },
  },
  // ── api workbench
  {
    methods: [
      'api.collections.list',
      'api.collections.open',
      'api.collections.create',
      'api.collections.createFolder',
      'api.collections.close',
      'api.environment.read',
      'api.environment.write',
      'api.folder.read',
      'api.folder.write',
      'api.import.curl',
      'api.import.postman',
      'api.request.cancel',
      'api.request.delete',
      'api.request.move',
      'api.request.read',
      'api.request.scope',
      'api.request.send',
      'api.request.write',
    ],
    origin: 'person',
    report: {
      kind: 'own-state',
      where: 'api-store: its own error signal, and watchFailed for the carrier',
      retry: 'the workbench’s Refresh, or the same control',
    },
    note: 'The collection CARRIER is background, but it rides files.open/files.watch/files.close (the files row), not api.*.',
  },
  // ── notes
  {
    methods: [
      'notes.list',
      'notes.get',
      'notes.create',
      'notes.update',
      'notes.delete',
      'notes.search',
    ],
    origin: 'person',
    report: {
      kind: 'action-toast',
      where: 'notes-panel: the action’s own outcome',
      retry: 'the same control',
    },
  },
  // ── snippets
  {
    methods: [
      'snippets.list',
      'snippets.create',
      'snippets.update',
      'snippets.delete',
      'snippets.reorder',
    ],
    origin: 'person',
    report: {
      kind: 'action-toast',
      where: 'the snippet palette/settings: the action’s own outcome',
      retry: 'the same control',
    },
  },
  // ── assistant and agent
  {
    methods: [
      'agent.ask',
      'agent.approve',
      'agent.cancel',
      'agent.dump',
      'agent.status',
      'agent.emitting',
      'agent.calibration',
      'agent.calibration.answer',
      'agent.rules',
      'agent.rules.set',
      'agent.rules.setEnabled',
      'agent.rules.delete',
      'agent.type',
      'agent.runResolved',
      'agentAccess.list',
      'agentAccess.forget',
      'agentRecords.list',
      'agentRecords.save',
      'agentRecords.remove',
      'agentRecords.detect',
      'policy.get',
      'policy.set',
      'policy.classify',
      'policy.explain',
      'policy.setRule',
      'policy.forgetRule',
    ],
    origin: 'person',
    report: {
      kind: 'action-toast',
      where: 'the assistant surface that asked: its own error line or the action’s own outcome',
      retry: 'the same control',
    },
  },
  // ── terminal and session
  {
    methods: [
      'session.intent',
      'session.signal',
      'session.historyPage',
      'session.recoveryStatus',
      'open',
      'attach',
      'detach',
      'close',
      'resize',
      'sessions.live',
      'sessions.inventory',
      'history.query',
      'history.status',
      'checkouts.status',
      'fs.complete',
      'shell.complete',
      'shell.commandNames',
      'shell.integrate',
      'shell.footprint.status',
      'shell.footprint.uninstall',
      'shell.footprint.helperUninstall',
    ],
    origin: 'mixed',
    report: {
      kind: 'own-state',
      where:
        'terminal-content: the pane’s own connection/overlay state, or the action’s own outcome',
      retry: 'the overlay’s Retry, or the same control',
    },
    note: "The pane's bookkeeping (attach, detach, close, resize, sessions.live, sessions.inventory, fs.*, shell.commandNames) is BACKGROUND: it is the pane's own machinery, and the person's recovery is the connection overlay. session.open and session.intent, shell.integrate and the footprint calls are PERSON.",
  },
  // ── layout, tabs, panes, workspaces, uistate
  {
    methods: [
      'layout.read',
      'panes.create',
      'panes.close',
      'panes.setCwd',
      'tabs.create',
      'tabs.close',
      'tabs.rename',
      'tabs.pin',
      'tabs.recolour',
      'tabs.reorder',
      'tabs.setPanel',
      'workspaces.create',
      'workspaces.close',
      'workspaces.rename',
      'workspaces.recolour',
      'workspaces.reorder',
      'uistate.get',
      'uistate.set',
      'uistate.window.get',
    ],
    origin: 'mixed',
    report: {
      kind: 'action-toast',
      where: 'the layout surface: the action’s own outcome',
      retry: 'the same control',
    },
    note: "uistate.* and layout.read are the window's own bookkeeping and are BACKGROUND; tabs/panes/workspaces are a person's gestures and are PERSON.",
  },
  // ── a mutation whose answer was lost
  {
    methods: ['operation.outcome'],
    origin: 'mixed',
    report: {
      kind: 'own-state',
      where:
        'the Operations row for that call (operations/call-operations.ts): it stops saying "checking what happened" and says the outcome is unknown, naming what to look at',
      retry:
        'nothing re-sends: a mutation is never replayed, and asking for the change again is the person’s own next action',
    },
    note: "A person's Cancel, or the renderer noticing that the answer never arrived, is what asks (mutations.ts, and the dispatcher's vault-unlock gate for a background call); either way the answer lands in the row that person was watching, and a refused ask becomes one of the three answers rather than a failure of its own.",
  },
  // ── notifications the running program raises
  {
    methods: ['notify.raise', 'notify.bell', 'notify.paneWorkFinished', 'notify.catalogue'],
    origin: 'background',
    report: {
      kind: 'background',
      where:
        'nothing shown: a bell, a badge or a catalogue read refused is not a failure a person can act on',
    },
    note: 'Raised by the running program (a bell, a finished command) or read once for the panel’s categories — never by a press.',
  },
  // ── the notifications panel's own gestures
  {
    methods: ['notify.feed.read', 'notify.feed.markRead'],
    origin: 'person',
    report: {
      kind: 'action-toast',
      where: 'the notifications panel: the action’s own outcome',
      retry: 'the same control',
    },
  },
  // ── ledger, lifecycle, host
  {
    methods: [
      'ledger.bind',
      'ledger.get',
      'ledger.query',
      'ledger.artifact',
      'lifecycle.submitAttempt',
      'lifecycle.recoverAck',
      'host.resolved',
    ],
    origin: 'background',
    report: {
      kind: 'background',
      where:
        'nothing shown: the ledger and lifecycle are fail-open bookkeeping (command-ledger.ts: a refused submitAttempt must never swallow the command)',
    },
  },
  // ── ports
  {
    methods: ['ports.status', 'ports.sample', 'ports.pause', 'ports.visible'],
    origin: 'mixed',
    report: {
      kind: 'own-state',
      where: 'ports panel: its own listing failure, named',
      retry: 'Refresh',
    },
    note: "ports.sample is the panel's poll and is BACKGROUND while it is on screen; ports.visible is its own bookkeeping.",
  },
  // ── dialog
  {
    methods: ['dialog.openFile', 'dialog.openDirectory', 'dialog.openFileForUpload'],
    origin: 'person',
    report: {
      kind: 'action-toast',
      where: 'the action that raised the picker: its own outcome',
      retry: 'the same control',
    },
  },
  // ── agent lane bookkeeping
  {
    methods: ['agent.laneInteractivity'],
    origin: 'background',
    report: {
      kind: 'background',
      where: 'nothing shown: the pane’s own interactivity report, fire-and-forget',
    },
  },
]
