# Backup & Restore

**Path:** Settings → Backup & Restore

## Overview

nocx provides a structured Backup & Restore feature that exports your configuration
as a versioned, human-readable JSON file. The backup contains your non-secret settings,
SSH connections, and connection groups — without credential records, secret references,
or keychain material.

This document describes the backup format, what is included and excluded, restore
strategies, and crash-recovery behaviour.

## Accessing Backup & Restore

1. Open **Settings** from the main toolbar.
2. Select the **Backup & Restore** page from the left navigation.

## Creating a Backup

Click **Create backup** to generate a versioned JSON file. The file is saved to your
downloads folder with a timestamped name: `nocx-backup-YYYYMMDDTHHMMSSZ.json`.

The create result shows counts of:
- Settings overrides included
- Connections (SSH profiles) included
- Groups included
- Credential bindings removed (connections that had a credential reference)
- Group credential bindings removed
- Group default keys omitted (unknown provider keys not in the safe subset)

## Backup Format (v1)

The backup file uses the `nocx-backup` format, version `1`. It is plaintext JSON
with the following structure:

```json
{
  "format": "nocx-backup",
  "version": 1,
  "createdAt": "2026-07-30T12:00:00Z",
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
        "defaults": {
          "ssh": {
            "options": {
              "port": 2222
            }
          }
        },
        "credentialBindingRemoved": true
      }
    ]
  }
}
```

### What is included

- **Settings overrides:** All saved non-secret values from the Settings Registry
  (`PublicConfig`, `PrivateMetadata`, and `PrivateContent` data classes).
- **SSH connections:** Full profile identity and options — host, port, user,
  auth mode, keepalive, agent forwarding, and all configurable fields — except
  `credentialId`.
- **Groups:** Group names, icons, colors, parent relationships, and a typed
  subset of `defaults.ssh.options` (host, port, user, auth, keepaliveInterval,
  keepaliveCountMax, readyTimeout, jumpHost, agentForward, canBeJumpServer).

### What is excluded

- **Credential records (УЗ):** Credential metadata is stored in the same
  `profiles.json` document but is never exported.
- **CredentialId:** No `credentialId` field appears on any backup profile or
  group default.
- **Secret references:** `SecretID`, `PassphraseSecretID`, and any keychain
  reference are absent.
- **SecretStore material:** Passwords, key passphrases, and all OS keychain
  entries are never read or written during backup/restore.
- **ContentDB:** AI conversations, command history, and `content.db` are not
  included.
- **Declared defaults:** Only user-saved overrides are exported, not the
  settings' declared defaults.
- **Secret-class settings:** Settings with `SecretAuthenticator` data class
  are excluded.

### File size limit

The backup file must not exceed **8 MiB** (8,388,608 bytes) of UTF-8 JSON.
Files exceeding this limit are rejected on both create and restore.

## Restoring a Backup

### Choosing a file

Use the file picker to select a previously created backup file (the `nocx-backup` format, version 1).
The file is read client-side and validated before preview.

### Restore strategies

Two strategies are available:

| Strategy  | Behaviour |
|-----------|-----------|
| **Merge** (default) | Backup overrides win for matching keys. Local settings without a backup counterpart are kept. Matching connections receive backup-owned fields. New connections from the backup are added. Local connections not in the backup are kept. Direct credential binding on a matching connection survives only when host and effective port are unchanged. Group-level credential binding is always cleared. |
| **Replace** | Non-secret settings become exactly the backup set; others reset to defaults. Connections and groups become exactly the backup set, in backup order. All credential bindings are removed. Credential metadata and keychain entries are untouched. |

### Preview and confirmation

Before restore executes, you see a preview showing:
- **Settings:** Included, changed, and reset counts.
- **Connections:** Included, added, updated, and removed counts.
- **Groups:** Included, added, updated, and removed counts.
- **Connections requiring credential:** List of profiles (ID + name) that had
  a credential binding in the backup and need a new credential assigned after
  restore.
- **Omissions:** Credential bindings removed from connections and groups,
  group default keys omitted from the backup.

The preview produces a **binding token** that ties the exact file contents,
strategy, and your current local state together. Restore must use this token;
if anything changes between preview and restore (e.g., you modify a setting
in another tab), the token is rejected and you must re-preview and re-confirm.

### Credential reassignment after restore

After a restore, connections that had `requiresCredential: true` in the backup
and no longer have a credential binding are listed in the result. You must
assign a local credential to each of these connections manually through the
Connections page.

## Crash Recovery

Restore uses a local crash-safe journal (`backup-restore-journal.json`). If
nocx crashes or is force-quit during a restore:

- If the crash occurs **before** the write is committed, the journal rolls
  back both connections and settings to their pre-restore state.
- If the crash occurs **after** the write is committed, the new state is kept
  and the journal is cleaned up on next start.
- If the journal is corrupt or unreadable, nocx enters **configuration
  recovery mode**: all configuration-mutating operations (backup, profiles,
  groups, credentials, settings, new sessions) are blocked until you restart
  nocx.

## Plaintext Warning

> **The backup file is plaintext JSON.** All settings values, hostnames,
> connection names, inline usernames, and auth modes are stored without
> encryption. Treat the backup file as private data — do not share it,
> upload it to untrusted services, or store it in unencrypted locations.
>
> Credential secrets (passwords, key passphrases) are never included, but
> the connection metadata (hosts, usernames) may still be sensitive.

## Version Compatibility

Backup format v1 accepts only v1 files. Future format versions will require
a migration or a new version of nocx. The backup format version is independent
of the app's internal schema versions.

## Recovery-Required Mode

If nocx detects an unrecoverable journal state, all configuration operations
return the error `Configuration recovery is required; restart nocx`. Restart
the application to clear this state. Existing terminal sessions are not
affected.
