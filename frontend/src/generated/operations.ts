/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/operations.json, itself generated from
 * internal/operation/catalog. Regenerate: cd frontend && npm run contracts
 */

export interface CallOperationDeclaration {
  /** The sentence shown while this call waits, in the person's terms. */
  readonly waiting: string
  /** When Operations may show it. */
  readonly row: 'never' | 'past-wait' | 'always'
  /** The outcome at the deadline. 'outcome-unknown' is the catalogue's own
   *  statement that this call may have changed something by then, and the one
   *  test for "this is a mutation": only these calls carry an identity. */
  readonly expired: 'not-started' | 'outcome-unknown' | 'retry-read'
}

const CALL_OPERATIONS: Readonly<Record<string, CallOperationDeclaration>> = {
  ack: { waiting: 'Acknowledging terminal output', row: 'never', expired: 'not-started' },
  'agent.approve': {
    waiting: 'Waiting for your approval',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'agent.ask': { waiting: 'Waiting for the model', row: 'past-wait', expired: 'outcome-unknown' },
  'agent.calibration': {
    waiting: 'Calibrating the agent',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'agent.calibration.answer': {
    waiting: 'Calibrating the agent',
    row: 'never',
    expired: 'outcome-unknown',
  },
  'agent.cancel': { waiting: 'Stopping the agent run', row: 'past-wait', expired: 'not-started' },
  'agent.dump': { waiting: 'Reading the agent run', row: 'past-wait', expired: 'retry-read' },
  'agent.emitting': { waiting: 'Waiting for the model', row: 'past-wait', expired: 'not-started' },
  'agent.laneInteractivity': {
    waiting: 'Waiting for the model',
    row: 'never',
    expired: 'not-started',
  },
  'agent.rules': { waiting: 'Saving the agent rules', row: 'past-wait', expired: 'retry-read' },
  'agent.rules.delete': {
    waiting: 'Saving the agent rules',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'agent.rules.set': {
    waiting: 'Saving the agent rules',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'agent.rules.setEnabled': {
    waiting: 'Saving the agent rules',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'agent.runResolved': { waiting: 'Waiting for the model', row: 'never', expired: 'not-started' },
  'agent.status': { waiting: 'Waiting for the model', row: 'past-wait', expired: 'retry-read' },
  'agent.type': { waiting: 'Waiting for the model', row: 'past-wait', expired: 'outcome-unknown' },
  'agentAccess.forget': {
    waiting: 'Reading the agent access list',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'agentAccess.list': {
    waiting: 'Reading the agent access list',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'agentRecords.list': {
    waiting: 'Reading the agent records',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'agentRecords.remove': {
    waiting: 'Reading the agent records',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'agentRecords.save': {
    waiting: 'Reading the agent records',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'api.collections.close': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'not-started',
  },
  'api.collections.create': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'api.collections.createFolder': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'api.collections.list': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'api.collections.open': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'api.environment.read': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'api.environment.write': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'api.folder.read': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'api.folder.write': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'api.import.curl': {
    waiting: 'Importing the curl command',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'api.import.postman': {
    waiting: 'Importing the Postman collection',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'api.request.cancel': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'not-started',
  },
  'api.request.delete': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'api.request.move': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'api.request.read': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'api.request.scope': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'api.request.send': {
    waiting: 'Sending this request',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'api.request.write': {
    waiting: 'Working on this collection',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'app.about': {
    waiting: 'Reading nocx’s build information',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'app.diagnostics': { waiting: 'Collecting diagnostics', row: 'past-wait', expired: 'retry-read' },
  attach: { waiting: 'Reattaching to this session', row: 'past-wait', expired: 'not-started' },
  'backup.create': {
    waiting: 'Backing up your settings',
    row: 'always',
    expired: 'outcome-unknown',
  },
  'backup.preview': { waiting: 'Reading the backup file', row: 'past-wait', expired: 'retry-read' },
  'backup.restore': {
    waiting: 'Restoring your settings',
    row: 'always',
    expired: 'outcome-unknown',
  },
  'backup.saveToFile': {
    waiting: 'Writing the backup file',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'checkouts.status': { waiting: 'Reading the checkouts', row: 'past-wait', expired: 'retry-read' },
  close: { waiting: 'Closing this session', row: 'past-wait', expired: 'outcome-unknown' },
  'connections.passwordResolved': {
    waiting: 'Checking the connection',
    row: 'never',
    expired: 'not-started',
  },
  'connections.setIntegrationMethod': {
    waiting: 'Checking the connection',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'connections.test': {
    waiting: 'Checking the connection',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'connections.trustHostKey': {
    waiting: 'Waiting for you to accept the host key',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  detach: { waiting: 'Detaching this session', row: 'past-wait', expired: 'not-started' },
  'dialog.openDirectory': {
    waiting: 'Waiting for you to choose a folder',
    row: 'past-wait',
    expired: 'not-started',
  },
  'dialog.openFile': {
    waiting: 'Waiting for you to choose a file',
    row: 'past-wait',
    expired: 'not-started',
  },
  'dialog.openFileForUpload': {
    waiting: 'Waiting for you to choose a file',
    row: 'past-wait',
    expired: 'not-started',
  },
  'endpoints.create': {
    waiting: 'Saving your endpoints',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'endpoints.delete': {
    waiting: 'Saving your endpoints',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'endpoints.list': { waiting: 'Saving your endpoints', row: 'past-wait', expired: 'retry-read' },
  'endpoints.probe': {
    waiting: 'Checking the model endpoint',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'endpoints.update': {
    waiting: 'Saving your endpoints',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'files.close': { waiting: 'Closing this folder', row: 'past-wait', expired: 'not-started' },
  'files.download': { waiting: 'Downloading this file', row: 'always', expired: 'outcome-unknown' },
  'files.downloadCancel': {
    waiting: 'Reading this folder',
    row: 'past-wait',
    expired: 'not-started',
  },
  'files.list': { waiting: 'Reading this folder', row: 'past-wait', expired: 'retry-read' },
  'files.open': { waiting: 'Opening this folder', row: 'past-wait', expired: 'outcome-unknown' },
  'files.read': { waiting: 'Reading this file', row: 'past-wait', expired: 'retry-read' },
  'files.reveal': { waiting: 'Revealing this file', row: 'past-wait', expired: 'outcome-unknown' },
  'files.stat': { waiting: 'Looking at this file', row: 'past-wait', expired: 'retry-read' },
  'files.upload': { waiting: 'Uploading this file', row: 'always', expired: 'outcome-unknown' },
  'files.uploadCancel': {
    waiting: 'Reading this folder',
    row: 'past-wait',
    expired: 'not-started',
  },
  'files.visible': { waiting: 'Showing this folder', row: 'past-wait', expired: 'retry-read' },
  'files.watch': { waiting: 'Watching this folder', row: 'past-wait', expired: 'retry-read' },
  'fs.complete': { waiting: 'Reading this folder', row: 'past-wait', expired: 'retry-read' },
  'git.close': { waiting: 'Closing this checkout', row: 'past-wait', expired: 'outcome-unknown' },
  'git.commit': {
    waiting: 'Committing in this checkout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'git.diff': {
    waiting: 'Reading this checkout’s changes',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'git.headMessage': {
    waiting: 'Waiting for git in this checkout',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'git.log': {
    waiting: 'Reading this checkout’s history',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'git.open': { waiting: 'Opening this checkout', row: 'past-wait', expired: 'outcome-unknown' },
  'git.remote': {
    waiting: 'Waiting for git in this checkout',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'git.stage': {
    waiting: 'Waiting for git in this checkout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'git.stageAll': {
    waiting: 'Waiting for git in this checkout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'git.status': { waiting: 'Reading this checkout', row: 'past-wait', expired: 'retry-read' },
  'git.unstage': {
    waiting: 'Waiting for git in this checkout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'git.unstageAll': {
    waiting: 'Waiting for git in this checkout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'groups.apply': { waiting: 'Saving your groups', row: 'past-wait', expired: 'outcome-unknown' },
  'groups.create': { waiting: 'Saving your groups', row: 'past-wait', expired: 'outcome-unknown' },
  'groups.delete': { waiting: 'Saving your groups', row: 'past-wait', expired: 'outcome-unknown' },
  'groups.impact': { waiting: 'Saving your groups', row: 'past-wait', expired: 'retry-read' },
  'groups.list': { waiting: 'Saving your groups', row: 'past-wait', expired: 'retry-read' },
  'groups.update': { waiting: 'Saving your groups', row: 'past-wait', expired: 'outcome-unknown' },
  'history.query': {
    waiting: 'Looking through your history',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'history.status': {
    waiting: 'Checking history recording',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'host.attentionActivated': {
    waiting: 'Showing the window',
    row: 'never',
    expired: 'not-started',
  },
  'host.resolved': { waiting: 'Waiting for the window', row: 'never', expired: 'not-started' },
  'layout.read': { waiting: 'Reading the window layout', row: 'past-wait', expired: 'retry-read' },
  'ledger.artifact': {
    waiting: 'Reading the session ledger',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'ledger.bind': {
    waiting: 'Reading the session ledger',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'ledger.close': {
    waiting: 'Reading the session ledger',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'ledger.get': { waiting: 'Reading the session ledger', row: 'past-wait', expired: 'retry-read' },
  'ledger.open': {
    waiting: 'Reading the session ledger',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'ledger.query': {
    waiting: 'Reading the session ledger',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'lifecycle.recoverAck': {
    waiting: 'Recording the recovery acknowledgement',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'lifecycle.submitAttempt': {
    waiting: 'Recording the command attempt',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'notes.create': { waiting: 'Reading your notes', row: 'past-wait', expired: 'outcome-unknown' },
  'notes.delete': { waiting: 'Reading your notes', row: 'past-wait', expired: 'outcome-unknown' },
  'notes.get': { waiting: 'Reading your notes', row: 'past-wait', expired: 'retry-read' },
  'notes.list': { waiting: 'Reading your notes', row: 'past-wait', expired: 'retry-read' },
  'notes.search': { waiting: 'Reading your notes', row: 'past-wait', expired: 'retry-read' },
  'notes.update': { waiting: 'Reading your notes', row: 'past-wait', expired: 'outcome-unknown' },
  'notify.bell': { waiting: 'Sending the notification', row: 'never', expired: 'not-started' },
  'notify.catalogue': { waiting: 'Sending the notification', row: 'never', expired: 'retry-read' },
  'notify.feed.markRead': {
    waiting: 'Reading notifications',
    row: 'never',
    expired: 'not-started',
  },
  'notify.feed.read': { waiting: 'Reading notifications', row: 'never', expired: 'retry-read' },
  'notify.paneWorkFinished': {
    waiting: 'Sending the notification',
    row: 'never',
    expired: 'not-started',
  },
  'notify.raise': { waiting: 'Sending the notification', row: 'never', expired: 'not-started' },
  open: { waiting: 'Opening your terminal', row: 'always', expired: 'outcome-unknown' },
  'operation.outcome': { waiting: 'Checking what happened', row: 'never', expired: 'retry-read' },
  'panes.close': {
    waiting: 'Changing the pane layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'panes.create': {
    waiting: 'Changing the pane layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'panes.move': {
    waiting: 'Changing the pane layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'panes.setCwd': {
    waiting: 'Changing the pane layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'policy.classify': {
    waiting: 'Reading the agent policy',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'policy.explain': {
    waiting: 'Reading the agent policy',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'policy.forgetRule': {
    waiting: 'Reading the agent policy',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'policy.get': { waiting: 'Reading the agent policy', row: 'past-wait', expired: 'retry-read' },
  'policy.set': {
    waiting: 'Saving the agent policy',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'policy.setRule': {
    waiting: 'Reading the agent policy',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'ports.pause': {
    waiting: 'Sampling the listening ports',
    row: 'past-wait',
    expired: 'not-started',
  },
  'ports.sample': {
    waiting: 'Sampling the listening ports',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'ports.status': {
    waiting: 'Sampling the listening ports',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'ports.visible': {
    waiting: 'Sampling the listening ports',
    row: 'past-wait',
    expired: 'not-started',
  },
  'profiles.create': {
    waiting: 'Saving your profile',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'profiles.delete': {
    waiting: 'Removing your profile',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'profiles.effective': {
    waiting: 'Saving your profiles',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'profiles.importTabby': {
    waiting: 'Saving your profiles',
    row: 'always',
    expired: 'outcome-unknown',
  },
  'profiles.list': { waiting: 'Reading your profiles', row: 'past-wait', expired: 'retry-read' },
  'profiles.moveImpact': {
    waiting: 'Saving your profiles',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'profiles.patch': {
    waiting: 'Saving your profile',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'profiles.tabbyExecute': {
    waiting: 'Saving your profiles',
    row: 'always',
    expired: 'outcome-unknown',
  },
  'profiles.tabbyPreview': {
    waiting: 'Saving your profiles',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'profiles.update': {
    waiting: 'Saving your profile',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  resize: { waiting: 'Resizing this terminal', row: 'past-wait', expired: 'not-started' },
  'roles.assign': {
    waiting: 'Saving the model roles',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'roles.list': { waiting: 'Saving the model roles', row: 'past-wait', expired: 'retry-read' },
  'roles.setDefault': {
    waiting: 'Saving the model roles',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'rpc.cancel': { waiting: 'Stopping the operation', row: 'never', expired: 'not-started' },
  'secrets.captureDismiss': {
    waiting: 'Saving the secret',
    row: 'never',
    expired: 'outcome-unknown',
  },
  'secrets.captureSave': {
    waiting: 'Saving the captured secret',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'secrets.detect': {
    waiting: 'Looking for secrets in the output',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'secrets.paneClosed': { waiting: 'Saving the secret', row: 'never', expired: 'outcome-unknown' },
  'secrets.saveKeyMaterial': {
    waiting: 'Saving the secret',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'secrets.saveKeyPassphrase': {
    waiting: 'Saving the secret',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'secrets.savePassword': {
    waiting: 'Saving the secret',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'secrets.usage': { waiting: 'Saving the secret', row: 'past-wait', expired: 'retry-read' },
  'session.historyPage': {
    waiting: 'Reading this session’s history',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'session.intent': {
    waiting: 'Sending input to this terminal',
    row: 'past-wait',
    expired: 'not-started',
  },
  'session.recoveryStatus': {
    waiting: 'Reading this session’s record',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'session.signal': {
    waiting: 'Signalling this terminal',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'sessions.inventory': {
    waiting: 'Reading the helper sessions',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'sessions.live': {
    waiting: 'Reading the live sessions',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'sessions.status': { waiting: 'Reading the sessions', row: 'past-wait', expired: 'retry-read' },
  'settings.describe': {
    waiting: 'Reading your settings',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'settings.getSnapshot': {
    waiting: 'Reading your settings',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'settings.reset': {
    waiting: 'Resetting your settings',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'settings.secretDelete': {
    waiting: 'Saving your settings',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'settings.secretExists': {
    waiting: 'Saving your settings',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'settings.secretSet': {
    waiting: 'Saving your settings',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'settings.set': { waiting: 'Saving your settings', row: 'past-wait', expired: 'outcome-unknown' },
  'shell.commandNames': {
    waiting: 'Installing shell integration',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'shell.complete': {
    waiting: 'Installing shell integration',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'shell.footprint.helperUninstall': {
    waiting: 'Installing shell integration',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'shell.footprint.status': {
    waiting: 'Installing shell integration',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'shell.footprint.uninstall': {
    waiting: 'Installing shell integration',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'shell.integrate': {
    waiting: 'Installing shell integration',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'shell.openUrl': {
    waiting: 'Installing shell integration',
    row: 'past-wait',
    expired: 'not-started',
  },
  'skills.approve': {
    waiting: 'Reading your skills',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'skills.audit': {
    waiting: 'Asking the model to audit this skill',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'skills.check': { waiting: 'Reading your skills', row: 'past-wait', expired: 'retry-read' },
  'skills.file': { waiting: 'Reading your skills', row: 'past-wait', expired: 'retry-read' },
  'skills.files': { waiting: 'Reading your skills', row: 'past-wait', expired: 'retry-read' },
  'skills.list': { waiting: 'Reading your skills', row: 'past-wait', expired: 'retry-read' },
  'skills.remove': { waiting: 'Reading your skills', row: 'past-wait', expired: 'outcome-unknown' },
  'skills.scan': { waiting: 'Scanning your skills', row: 'past-wait', expired: 'outcome-unknown' },
  'skills.setEnabled': {
    waiting: 'Reading your skills',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'skills.setPin': { waiting: 'Reading your skills', row: 'past-wait', expired: 'outcome-unknown' },
  'snippets.create': {
    waiting: 'Saving your snippets',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'snippets.delete': {
    waiting: 'Saving your snippets',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'snippets.list': { waiting: 'Saving your snippets', row: 'past-wait', expired: 'retry-read' },
  'snippets.reorder': {
    waiting: 'Saving your snippets',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'snippets.update': {
    waiting: 'Saving your snippets',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'sshConfig.aliases': {
    waiting: 'Reading your ssh config',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'sshConfig.path': { waiting: 'Reading your ssh config', row: 'past-wait', expired: 'retry-read' },
  'tabs.close': {
    waiting: 'Changing the tab layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'tabs.create': {
    waiting: 'Changing the tab layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'tabs.pin': { waiting: 'Changing the tab layout', row: 'past-wait', expired: 'outcome-unknown' },
  'tabs.recolour': {
    waiting: 'Changing the tab layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'tabs.rename': {
    waiting: 'Changing the tab layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'tabs.reorder': {
    waiting: 'Changing the tab layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'transport.ping': {
    waiting: 'Checking the backend is alive',
    row: 'never',
    expired: 'not-started',
  },
  'tunnel.open': { waiting: 'Opening the tunnel', row: 'past-wait', expired: 'outcome-unknown' },
  'tunnel.stop': { waiting: 'Opening the tunnel', row: 'past-wait', expired: 'not-started' },
  'uistate.get': { waiting: 'Reading the window state', row: 'past-wait', expired: 'retry-read' },
  'uistate.set': {
    waiting: 'Saving the window state',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'uistate.window.get': {
    waiting: 'Saving the window state',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'uistate.window.observe': {
    waiting: 'Saving the window state',
    row: 'never',
    expired: 'not-started',
  },
  'vault.activity': { waiting: 'Waiting for the vault', row: 'past-wait', expired: 'retry-read' },
  'vault.changePassphrase': {
    waiting: 'Changing the vault passphrase',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'vault.createSecret': {
    waiting: 'Waiting for the vault',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'vault.deleteSecret': {
    waiting: 'Waiting for the vault',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'vault.inventory': { waiting: 'Waiting for the vault', row: 'past-wait', expired: 'retry-read' },
  'vault.regenerateRecovery': {
    waiting: 'Waiting for the vault',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'vault.renameSecret': {
    waiting: 'Waiting for the vault',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'vault.replaceSecret': {
    waiting: 'Waiting for the vault',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'vault.reset': { waiting: 'Resetting the vault', row: 'past-wait', expired: 'outcome-unknown' },
  'vault.resetPreview': {
    waiting: 'Checking what a vault reset would remove',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'vault.resolveLine': {
    waiting: 'Waiting for the vault',
    row: 'past-wait',
    expired: 'retry-read',
  },
  'vault.seal': { waiting: 'Sealing the vault', row: 'past-wait', expired: 'outcome-unknown' },
  'vault.setAutoSeal': {
    waiting: 'Waiting for the vault',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'vault.setDefaultProvider': {
    waiting: 'Waiting for the vault',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'vault.setup': { waiting: 'Setting up the vault', row: 'past-wait', expired: 'outcome-unknown' },
  'vault.status': { waiting: 'Waiting for the vault', row: 'past-wait', expired: 'retry-read' },
  'vault.unlockResolved': {
    waiting: 'Waiting for the vault',
    row: 'never',
    expired: 'not-started',
  },
  'vault.unseal': {
    waiting: 'Waiting for you to unlock the vault',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'workspaces.close': {
    waiting: 'Changing the window layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'workspaces.create': {
    waiting: 'Changing the window layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'workspaces.recolour': {
    waiting: 'Changing the window layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'workspaces.rename': {
    waiting: 'Changing the window layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
  'workspaces.reorder': {
    waiting: 'Changing the window layout',
    row: 'past-wait',
    expired: 'outcome-unknown',
  },
}

/** The declaration for a JSON-RPC method, or undefined where none exists. */
export function callOperationDeclaration(method: string): CallOperationDeclaration | undefined {
  return CALL_OPERATIONS[method]
}
