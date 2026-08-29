# Assistant UI action capability map

This document is the cut line for the assistant-to-UI projection. It is deliberately
named for the deliverable, rather than for a Go package: the source of truth is the
set of things a person can do in the frontend, and the capability catalogue is the
implementation projection.

## Scope and counting rules

The map starts from the frontend surfaces and the inventory in
`frontend/src/ui/README.md`; it does not count JSON-RPC methods. A method may be a
notification, acknowledgement, resolved callback, or transport callback rather than
a user action.

An **action family** is one user intent. CRUD is one family when the same surface and
resource semantics apply; changing one field or choosing one radio option is not a
new family. This produces **95 user-facing action families**. The count is useful for
coverage, not as a claim that the eventual tool schema has 95 methods.

The capability count is **23 exported operation interfaces** currently present in
`internal/capability`:

- `AgentOperation`, `APICollectionOperation`, `APIImportOperation`,
  `BackupOperation`, `CaptureSaveOperation`, `TabbyImportOperation`,
  `ConfigOperation`, `ContentOperation`, `FilesystemOpenOperation`,
  `FilesystemBindingOperation`, `GitOpenOperation`, `GitBindingOperation`,
  `LayoutOperation`, `LedgerOperation`, `NoteOperation`, `OpenOperation`,
  `SecretOperation`, `SessionOperation`, `SessionTargetOperation`,
  `SnippetOperation`, `UIStateOperation`, `VaultOperation`, and
  `VaultResetOperation`.
- `SessionOperations` is a factory/holder, not an additional operation interface.
- There is no `internal/capability/endpoint.go` in this checkout. Endpoint and role
  actions are served by `ConfigOperation`; `endpoints.probe` additionally calls the
  assistant probe seam in transport.

The operation classification is:

| Class     |  Count | Meaning                                                                                                                                                                            |
| --------- | -----: | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Direct    |     14 | The operation callback is headless and has explicit resource inputs; the operation itself does not require a websocket, connection state, current selection, or renderer callback. |
| Adapted   |      7 | A façade must supply connection/session ownership, binding identity, or a transport-owned lifetime before the operation can be an assistant action.                                |
| Excluded  |      2 | The operation is an internal durability/presentation mechanism, not a user action the assistant should expose.                                                                     |
| **Total** | **23** |                                                                                                                                                                                    |

The action table uses `assistant.ui.execute` as the intended generic entry point. It is
a proposed façade contract, not a claim that this method already exists. Its payload
is an action id plus explicit params; it must dispatch to the capability operation or
to the renderer executor named in the table. This is the single generic route that
avoids one bridge per feature.

`assistant.ui.observe` means the assistant can inspect the resulting bounded state in
the same exchange. For actions that create a terminal or transfer, the result must
contain the minted id and lifecycle state; it must not require scraping undocumented
DOM state.

Every acceptance scenario below is intended for the headless devharness path in
`e2e/assistant-ui.spec.ts`. A scenario name is a test plan identifier, not an existing
test. External providers, SSH servers, and clocks must be scripted and held/released;
no scenario may use a sleep as synchronization.

## Action map

### Terminal, assistant, and capture

