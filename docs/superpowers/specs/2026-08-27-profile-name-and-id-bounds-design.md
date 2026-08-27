---
title: Profile and group name and id bounds — cap the minted id
status: accepted
created: 2026-08-27
related: nocx-jb20.8, nocx-jb20
review: BMAD Mary/John/Winston/Amelia/Paige roles applied — requirements, JTBD, architecture, testability, documentation
---

# Profile and group name and id bounds — cap the minted id

## 1. Problem

A backend-minted profile or group id is `"<ns>:custom:<slug>:<uuid>"` — a
namespace, the literal `:custom:`, the slugified display name, and a 32-hex
random suffix. The transport already bounds the two renderer-supplied strings
that feed it:

- `maxConfigNameRunes = 200` — a display name (`internal/transport/ws_config_handlers.go:80`);
- `maxConfigIDRunes = 128` — a renderer-supplied id, and the same class of
  value the ask path bounds at `maxIDRunes = 128` (`ws_config_handlers.go:77`,
  `ws_agent.go:68`).

`profiles.create` / `groups.create` validate the name (≤ 200 runes) and then,
when the renderer sends no id, mint one from that name
(`ws_config_handlers.go:928-930, 1183-1185`) via `profile.NewProfileID` /
`profile.NewGroupID`. Those two functions slugify the name **without a length
cap** (`profile.go:984, 994`). `slugify` is one-rune-in, one-rune-out
(`profile.go:1014-1028`), so a 200-rune name yields a 200-rune slug, and the
minted id is

```
44 + 200 = 244 runes   (profile: "ssh:custom:" + 200 + ":" + 32 hex)
```

The backend therefore mints an id it would itself refuse: the same 244-rune id,
resubmitted as `profiles.update`'s `id`, fails `configIDRunes` at 128.

The bead's acceptance is wider than the profile/group mint: **name and id length
are capped both at the point they are minted and at the point they are accepted,
with a test at each boundary, and the cap is one constant, not one per call
site.**

## 2. Why the 200-name / 128-id pair is coherent — and where it stops being

The pair is coherent **at ingress**. A name and an id are independent,
renderer-supplied fields with independent contracts: a name is echoed in lists
and can be generous (200); an id is a store key and a resolver input and is
bounded tighter (128). Nothing requires them to agree, and they do not
interact — a renderer may send any `name ≤ 200` with any `id ≤ 128`.

The pair stops being coherent **at the mint**, which is the only place the two
fields become coupled. `slugify` preserves rune count, so the name's 200-rune
bound is silently inherited by the slug, and the id inherits the name's length
plus a fixed 44/46/49-rune frame — blowing through the id's own 128-rune bound.
One bound (200) leaks into the other's value space (128), and the backend
produces a value its own ingress would reject.

The fix is to make the mint respect the id bound without touching the name
bound: **truncate the slug at the mint**, not the name at the door.

## 3. Binding decisions

### 3.1 One owner, one constant: `internal/profile` owns the id bound

`internal/profile` is the only place a namespaced id is minted
(`NewProfileID`, `NewGroupID`, `NewEndpointID`), so it owns the minted-id
length invariant. The cap becomes a single exported constant:

```go
// internal/profile/profile.go
const (
    // MaxIDRunes is the one length cap for every backend-minted namespaced id
    // and for the renderer-supplied ids the transport accepts. The domain owns
    // it because it mints ids; the transport imports it rather than keeping a
    // second number that would drift (AD-8: two numbers that must agree are two
    // numbers that will eventually not).
    MaxIDRunes = 128

    uuidHexRunes = 32 // newUUID() = hex of 16 random bytes

    // mintedIDFixedRunes is the runes a minted id carries besides the namespace
    // and slug: ":custom:" + ":" + 32 hex.
    mintedIDFixedRunes = len(":custom:") + 1 + uuidHexRunes // 41

    // maxNamespaceRunes is the namespace budget. A namespace longer than this
    // cannot fit even with an empty slug, so it is capped unconditionally.
    maxNamespaceRunes = MaxIDRunes - mintedIDFixedRunes // 87
)
```

