# Backup & Restore

**Settings → Backup & Restore**

A backup is one JSON file holding your settings, SSH connections, connection groups
and filesystem sandbox defaults. You can read it, diff it and edit it in any text
editor, and restore its portable configuration onto another machine.

It contains **no passwords, no key passphrases and no credential records**. That is the
first thing to know about it, because it decides everything else on this page: a restored
connection knows which host it points at, and does not know how to authenticate to it until
you tell it.

## Creating a backup

Press **Create backup**. nocx opens your system's save dialog with a timestamped name —
`nocx-backup-20260812T143005Z.json`. Choose where it goes.

Cancel the dialog and nothing is written. The backup was built in memory and is discarded.

If there is no save dialog to open — a Linux box without `zenity`, or nocx running in a
browser — the file downloads instead.

Whichever way the file lands, the message tells you what went into it: settings
overrides, connections, groups and filesystem sandbox profiles included, plus the
credential bindings and group-default keys left behind
(see [What is left out](#what-is-left-out)).

## What is in the file

```json
{
  "format": "nocx-backup",
  "version": 1,
  "createdAt": "2026-08-12T14:30:05Z",
  "settings": {
    "overrides": {
      "tab.placement": "vertical",
      "clipboard.osc52Suppressed": true
    }
  },
  "connections": {
    "profiles": [
      {
        "id": "ssh:custom:myhost:abc123",
        "type": "ssh",
        "name": "My Server",
        "group": "g1",
        "options": {
          "host": "server.example.com",
          "port": 22,
          "user": "admin",
          "auth": "agent"
        },
        "requiresCredential": true
      }
    ],
    "groups": [
      {
        "id": "g1",
        "name": "Production",
        "defaults": { "ssh": { "options": { "port": 2222 } } },
        "credentialBindingRemoved": true
      }
    ]
  }
}
```

**Settings** — every override you have saved whose data class is public configuration,
private metadata or private content. Defaults you never changed are not written out, so the
file stays small and a restore never re-asserts a default that has since moved.

**Connections** — the whole SSH profile: host, port, user, auth mode, key path, keepalive,
jump host, agent forwarding, port forwards, and the rest. Everything except the credential.
A connection that had one carries `"requiresCredential": true` in its place.

**Groups** — name, icon, colour, parent, and ten SSH default fields: `port`, `user`,
`auth`, `keepaliveInterval`, `keepaliveCountMax`, `readyTimeout`, `jumpHost`,
`agentForward`, `desiredMode`, `portDiscovery`.

**Filesystem sandbox configuration** — the optional `sandbox` section holds the typed
standard document (`schemaVersion`, `revision`, `enabled`, `readOnlyDirs`,
`readWriteDirs`) and named workspace profiles (`workspaceId`, `revision`,
`override`). It contains future-launch defaults only. A backup never exports launch
grants, pane heads, helper bindings, runtime directories or observations, and
restoring configuration never creates, adopts or widens a running process.

Named workspace identities must already exist on the destination; an unknown ID
refuses restoration rather than creating layout or guessing a different workspace.
The default workspace always inherits standard. Imported revisions are not adopted:
successful import and rollback advance the destination's local CAS clocks.

### What is left out

- **Credentials.** Credential records live in the same `profiles.json` on disk and are
  never written to a backup.
- **Every `credentialId`.** The key does not appear on any object in the file.
- **Secret references** — `secretId`, `passphraseSecretId` and the group-default secret
  fields. A group that had one is marked `"credentialBindingRemoved": true`, which says a
  binding was dropped without naming which secret it was.
- **Keychain material.** No password or passphrase is read during create or written during
  restore. The keychain is not touched by either.
- **Command history and AI conversations** (`content.db`). Not in v1.
- **Live filesystem authority.** Launch grants, pane launch heads, helper bindings,
  diagnostic observations and runtime directories are never part of the portable
  configuration document.
- **Two group-default keys by name** — `keyPath` and `behaviorOnSessionEnd` — plus any key
  the format does not recognise. They are listed in `omittedDefaultKeys` on the group, and
  counted for you on the create and preview screens.

The file must stay under **8 MiB**. Larger files are rejected on create and on restore.

## Restoring a backup

Pick the file, choose a strategy, read the preview, confirm.

### Merge, or replace

**Merge** is the default and adds rather than overwrites. Settings in the backup win for
the keys they mention; your other settings are left alone. Connections and groups that
match by ID take the backup's values, connections only in the backup are added, and
connections only on this machine stay.

Merge keeps a connection's existing credential **only if the connection still points at the
same place** — same host, same effective port, reading an unset port as 22. If the backup
moves it to a different host, the binding is cleared, because a secret issued for one host
must not silently follow the connection to another. Group-level credential defaults are
always cleared: a group default is not tied to one host, so there is nothing to revalidate.

**Replace** makes this machine match the file. Settings become exactly the backup's set and
everything else returns to its default; connections and groups become exactly the backup's
list, in its order, with no credential bindings at all.

Neither strategy deletes a credential or a keychain entry. The backup cannot describe them,
so restoring one cannot destroy them.

When `sandbox` is included, both strategies restore its standard values. **Merge**
changes the listed workspace profiles and keeps unlisted profiles. **Replace**
also resets unlisted known workspace profiles to standard inheritance, without
deleting their workspace, tabs or panes. Older files without `sandbox` leave current
sandbox configuration untouched under either strategy. Existing process grants
remain unchanged; applying changed defaults to a shell requires a separately
confirmed new launch.

### The preview, and why it can go stale

Before anything is written you see:

- how many settings, connections and groups will be **added, updated, removed or reset**;
- **which connections will need a credential**, by name;
- **what was left out** — bindings dropped from connections and from groups, and
  group-default keys the format does not carry.

The preview is bound to the exact file, the strategy you chose and the state of this
machine at the moment it was computed. If any of those change before you press
confirm — a setting, connection or sandbox profile changes in another window —
the restore refuses before writing and recomputes the preview. Read the new
numbers; they describe a different state.

### After the restore

Connections listed as **requiring a credential** are connected to nothing until you assign
one. Open **Connections**, pick each, and give it a credential from this machine. Until you
do, the connection looks complete and will fail on connect.

## If nocx is interrupted mid-restore

A restore writes a journal before it touches anything, so there is no state where half of a
backup has been applied. If nocx is killed, crashes or loses power during one:

- interrupted **before** the write completed — the next start restores connections,
  settings and mutable sandbox configuration to their prior values; sandbox CAS
  clocks advance rather than rewind;
- interrupted **after** it completed — the restored state is correct and the next start
  just clears the journal.

Either way it happens at startup, before the window appears, and you do not have to do
anything.

Sandbox import/recovery owns an exclusive feature restore scope. Concurrent profile
edits and launch-grant CAS refuse while it is active; ordinary reads remain available.
Journal and other filesystem IO do not hold the configuration mutex. Startup restores
settings prerequisites before opening ContentDB and workspace/default configuration
before exposing transport.

If the journal itself is unreadable — a corrupt file, or one written by a newer nocx —
**nocx refuses to start** and reports the error rather than opening with a configuration it
cannot vouch for. Restore `~/.config/nocx` (or your platform's config directory) from a
copy, or remove the journal document if you accept losing the interrupted restore.

## Keep the file private

The backup carries no secrets, but it carries hostnames, usernames, jump hosts, port
forwards, changed settings and filesystem sandbox root paths — private configuration
about what you connect to and which directories future shells may access.

It is plaintext by design, so that it stays readable and repairable by hand. Encryption is
your filesystem's job: keep it on an encrypted volume, and do not upload it anywhere you
would not upload your `~/.ssh/config`.

## Versions

The file's `version` is the backup format's own, unrelated to nocx's internal schema
versions. Version 1 reads version 1 files and nothing else. A future format version will
come with a stated migration path.

## Design notes

The reasoning behind one file instead of the four export modes that came before it —
including why there is no encrypted export — is in
[ADR-0027](decisions/0027-structured-backup-and-restore.md).