| ID  | User-facing action family                                              | Capability operation(s)                                              | Intended agent entry point                                                 | Headless acceptance scenario                                                                                                           |
| --- | ---------------------------------------------------------------------- | -------------------------------------------------------------------- | -------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| T01 | Start a local terminal                                                 | `OpenOperation` (adapted: session registration/ownership)            | `assistant.ui.execute("terminal.openLocal")`                               | `terminal.openLocal`: assistant opens a pane and the returned session is visible in the terminal surface                               |
| T02 | Connect using a saved profile, SSH alias, or quick-connect destination | `OpenOperation` (adapted)                                            | `assistant.ui.execute("terminal.connect")`                                 | `terminal.connect`: scripted SSH accepts the connection and the returned session id is attached to the requested pane                  |
| T03 | Resize a terminal                                                      | `SessionTargetOperation` (adapted: session ownership)                | `assistant.ui.execute("terminal.resize")`                                  | `terminal.resize`: explicit session id and dimensions reach the target session and the bounded result reports them                     |
| T04 | Close the active pane or tab                                           | `LayoutOperation` + `SessionOperation` (adapted)                     | `assistant.ui.execute("layout.closeSurface")`                              | `layout.closeSurface`: the named pane/tab disappears and its session emits one terminal outcome                                        |
| T05 | Reattach a session after reconnect                                     | `SessionTargetOperation` (adapted)                                   | `assistant.ui.execute("terminal.reattach")`                                | `terminal.reattach`: a dropped connection is reattached by explicit session id and the terminal receives subsequent bytes              |
| T06 | Run a shell command                                                    | None — terminal bytes use the data plane, not a capability operation | `assistant.ui.execute("terminal.submit")` through the renderer executor    | `terminal.submit`: assistant submits text and the held shell produces a prompt-delimited result                                        |
| T07 | Ask the assistant a question                                           | `AgentOperation`                                                     | `assistant.ui.execute("agent.ask")`                                        | `agent.ask`: held fake model streams a bounded answer and the assistant block terminalizes                                             |
| T08 | Cancel an in-flight assistant run                                      | None — run cancellation is transport/assistant-run state             | `assistant.ui.execute("agent.cancel")`                                     | `agent.cancel`: held stream is cancelled, the run becomes cancelled, and no later answer is rendered                                   |
| T09 | Approve or deny a proposed assistant tool action                       | None — approval is policy/transport state                            | `assistant.ui.execute("agent.approval")`                                   | `agent.approval`: a proposal is shown, approval executes once, denial executes zero times                                              |
| T10 | Save a detected secret capture                                         | `CaptureSaveOperation`                                               | `assistant.ui.execute("capture.save")`                                     | `capture.save`: a held capture is saved and its linked history row contains the returned secret reference                              |
| T11 | Dismiss a detected secret capture                                      | None — dismissal only clears renderer/capture state                  | `assistant.ui.execute("capture.dismiss")`                                  | `capture.dismiss`: the prompt closes and the secret store and history remain unchanged                                                 |
| T12 | Insert a stored secret into a command                                  | `SecretOperation`                                                    | `assistant.ui.execute("secret.resolveForInput")`                           | `secret.resolveForInput`: an explicit row handle resolves into the command input without exposing the secret in the durable transcript |
| T13 | Copy or paste terminal text                                            | None — renderer/clipboard action                                     | `assistant.ui.execute("terminal.clipboard")` through the renderer executor | `terminal.clipboard`: selected text is copied and pasted into the command editor through the clipboard seam                            |

### Workspaces, tabs, and panes

| ID  | User-facing action family      | Capability operation(s)                                                 | Intended agent entry point                                               | Headless acceptance scenario                                                                                   |
| --- | ------------------------------ | ----------------------------------------------------------------------- | ------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------- |
| L01 | Create a workspace             | `LayoutOperation`                                                       | `assistant.ui.execute("workspace.create")`                               | `workspace.create`: a named workspace appears in the persisted layout                                          |
| L02 | Switch the active workspace    | None — current selection is renderer state                              | `assistant.ui.execute("workspace.select")` through the renderer executor | `workspace.select`: the selected workspace becomes visible without changing another workspace's panes          |
| L03 | Rename or recolour a workspace | `LayoutOperation`                                                       | `assistant.ui.execute("workspace.update")`                               | `workspace.update`: explicit workspace id persists both the name and colour                                    |
| L04 | Reorder workspaces             | `LayoutOperation`                                                       | `assistant.ui.execute("workspace.reorder")`                              | `workspace.reorder`: an explicit id list is returned and survives a reload                                     |
| L05 | Close a workspace              | `LayoutOperation` + `SessionOperation` (adapted)                        | `assistant.ui.execute("workspace.close")`                                | `workspace.close`: the named workspace and its sessions close, while the replacement workspace remains visible |
| L06 | Create a tab                   | `LayoutOperation`                                                       | `assistant.ui.execute("tab.create")`                                     | `tab.create`: the new tab has the requested workspace and position                                             |
| L07 | Switch the active tab          | None — current selection is renderer state                              | `assistant.ui.execute("tab.select")` through the renderer executor       | `tab.select`: the named tab becomes visible and no other tab is mutated                                        |
| L08 | Rename or recolour a tab       | `LayoutOperation`                                                       | `assistant.ui.execute("tab.update")`                                     | `tab.update`: the named tab's title and colour are persisted                                                   |
| L09 | Pin or unpin a tab             | `LayoutOperation`                                                       | `assistant.ui.execute("tab.pin")`                                        | `tab.pin`: the explicit tab pin state is returned and reflected in the strip                                   |
| L10 | Reorder tabs                   | `LayoutOperation`                                                       | `assistant.ui.execute("tab.reorder")`                                    | `tab.reorder`: an explicit ordered id list is persisted                                                        |
| L11 | Close a tab                    | `LayoutOperation` + `SessionOperation` (adapted)                        | `assistant.ui.execute("tab.close")`                                      | `tab.close`: the named tab closes and the selected replacement is deterministic                                |
| L12 | Split a pane or create a pane  | `LayoutOperation` + `OpenOperation` (adapted where a session is opened) | `assistant.ui.execute("pane.create")`                                    | `pane.create`: a new pane is created at the explicit location and, when requested, receives a live session     |
| L13 | Move a pane                    | `LayoutOperation`                                                       | `assistant.ui.execute("pane.move")`                                      | `pane.move`: the named pane moves to the explicit tab/workspace destination                                    |
| L14 | Close a pane                   | `LayoutOperation` + `SessionOperation` (adapted)                        | `assistant.ui.execute("pane.close")`                                     | `pane.close`: the named pane closes, its session is accounted for, and the replacement is visible              |

