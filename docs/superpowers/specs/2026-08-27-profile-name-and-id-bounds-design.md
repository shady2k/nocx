---
title: Profile, group, and endpoint name and ID bounds
status: accepted
created: 2026-08-27
related: nocx-jb20.8, nocx-jb20
review: BMAD Mary/John/Winston/Amelia/Paige roles applied
---

# Profile, group, and endpoint name and ID bounds

## 1. Problem

The backend mints namespaced IDs from a namespace, a slugified display name,
and a 32-hex UUID suffix:

```text
<namespace>:custom:<slug>:<uuid>
```

Transport accepts display names up to 200 runes and domain IDs up to 128 runes.
Before this change, slugification had no output budget, so a valid 200-rune name
could produce an ID longer than the transport would accept on a later update.
The same sink is shared by profile, group, and endpoint IDs.

The contract is inclusive at ingress: names of 200 runes and IDs of 128 runes
are accepted; 201 and 129 are rejected. Every newly minted namespaced ID must
also be no longer than 128 runes.

## 2. Decisions

### 2.1 One owner for the ID bound

`internal/profile` owns the shared exported `MaxIDRunes = 128` constant because
that package mints all three namespaced ID types. Transport aliases this value
for renderer-supplied profile-domain IDs. The display-name bound remains the
transport-owned `maxConfigNameRunes = 200`.

The following constructors use one helper:

- `NewProfileID(typ, name)`
- `NewGroupID(name)`
- `NewEndpointID(name)`

No renderer-side ID generation or compatibility shim is introduced.

### 2.2 Bound the derived slug, not the stored name

The display name is stored verbatim and remains available for lists and edits.
Only the derived slug is truncated. This preserves the existing 200-rune name
contract instead of silently reducing it to the smaller slug budgets.

The helper first bounds the namespace to the available prefix budget, then
allocates the remaining budget to the slug. Truncation always occurs on a UTF-8
rune boundary. The UUID remains unchanged and supplies uniqueness when two names
share a truncated prefix.

The fixed suffix is `:custom:` plus one separator and 32 hex runes:

```text
len(":custom:") + 1 + 32 = 41 runes
128 - 41 = 87 runes for namespace plus slug
```

Therefore the normal slug budgets are:

| Constructor                 | Namespace  | Slug budget |
| --------------------------- | ---------- | ----------: |
| `NewProfileID("ssh", name)` | `ssh`      |          84 |
| `NewGroupID(name)`          | `group`    |          82 |
| `NewEndpointID(name)`       | `endpoint` |          79 |

An oversized namespace is also capped, so the sink remains bounded for every
caller. Unicode names are lowercased before the ASCII slug allowlist is applied;
characters such as Kelvin sign (`K`) retain the historical lowercase behavior.

### 2.3 Validate SSH hosts before option parsing

An SSH host beginning with `-` is not a positional destination: OpenSSH parses
it as an option. The shared `ssh.IsOptionLikeHost` predicate is applied at both
boundaries that can receive a host:

- direct SSH open requests are refused before resolver or dialer work;
- stored profile options are refused before `ssh -G` configuration resolution,
  including when the connection resolver has no config resolver.

Typed `ResolveArgv` arguments remain typed argv data and are not changed by this
boundary check. The backend does not defer this decision to the SSH subprocess.

### 2.4 Remove obsolete `needsReview` state

`needsReview` is not part of the current profile contract. The persisted profile,
backup document, import result, RPC schemas, and service API no longer carry a
review flag or clear-review operation. JSON decoding remains strict, so an old
field is rejected rather than silently retained or migrated.

## 3. Contract surface

The following parameter contracts use the domain bounds:

- profile create/update: `id` 128, `group` 128, `name` 200;
- group create/update/apply/impact: group IDs 128, names 200;
- endpoint create/update/probe: endpoint IDs 128 where supplied, names 200;
- profile move-impact: profile IDs and target group ID 128.

Icon, color, URL, key, model, and header limits remain their existing
field-specific limits. The JSON schemas and registered runtime validators are
checked together at both inclusive boundaries, including nested group-impact
and array-shaped group-apply requests.

### 3.1 The bound reaches every surface that takes a profile ID

`profile.MaxIDRunes` is the owner only if every profile-id check reads it.
`validateProfileID` did not: it measured a profile id against `maxIDRunes`,
the agent surface's ask-and-attached-item bound, which is 128 today and is a
different concept. It now reads `maxConfigIDRunes` and lives in
`ingress_bounds.go`, which is where that file's own rule puts a bound shared
by more than one domain — `ports.status/sample/pause/visible`, `tunnel.open`,
`connections.test` and `shell.footprint.uninstall/helperUninstall` all reach
it (nocx-ms4xq).

Their contracts published 256 and 512 for the same field, so a renderer built
to the contract would be answered `-32602` for an id the contract called
legal. All eight now declare 128, and the parity table covers them.

The same class remains outside the profile domain — agent, ledger, lifecycle,
vault, api and uistate ids and names whose declared `maxLength` outruns their
validator — measured and recorded in nocx-o446h rather than fixed here.

## 4. Behavioral verification

Permanent regressions cover:

- every constructor producing parseable IDs no longer than 128 runes;
- inclusive slug budgets and truncation at budget plus one;
- Unicode lowercase preservation and UUID-based uniqueness;
- option-like SSH hosts rejected without spawning `ssh` or reaching a dialer;
- profile RPC refusing option-like stored hosts without persistence;
- direct open refusing option-like hosts before the SSH dialer;
- probe key-file read and parse failures sharing one public error without path
  disclosure;
- obsolete `needsReview` backup input rejected;
- contract/runtime parity for 128/129 ID and 200/201 name boundaries, over the
  profile, group and endpoint methods and the eight seam methods that take a
  profile id.

The existing stale-subscriber acknowledgement ownership regression on the current
main branch is retained and verified; no old PR ack rewrite is ported because
that behavior is already fixed upstream.

## 5. Affected files

- `internal/profile/profile.go`, `internal/profile/endpoint.go`
- `internal/ssh/ssh_resolver.go`, `internal/ssh/auth_chain_test.go`
- `internal/connection/resolver.go`
- `internal/transport/ingress_bounds.go`, `ws_config_handlers.go`,
  `ws_seam_specs.go`, `ws_session_handlers.go`, and regression tests
- `internal/backup/document.go`, `internal/backup/service.go`, and tests
- profile/group/endpoint parameter schemas under `contracts/`
- `docs/architecture.md`

No migration rewrites existing stored IDs. The new invariant governs IDs minted
from now on, while strict current contracts reject removed fields and invalid
new ingress values.