The transport's `maxConfigIDRunes` becomes an alias of the same number, so
there is exactly one literal for the id bound across both packages:

```go
// internal/transport/ws_config_handlers.go
maxConfigIDRunes = profile.MaxIDRunes
```

`maxConfigNameRunes = 200` stays as it is — it is already one constant, and the
name is a renderer-supplied ingress value owned by the transport, not a value
the domain mints. The bead's "one constant" is satisfied per cap: **one id cap
(128, domain-owned) and one name cap (200, transport-owned)**.

### 3.2 Truncate the slug at the sink; do not reject oversized names

The slug is derived data — a readable prefix of a name, never stored, never
shown on its own. The name is stored verbatim on the record (`Base.Name`,
`ProfileGroup.Name`) and truncated nowhere. The UUID suffix carries uniqueness
(§3.5). So truncating the slug loses only the least-informative tail of a
derived string, and loses no user data.

Rejecting names longer than the slug budget instead would shrink the documented
name cap from 200 to 84/82/79 runes and refuse names the product already accepts
and stores. That is a behavior change the repository's own rule rejects
("no backward-compatibility constraints — break and refactor freely" applies to
code, not to silently tightening a user-facing bound; and "no compatibility
shims or migrations" does not license breaking the name contract). The bead's
"capped at the point they are accepted" for the name is already satisfied by
`maxConfigNameRunes = 200`; the gap the bead names is the **mint**, and the mint
is fixed by capping the slug, not the name.

### 3.3 The algorithm and exact numbers

All three mint functions route through one helper that bounds the namespace and
slugifies the name within one shared budget, so the whole id is at most
`MaxIDRunes` runes **unconditionally** — regardless of what any caller (the
transport today, a future domain caller) passes as the namespace or the name:

```go
// internal/profile/profile.go
func mintID(namespace, name string) string {
    namespace = truncateRunes(namespace, maxIDNamespaceRunes)
    budget := maxIDNamespaceRunes - utf8.RuneCountInString(namespace)
    return namespace + ":custom:" + slugify(name, budget) + ":" + newUUID()
}

// truncateRunes caps a string on a rune boundary. Valid namespaces — "ssh",
// "group", "endpoint", and any transport-admitted type (bounded at
// maxEnumRunes = 64) — are unchanged; the cap fires only for a degenerate
// direct-domain caller.
func truncateRunes(s string, maxRunes int) string {
    if maxRunes <= 0 {
        return ""
    }
    count := 0
    for i := range s {
        if count == maxRunes {
            return s[:i]
        }
        count++
    }
    return s
}

// slugify preserves the old trim and Unicode-lowercase mapping while bounding
// output to maxRunes. strings.TrimSpace returns a subslice; the loop stops once
// the output budget is full, so no full-size intermediate string is allocated.
func slugify(s string, maxRunes int) string {
    if maxRunes <= 0 {
        return ""
    }
    s = strings.TrimSpace(s)
    var b strings.Builder
    b.Grow(min(len(s), maxRunes))
    for _, r := range s {
        if b.Len() == maxRunes {
            break
        }
        r = unicode.ToLower(r)
        switch {
        case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-' || r == '_':
            b.WriteRune(r)
        default:
            b.WriteByte('-')
        }
    }
    return b.String()
}

func NewProfileID(typ, name string) string { return mintID(typ, name) }
func NewGroupID(name string) string        { return mintID("group", name) }
func NewEndpointID(name string) string     { return mintID("endpoint", name) }
```

The fixed frame is `len(":custom:") + 1 + 32 = 41` runes, leaving
`MaxIDRunes - 41 = 87` runes to split between the namespace and the slug. With
the namespace capped at 87 runes, the slug budget is `87 - len(namespace)`, so
every minted id is `len(namespace) + 41 + len(slug) ≤ 87 + 41 = 128`:

| mint            | namespace  | slug budget | id length at slug budget |
| --------------- | ---------- | ----------- | ------------------------ |
| `NewProfileID`  | `ssh`      | **84**      | 128                      |
| `NewGroupID`    | `group`    | **82**      | 128                      |
| `NewEndpointID` | `endpoint` | **79**      | 128                      |

Every minted id is exactly 128 runes when the slug meets its budget, and
strictly shorter when it does not. `truncateRunes` guarantees the slug budget
is never negative, so there is no slice-index hazard; bounded `slugify` holds
at most `budget` runes in its builder (`strings.TrimSpace` returns a subslice
and allocates nothing), so a 10 MB name costs ~84 runes of output memory rather
than 10 MB.

`NewEndpointID` is included because it lives in the same package, mints the same
shape, and carries the same bug; the shared helper fixes it for the same cost
and keeps one owner rather than two. It is not a new surface, only the same
invariant applied to the third mint.

### 3.4 Rune/byte semantics and inclusive boundaries

Both caps are measured in **runes** — `boundedRunes` uses
`utf8.RuneCountInString` (`ws_config_handlers.go:142`) — and both are **inclusive
upper bounds** (`>` rejects, not `>=`):

- **Name (accept):** `0 … 200` runes accepted; `201+` rejected
  ("`name exceeds 200 characters`").
- **Renderer-supplied id (accept):** `0 … 128` runes accepted; `129+` rejected
  ("`id exceeds 128 characters`").
- **Minted id (mint):** always `≤ 128` runes, by construction — the namespace is
  capped at 87 runes and the slug at `87 - len(namespace)` runes.

`slugify` emits only ASCII (`[a-z0-9_-]`), so a slug's rune count equals its
byte count and `b.Grow`/`b.Len` enforce the rune budget exactly. It applies
`unicode.ToLower` before the allowlist, preserving the former
`strings.ToLower` mapping for characters such as `K → k`; remaining non-ASCII
runes become one `-`. The fixed frame is ASCII too, so a minted id whose
namespace is ASCII (every real one) has equal byte and rune lengths. A
non-ASCII namespace is truncated by `truncateRunes` on a rune boundary, so the
id remains valid UTF-8 and the declared rune bound still holds.

### 3.5 Collision behavior

Truncation cannot cause an id collision. Uniqueness comes from the 128-bit
random UUID suffix (`newUUID`, `profile.go:1117-1122`), not from the slug: two
distinct names that share a truncated slug prefix still differ in the UUID, and
two records minted from the same long name differ in the UUID. The slug is a
readability prefix only. `parseNamespacedID` (`profile.go:1004-1011`) splits on
`:` and reads the slug as `parts[2]`, so an empty or truncated slug changes
nothing about parsing; and `discovery.LocalTargetID = "local"` can never collide
with a minted id, whose prefix is always `ssh|group|endpoint:custom:`.

## 4. Behavioral tests

### 4.1 Domain — `internal/profile/profile_test.go`, `endpoint_test.go`

- **Minted id never exceeds its cap (table-driven).** Given names of `""`,
  `"my-host"`, a 200-rune ASCII name, a 200-rune multi-byte name, and a
  10_000-rune name, When each is minted through `NewProfileID`, `NewGroupID`,
  and `NewEndpointID`, Then every result has `utf8.RuneCountInString(id) ≤ 128`
  and parses via `parseNamespacedID`.
- **Profile slug at budget / +1.** Given a name of exactly 84 ASCII runes, When
  `NewProfileID("ssh", name)`, Then the id is exactly 128 runes with an 84-rune
  slug. Given a name of 85 runes, Then the id is exactly 128 runes with the slug
  truncated to 84 (a prefix of the 84-rune name).
- **Group slug at budget / +1.** Given 82 runes / 83 runes, When `NewGroupID`,
  Then id is exactly 128 runes with the slug 82 runes.
- **Endpoint slug at budget / +1.** Given 79 runes / 80 runes, When
  `NewEndpointID`, Then id is exactly 128 runes with the slug 79 runes.
- **Same prefix, distinct ids.** Given two calls to `NewProfileID("ssh", long)`
  with the same 200-rune name, Then the two ids differ (the UUID disambiguates).
- **Oversized namespace (unconditional sink).** Given
  `NewProfileID(strings.Repeat("x", 100), "name")` — a namespace longer than the
  87-rune budget — When minted, Then the id is exactly 128 runes: the namespace
  is capped to 87 runes and the slug budget is 0 (empty slug). The same
  guarantee holds for any namespace length, including a multi-byte one (capped
  on a rune boundary, still valid UTF-8).

### 4.2 Transport — `ws_profiles_test.go`, `ws_groups_test.go` (or a new `ws_config_handlers_test.go` for the validator units)

- **Name at limit / +1 (profiles.create).** Given `profiles.create` with a name
  of exactly 200 runes, When validated, Then accepted (no bound error). Given a
  name of 201 runes, Then refused with `name exceeds 200 characters`.
- **Name at limit / +1 (groups.create).** Given 200 / 201 runes, Then accepted /
  refused, same message.
- **Explicit id at limit / +1 (profiles.create with renderer-supplied id).**
  Given an id of exactly 128 runes, Then accepted. Given 129 runes, Then refused
  with `id exceeds 128 characters`.
- **Explicit id at limit / +1 (groups.create).** Given 128 / 129 runes, Then
  accepted / refused.
- **Minted id over the wire.** Given `profiles.create` with **no id** and a
  200-rune name, When the handler mints and stores it, Then the returned
  profile's `id` has ≤ 128 runes — the backend's own `configIDRunes` would
  accept it. Same for `groups.create`.

## 5. Affected symbols and files

- `internal/profile/profile.go` — add `MaxIDRunes` (exported),
  `uuidHexRunes`, `mintedIDFixedRunes`, `maxIDNamespaceRunes`, `mintID`, and
  `truncateRunes`; change `slugify` to accept an output budget while preserving
  its trim and Unicode-lowercase semantics. `NewProfileID` and `NewGroupID`
  delegate to `mintID`; `newUUID` is unchanged.
- `internal/profile/endpoint.go` — `NewEndpointID` delegates to `mintID`.
- `internal/transport/ws_config_handlers.go` — `maxConfigIDRunes` becomes
  `profile.MaxIDRunes`; `maxConfigNameRunes` and the validators are unchanged.
- Tests live in `internal/profile/profile_test.go` and
  `internal/transport/ws_profiles_test.go`.

No migration and no shim. The fix governs only ids minted from now on; an id
minted by the current bug (a name over the slug budget) is left exactly as it
is, never re-read or rewritten. That is the repository's own greenfield rule —
ADR-0017 §Consequences: "there are no users, no shipped release and no
connections worth keeping … documents in the old shape are not read, and nothing
converts them" — not a claim that every stored id already fits.

## 6. Ordered implementation

1. Add the failing domain tests: minted-id ≤ 128 (including the oversized
   namespace case), slug-at-budget / +1, same-prefix distinct ids, for
   `NewProfileID`, `NewGroupID`, `NewEndpointID`.
2. Add the shared bound and bounded mint helpers; route all three mints through
   `mintID` and preserve the old slug mapping. Domain tests go green.
3. Point `maxConfigIDRunes` at `profile.MaxIDRunes` and update its comment.
4. Add the transport tests: name 200/201 and explicit id 128/129 for
   `profiles.create`/`groups.create`, and the no-id 200-rune-name mint
   round-trip. Assert the returned id ≤ 128 runes.
5. Run focused `go test ./internal/profile/... ./internal/transport/...`; full CI
   is the main agent's job.