### Connections, profiles, and groups

| ID  | User-facing action family                   | Capability operation(s)                               | Intended agent entry point                           | Headless acceptance scenario                                                                                                    |
| --- | ------------------------------------------- | ----------------------------------------------------- | ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| C01 | Create, edit, or delete a saved SSH profile | `ConfigOperation`                                     | `assistant.ui.execute("profile.mutate")`             | `profile.mutate`: create/update/delete by explicit id and read back the exact profile projection                                |
| C02 | Test a saved connection                     | None — connection test is an external probe seam      | `assistant.ui.execute("profile.test")`               | `profile.test`: held SSH fixture returns success and refusal results without changing the saved profile                         |
| C03 | Trust or reject a host key                  | None — host-key decision is an SSH prompt seam        | `assistant.ui.execute("connection.hostKeyDecision")` | `connection.hostKeyDecision`: a changed key is refused until explicit trust, then one connection succeeds                       |
| C04 | Answer a connection password prompt         | None — prompt response is a transport/credential seam | `assistant.ui.execute("connection.password")`        | `connection.password`: the held SSH fixture reaches authentication and the password is not included in the durable command text |
| C05 | Import SSH config aliases                   | `ConfigOperation`                                     | `assistant.ui.execute("profiles.importSSHConfig")`   | `profiles.importSSHConfig`: a temporary config imports aliases and reports created/skipped rows                                 |
| C06 | Import Tabby profiles                       | `TabbyImportOperation`                                | `assistant.ui.execute("profiles.importTabby")`       | `profiles.importTabby`: a held Tabby document imports profiles/groups with collision decisions explicit in the result           |
| C07 | Create or edit a profile group              | `ConfigOperation`                                     | `assistant.ui.execute("group.mutate")`               | `group.mutate`: the named group is created/updated and its members are read back                                                |
| C08 | Move profiles into or out of a group        | `ConfigOperation`                                     | `assistant.ui.execute("group.membership")`           | `group.membership`: explicit profile and group ids produce exactly the requested membership                                     |
| C09 | Delete a profile group                      | `ConfigOperation`                                     | `assistant.ui.execute("group.delete")`               | `group.delete`: the group is removed and profiles remain available with their defined fallback state                            |

### Endpoints, roles, and assistant provider settings

| ID  | User-facing action family                         | Capability operation(s)                     | Intended agent entry point                       | Headless acceptance scenario                                                                                                 |
| --- | ------------------------------------------------- | ------------------------------------------- | ------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------- |
| E01 | Create, edit, or delete an assistant endpoint     | `ConfigOperation`                           | `assistant.ui.execute("endpoint.mutate")`        | `endpoint.mutate`: endpoint credentials are stored as references and the returned endpoint omits material                    |
| E02 | Probe an endpoint/model                           | `ConfigOperation` plus assistant probe seam | `assistant.ui.execute("endpoint.probe")`         | `endpoint.probe`: a held model server receives the draft URL/model/headers and the bounded result reports success or refusal |
| E03 | Assign or clear an assistant role                 | `ConfigOperation`                           | `assistant.ui.execute("role.assign")`            | `role.assign`: the explicit role resolves to the requested endpoint/model and reads back through `agent.status`              |
| E04 | Set or clear the default assistant model/provider | `ConfigOperation`                           | `assistant.ui.execute("assistant.defaultModel")` | `assistant.defaultModel`: the selected role/provider is persisted and the next ask uses it                                   |

### Vault and secrets

| ID  | User-facing action family                          | Capability operation(s) | Intended agent entry point                       | Headless acceptance scenario                                                                                                        |
| --- | -------------------------------------------------- | ----------------------- | ------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------- |
| V01 | Set up the vault                                   | `VaultOperation`        | `assistant.ui.execute("vault.setup")`            | `vault.setup`: the vault is created with the explicit provider and reports ready without returning key material                     |
| V02 | Unlock or seal the vault                           | `VaultOperation`        | `assistant.ui.execute("vault.unlockOrSeal")`     | `vault.unlockOrSeal`: lock state changes and a subsequent secret operation observes the new state                                   |
| V03 | Change the vault passphrase                        | `VaultOperation`        | `assistant.ui.execute("vault.changePassphrase")` | `vault.changePassphrase`: the old passphrase stops working and the new one unlocks the same vault                                   |
| V04 | Regenerate recovery material                       | `VaultOperation`        | `assistant.ui.execute("vault.recovery")`         | `vault.recovery`: new recovery output is returned once, bounded, and the old recovery value is refused                              |
| V05 | Choose the vault provider or auto-seal policy      | `VaultOperation`        | `assistant.ui.execute("vault.policy")`           | `vault.policy`: explicit provider/policy is persisted and reflected in status                                                       |
| V06 | Create, replace, rename, or delete a stored secret | `SecretOperation`       | `assistant.ui.execute("secret.mutate")`          | `secret.mutate`: the named row changes while profile references resolve to the returned row handle                                  |
| V07 | Reset the vault and its references                 | `VaultResetOperation`   | `assistant.ui.execute("vault.reset")`            | `vault.reset`: explicit confirmation clears the vault and dependent references atomically, with refusal when confirmation is absent |

### Settings, backup, diagnostics, and shell integration

| ID  | User-facing action family           | Capability operation(s)                                            | Intended agent entry point                                                          | Headless acceptance scenario                                                                                                                               |
| --- | ----------------------------------- | ------------------------------------------------------------------ | ----------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| S01 | Change a normal setting             | `ConfigOperation`                                                  | `assistant.ui.execute("settings.set")`                                              | `settings.set`: the explicit setting value persists and is returned in the next settings read                                                              |
| S02 | Reset a setting to its default      | `ConfigOperation`                                                  | `assistant.ui.execute("settings.reset")`                                            | `settings.reset`: the named setting returns to its documented default                                                                                      |
| S03 | Manage a secret-valued setting      | `ConfigOperation` + `SecretOperation`                              | `assistant.ui.execute("settings.secret")`                                           | `settings.secret`: the setting stores a row reference, never material, and resolves after unlock                                                           |
| S04 | Create/save a backup                | `BackupOperation`                                                  | `assistant.ui.execute("backup.create")`                                             | `backup.create`: a backup is written through the injected file sink and its bounded result identifies the artifact                                         |
| S05 | Preview or restore a backup         | `BackupOperation`                                                  | `assistant.ui.execute("backup.restore")`                                            | `backup.restore`: a held backup preview is followed by an explicit restore and the restored state is readable                                              |
| S06 | Install or repair shell integration | `SessionOperation` (adapted: session ownership)                    | `assistant.ui.execute("shell.integrate")`                                           | `shell.integrate`: the named session receives the integration and the held shell emits the expected markers                                                |
| S07 | Uninstall shell integration         | `SessionOperation` (adapted)                                       | `assistant.ui.execute("shell.unintegrate")`                                         | `shell.unintegrate`: the named session stops emitting integration markers                                                                                  |
| S08 | Copy diagnostics/about information  | None — renderer/clipboard action                                   | `assistant.ui.execute("diagnostics.copy")` through the renderer executor            | `diagnostics.copy`: the clipboard contains the bounded diagnostic text and no secret material                                                              |
| S09 | Resize the sidebar                  | `UIStateOperation` — **Excluded** as a presentation-only operation | `assistant.ui.execute("sidebar.resize")` only through an explicit renderer executor | `sidebar.resize`: the user-visible width changes and the persisted layout survives reload; the assistant must not call `UIStateOperation` as a domain tool |

### Files

| ID  | User-facing action family                           | Capability operation(s)                                                                        | Intended agent entry point                                           | Headless acceptance scenario                                                                                                                          |
| --- | --------------------------------------------------- | ---------------------------------------------------------------------------------------------- | -------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| F01 | Open/refresh the Files panel and browse a directory | `FilesystemOpenOperation` + `FilesystemBindingOperation` (adapted: connection/session binding) | `assistant.ui.execute("files.browse")`                               | `files.browse`: explicit session/root opens one binding and lists the expected entries                                                                |
| F02 | Open and read a file                                | `FilesystemBindingOperation` (adapted)                                                         | `assistant.ui.execute("files.read")`                                 | `files.read`: bounded byte/text window is returned for the named binding/path                                                                         |
| F03 | Reveal a file path in the host file manager         | `FilesystemBindingOperation` (adapted; local-only revealer seam)                               | `assistant.ui.execute("files.reveal")`                               | `files.reveal`: local provider invokes the held file-manager executable; remote provider returns the defined refusal                                  |
| F04 | Upload a local file                                 | `FilesystemBindingOperation` (adapted)                                                         | `assistant.ui.execute("files.upload")`                               | `files.upload`: a source ticket is redeemed once, bytes arrive at the explicit destination, and the terminal notification is retained across reattach |
| F05 | Download a remote file                              | `FilesystemBindingOperation` (adapted)                                                         | `assistant.ui.execute("files.download")`                             | `files.download`: the explicit remote path writes to the held local sink and reports a terminal outcome                                               |
| F06 | Cancel an upload or download                        | `FilesystemBindingOperation` (adapted)                                                         | `assistant.ui.execute("files.transferCancel")`                       | `files.transferCancel`: the named transfer becomes cancelled and a repeated cancel is idempotent                                                      |
| F07 | Filter the file list or clear the filter            | None — renderer-local filtering                                                                | `assistant.ui.execute("files.filter")` through the renderer executor | `files.filter`: the visible rows match the explicit query and clearing restores all rows                                                              |

### Git

| ID  | User-facing action family                          | Capability operation(s)                                                       | Intended agent entry point                                           | Headless acceptance scenario                                                                             |
| --- | -------------------------------------------------- | ----------------------------------------------------------------------------- | -------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| G01 | Open a repository                                  | `GitOpenOperation` (adapted: session/helper selection and connection binding) | `assistant.ui.execute("git.open")`                                   | `git.open`: explicit session/cwd opens one repository binding and returns its id                         |
| G02 | Refresh repository status, log, or remote metadata | `GitBindingOperation` (adapted)                                               | `assistant.ui.execute("git.refresh")`                                | `git.refresh`: the named binding returns bounded status/log/remote data after a held repository mutation |
| G03 | View a diff                                        | `GitBindingOperation` (adapted)                                               | `assistant.ui.execute("git.diff")`                                   | `git.diff`: explicit path/ref returns the expected bounded diff, including a capped result               |
| G04 | Stage or unstage selected paths                    | `GitBindingOperation` (adapted)                                               | `assistant.ui.execute("git.stage")`                                  | `git.stage`: explicit path list changes index state and the returned status proves it                    |
| G05 | Stage or unstage all changes                       | `GitBindingOperation` (adapted)                                               | `assistant.ui.execute("git.stageAll")`                               | `git.stageAll`: all and only the repository's current changes are staged/unstaged                        |
| G06 | Commit staged changes                              | `GitBindingOperation` (adapted)                                               | `assistant.ui.execute("git.commit")`                                 | `git.commit`: a held pre-commit hook is released, one commit is made, and the exact message is read back |
| G07 | Open a hosting-service link                        | None — renderer/external URL action                                           | `assistant.ui.execute("git.openLink")` through the renderer executor | `git.openLink`: the explicit URL reaches the held opener and no unrelated URL is opened                  |
| G08 | Accept or refuse remote-helper consent             | None — consent is a transport policy seam                                     | `assistant.ui.execute("git.helperConsent")`                          | `git.helperConsent`: refusal prevents helper use; approval allows exactly one helper-backed operation    |

### Notes and snippets

| ID  | User-facing action family                | Capability operation(s) | Intended agent entry point                                                            | Headless acceptance scenario                                                                                   |
| --- | ---------------------------------------- | ----------------------- | ------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| N01 | List, search, or open notes              | `NoteOperation`         | `assistant.ui.execute("notes.search")`                                                | `notes.search`: an explicit query returns bounded rows and opening one returns its body                        |
| N02 | Create or edit a note                    | `NoteOperation`         | `assistant.ui.execute("notes.create")` / `assistant.ui.execute("notes.update")`       | `notes.create` requires body without id; `notes.update` requires an explicit note id and body                  |
| N03 | Delete a note                            | `NoteOperation`         | `assistant.ui.execute("notes.delete")`                                                | `notes.delete`: the named note is gone and a repeated delete returns the defined result                        |
| P01 | Browse/manage the snippets palette       | `SnippetOperation`      | `assistant.ui.execute("snippets.list")`                                               | `snippets.list`: the ordered snippet rows are returned and the selected row is addressable by id               |
| P02 | Create or edit a snippet                 | `SnippetOperation`      | `assistant.ui.execute("snippets.create")` / `assistant.ui.execute("snippets.update")` | `snippets.create` requires title/body without id; `snippets.update` requires an explicit snippet id/title/body |
| P03 | Delete a snippet                         | `SnippetOperation`      | `assistant.ui.execute("snippets.delete")`                                             | `snippets.delete`: the named snippet disappears and a second delete is idempotent                              |
| P04 | Reorder snippets                         | `SnippetOperation`      | `assistant.ui.execute("snippets.reorder")`                                            | `snippets.reorder`: the explicit id order is persisted                                                         |
| P05 | Insert a snippet into the command editor | `SnippetOperation`      | `assistant.ui.execute("snippets.insert")` through the renderer executor               | `snippets.insert`: the selected snippet text reaches the editor at the requested insertion point               |

### API workbench

| ID  | User-facing action family                               | Capability operation(s)  | Intended agent entry point                    | Headless acceptance scenario                                                                                           |
| --- | ------------------------------------------------------- | ------------------------ | --------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| A01 | Open, create, or close an API collection                | `APICollectionOperation` | `assistant.ui.execute("api.collection")`      | `api.collection`: explicit collection actions persist and return one collection projection                             |
| A02 | Open or create an API folder                            | `APICollectionOperation` | `assistant.ui.execute("api.folder")`          | `api.folder`: the folder is created/opened under the named collection                                                  |
| A03 | Create, edit, duplicate, move, or delete an API request | `APICollectionOperation` | `assistant.ui.execute("api.request")`         | `api.request`: each explicit request mutation is reflected in the collection tree without duplicate rows               |
| A04 | Create or edit an API environment                       | `APICollectionOperation` | `assistant.ui.execute("api.environment")`     | `api.environment`: environment values are persisted with references unresolved where required                          |
| A05 | Edit folder variables                                   | `APICollectionOperation` | `assistant.ui.execute("api.folderVariables")` | `api.folderVariables`: explicit folder id/value changes are returned and scoped to that folder                         |
| A06 | Send an API request                                     | `APICollectionOperation` | `assistant.ui.execute("api.send")`            | `api.send`: a held HTTP server receives the resolved request and the bounded response is rendered                      |
| A07 | Stop an API request                                     | `APICollectionOperation` | `assistant.ui.execute("api.stop")`            | `api.stop`: a held request is cancelled and no later response replaces the cancelled result                            |
| A08 | Import a Postman collection                             | `APIImportOperation`     | `assistant.ui.execute("api.importPostman")`   | `api.importPostman`: a held fixture is imported, unsupported entries are reported, and supported requests are readable |
| A09 | Import a curl command                                   | `APIImportOperation`     | `assistant.ui.execute("api.importCurl")`      | `api.importCurl`: the explicit curl text becomes one request with the expected method, URL, headers, and body          |

### Notifications, ports, and policy

| ID  | User-facing action family                 | Capability operation(s)                                                                   | Intended agent entry point                                                     | Headless acceptance scenario                                                                                |
| --- | ----------------------------------------- | ----------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------- |
| R01 | Filter or view notifications              | None — notification feed is a renderer read model                                         | `assistant.ui.execute("notifications.filter")` through the renderer executor   | `notifications.filter`: explicit filter changes the visible feed and does not mutate notification records   |
| R02 | Mark a notification read                  | None — notification acknowledgement is feed state                                         | `assistant.ui.execute("notifications.markRead")` through the renderer executor | `notifications.markRead`: the named notification changes to read exactly once                               |
| R03 | Discover, refresh, pause, or resume ports | None — discovery is a transport/OS probe seam                                             | `assistant.ui.execute("ports.discover")`                                       | `ports.discover`: a held fixture produces deterministic discovered rows and pause/resume controls the probe |
| R04 | Forward a local/remote port               | None — tunnel ownership is connection/transport state; no capability operation is present | `assistant.ui.execute("ports.forward")`                                        | `ports.forward`: a held socket carries bytes through the requested forward and returns its handle           |
| R05 | Stop a port forward                       | None — tunnel lifecycle is transport state                                                | `assistant.ui.execute("ports.stop")`                                           | `ports.stop`: the named forward closes and subsequent bytes are refused                                     |
| R06 | Copy or open a forwarded address          | None — renderer/URL action                                                                | `assistant.ui.execute("ports.openAddress")` through the renderer executor      | `ports.openAddress`: the explicit address reaches the held opener/clipboard seam                            |
| R07 | Edit assistant policy rules               | None — policy store/approval seam is not an `internal/capability` operation               | `assistant.ui.execute("agent.policy")`                                         | `agent.policy`: a rule is saved, then the next matching proposal is refused or allowed as specified         |

## Where the authority now lives

This document produced the classification; it no longer holds it. Each operation
in `internal/capability` carries its disposition as a required field, and an
operation without one does not construct — so the code is what a reader should
trust when the two disagree, and this map is the analysis that argued for it.
`TestOperationDispositionsMatchMap` pins the code against an independent list;
nothing mechanically checks this prose, so treat a difference as a stale
document rather than as two competing answers.

## Operation classification

The following is the complete classification of the 23 operation interfaces. A
classification applies to the operation surface, not to every transport method that
currently calls it. In particular, a Direct operation can still be reached by a
connection-bound UI handler; that handler is the reason the corresponding action row
says `Adapted`.

### Direct (14)

| Operation                | Classification |
| ------------------------ | -------------- |
| `AgentOperation`         | Direct         |
| `APICollectionOperation` | Direct         |
| `APIImportOperation`     | Direct         |
| `BackupOperation`        | Direct         |
| `CaptureSaveOperation`   | Direct         |
| `TabbyImportOperation`   | Direct         |
| `ConfigOperation`        | Direct         |
| `ContentOperation`       | Direct         |
| `LayoutOperation`        | Direct         |
| `NoteOperation`          | Direct         |
| `SecretOperation`        | Direct         |
| `SnippetOperation`       | Direct         |
| `VaultOperation`         | Direct         |
| `VaultResetOperation`    | Direct         |

At least five Direct classifications are confirmed directly from the operation
signatures: `AgentOperation` (`internal/capability/agent.go:63-71`),
`APICollectionOperation` (`internal/capability/api.go:403-424`),
`BackupOperation` (`internal/capability/backup.go:20-55`),
`ConfigOperation` (`internal/capability/config.go:162-182`),
`ContentOperation` (`internal/capability/content.go:36-44`),
`LayoutOperation` (`internal/capability/layout.go:68-76`), and
`NoteOperation` (`internal/capability/note.go:28-36`). Their constructors accept
admissions and domain services, not `*wsConn` or `*connState`; their callbacks receive
only `context.Context` and the guarded service. This is the required no-connection-state
check, not a claim that all callers are currently headless.
The three potentially human-brokered domains remain Direct under criterion 1 when
the criterion is applied to the operation's own call path:

- `VaultOperation`: lifecycle handlers construct and pass explicit
  `SetupRequest`, `UnsealRequest`, and `ChangePassphraseRequest` values into the
  capability service (`internal/transport/ws_vault.go:268-275`,
  `internal/transport/ws_vault.go:323-344`, and
  `internal/transport/ws_vault.go:402-415`). Those paths call `svc.Setup`,
  `svc.Unseal`, or `svc.ChangePassphrase`; they do not call `EnsureUnsealed`.
  `EnsureUnsealed` is the separate secret-read path that can invoke
  `RequestUnlock` (`internal/vault/unlock.go:63-87`). An explicit passphrase,
  recovery code, or OS-key choice is therefore an operation input, not a
  reverse renderer request.
- `VaultResetOperation`: its callback is only `Preview(ctx)` or `Execute(ctx)`
  (`internal/capability/vault.go:145-173`). The transport calls those methods
  directly and returns their bounded result (`internal/transport/ws_vault_reset.go:61-97`);
  no requester or renderer-resolution seam is reachable from the reset
  orchestrator. The preview/execute boundary is the explicit confirmation
  protocol at the surrounding surface; it is not a reverse renderer request
  from the operation.
- `BackupOperation`: restore validates explicit `contents`, `strategy`, and
  `previewToken`, then calls `svc.Restore` inside `operation.Run` and returns its
  result (`internal/transport/ws_backup.go:186-207`). There is no responder ask
  inside that call path. A human may still be required to approve the destructive
  effect at the assistant-policy layer.

`ConfigOperation` also remains Direct. Its endpoint secret writes reach vault
methods that return `ErrVaultSealed` when sealed rather than raising an unlock
prompt (`internal/vault/vault.go:1060-1075` and
`internal/vault/vault.go:1593-1604`). `connections.trustHostKey` and
`connections.passwordRequest` are separate transport/connection seams, not
calls reachable through `ConfigOperation` (`internal/transport/ws_probe.go:325-339`
and `internal/transport/password_requester.go:11-38`).

Human-brokered asks are a separate axis from Direct/Adapted classification. Unlock,
host-key, password, and destructive-effect confirmation may be required by a
user-facing flow even when the domain operation itself is Direct. The assistant
façade must represent those asks explicitly; in particular, an assistant that
cannot ask for an unlock cannot restore a backup either. Direct does not mean
“the whole UI flow never needs a human”; it means only that the operation does not
itself reverse-call the renderer.

### Adapted (7)

Each Adapted operation needs the following façade changes before direct assistant
projection:

| Operation                    | Why it is Adapted                                                                                                                                          | Required façade change                                                                                                                            |
| ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `OpenOperation`              | `openHandlers` uses the websocket as identity and registers the opened session with connection state (`internal/transport/ws_session_handlers.go:94-106`). | Mint/attach a session owner for the assistant run, accept an explicit profile id, and return a bounded session descriptor plus lifecycle outcome. |
| `SessionOperation`           | Resize/close/attach handlers enforce the requesting connection's session ownership (`internal/transport/ws_session_handlers.go:624-628`).                  | Carry an explicit assistant-owned session scope and reject ids outside it; return bounded state rather than relying on the current pane.          |
| `SessionTargetOperation`     | Completion and other remote session work first use `connState` to resolve the immutable session target (`internal/transport/ws_complete.go:51-53`).        | Accept a session id in the assistant scope and return the copied route/operation result without a connection lookup.                              |
| `FilesystemOpenOperation`    | Files open resolves a session and registers a provider/binding; the handler receives per-call connection state (`internal/transport/ws_files.go:430-444`). | Make the assistant façade mint an explicit session-scoped binding and return the binding/endpoint attestation.                                    |
| `FilesystemBindingOperation` | Every `files.*` call uses the requesting connection's binding/session ownership check (`internal/transport/ws_files.go:535-543`).                          | Carry an assistant-owned binding lease/caller identity and return bounded list/read/transfer results.                                             |
| `GitOpenOperation`           | `git.open` needs `connState` and selects a local/helper factory for the session (`internal/transport/ws_git.go:532-549`).                                  | Resolve an explicit assistant session/cwd and helper consent, then return a repository binding owned by the assistant scope.                      |
| `GitBindingOperation`        | Git binding acquisition validates a connection-scoped caller and can close asynchronously (`internal/capability/git.go:137-156`).                          | Carry an assistant-owned repository lease, bound reads, and make mutation/close idempotency explicit.                                             |

These seven are the only operation interfaces classified Adapted. The façade must
not pass a websocket or renderer object into `internal/capability`; it owns the
transport-to-domain conversion at the composition boundary.

### Excluded (2)

| Operation          | Why it is Excluded                                                                                                                                                                                                                                                     |
| ------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `LedgerOperation`  | It is the assistant/content transaction's internal durable entry lifecycle (`internal/capability/ledger.go:75-85`), not a user-visible action. Exposing it would let a tool mutate the assistant's audit record instead of performing the action being audited.        |
| `UIStateOperation` | It persists renderer presentation state (`internal/capability/uistate.go:30-38`), such as layout facts, rather than a domain action. The assistant may request a named renderer action such as sidebar resize, but must not receive a generic presentation-state tool. |

## Methods that are not user-facing actions

These are intentionally covered once rather than inflated into the 95-action count:

- JSON-RPC acknowledgements, response correlation, and resolved callbacks.
- Notifications such as `files.changed`, `files.uploadProgress`,
  `files.uploadDone`, `git.changed`, `session.ended`, and assistant status updates.
- Connection/session handoff plumbing, helper consent plumbing, and stream/data-plane
  bytes.
- Renderer-only selection, filtering, focus, clipboard, URL-open, and current-pane
  state transitions unless the table explicitly names them as a user action family.

They are observable inputs or consequences of an action. They are not additional
assistant tools.

## Coverage result

- **23** capability operation interfaces classified: **14 Direct**, **7 Adapted**,
  **2 Excluded**.
- **95** user-facing UI action families mapped.
- **21** action families have no `internal/capability` operation behind them:
  T06, T08, T09, T11, T13, L02, L07, C02, C03, C04, S08, F07, G07, G08,
  R01, R02, R03, R04, R05, R06, and R07. These are findings, not omitted rows.
- **74** action families have at least one capability operation behind them. Some
  compose a capability with a transport or renderer seam; that is why action coverage
  and operation classification are separate counts.

The 21 no-operation findings are not omissions. They identify the renderer, OS,
transport, policy, and data-plane seams that a generic assistant UI façade must expose
or deliberately leave outside the assistant contract.
