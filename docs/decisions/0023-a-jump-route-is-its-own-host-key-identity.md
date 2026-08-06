# ADR-0023: a jump route is its own host-key identity

- **Status:** accepted (2026-08-06)
- **Taken from** `nocx-shat`, while splitting PR #64 into per-concern branches
- **Related:** `nocx-8b1v` (the bastion's own auth material), `nocx-9224` (the wildcard
  entry a trust write leaves behind), [`ADR-0015`](0015-ssh-g-as-the-ssh-config-oracle.md)

## Context

A hostname does not name a machine. `db.internal` reached directly from the office and
`db.internal` reached through a bastion can legitimately be two different servers — split
horizon DNS, a NAT that maps the name onto a different host inside, an overlapping RFC 1918
range on the far side of the jump. This is ordinary infrastructure, not a misconfiguration.

nocx checked both routes against one `known_hosts` identity: the target's hostname and port.
So the two routes fought over one line. Accepting the key seen through the bastion made the
direct route report a mismatch — the loudest error the product has, the one that means
somebody is intercepting you — and accepting it back invalidated the jump route again. There
was no state in which both worked.

The same identity is what Test and Open each derived independently, and they disagreed for a
second reason: the probe dialled directly even when a jump host was configured, so it
verified a key the open path would never see.

## Decision

**A direct route keeps its OpenSSH-compatible `known_hosts` identity. A jump route gets its
own, derived from the target endpoint plus every hop endpoint in the chain.**

The jump identity is `nocx-v1-<base32(sha256(...))>:22`, hashing a version tag, the
normalised target endpoint, and each normalised hop endpoint in order
(`knownHostsTargetAddr`, `internal/ssh/ssh_real.go`). Two distinct chains to one target
therefore never collide, and neither can displace the direct entry.

It is derived from **routing alone**. No credential, no secret id, no profile id enters the
hash.

The probe dials through the jump path whenever one is configured, so Test and Open ask about
the same key.

## Rationale

**Why not key trust by profile id.** It is the obvious alternative and it is wrong for the
same reason the bug exists: two profiles pointing at one machine over one route would each
accept keys the other cannot see, and rotating a credential — which changes nothing about
which machine answers — would silently discard the trust. Host keys answer "is this the
machine I reached last time"; that question is about the route, not about who is asking.

**Why an opaque identity rather than a readable composite** such as
`db.internal%via%bastion.example.com`. `known_hosts` hostnames have no escaping and are
matched with `*`/`?` wildcards, so any readable composite has to invent a separator that
cannot appear in a hostname and cannot be swallowed by somebody's existing wildcard line. A
digest sidesteps the whole class. The version tag in the hash input is what lets the scheme
change later without silently reinterpreting entries written under the old one.

**What this costs, stated plainly.** A jump-route entry in `known_hosts` is not readable by
a human and not shareable with `ssh`. Somebody inspecting the file sees `nocx-v1-…` lines
they cannot map back to a host. That is the price of not corrupting the direct entries,
which OpenSSH and nocx do share — and the direct entries are the ones people actually read,
copy between machines, and manage centrally.

**It is not reversible for free.** Entries are already being written under this identity.
Changing the derivation orphans them: every jump route re-prompts on its next open, which
looks to a user exactly like the warning that means something is wrong. A change here needs
a migration or a deliberate decision to re-prompt, which is why this is an ADR and not a
comment.

## Consequences

- The wire carries both identities: `host` for the human and `knownHostsHost` for the
  backend's lookup and write (`contracts/connections.probe.schema.json`). The renderer never
  derives either — a second derivation on the renderer side is the defect this shape exists
  to prevent.
- Every hop is still verified on its own hostname, as a bastion is not itself jumped.
- `nocx-9224` was independent and is now fixed: `TrustHostKey` compared `known_hosts` names
  literally while verification applied wildcards, so a `*.example.com` line survived a trust
  write and kept a rejected key valid. The fix removed the second comparison rather than
  teaching it wildcards — `knownhosts` is asked which lines cover a host, since it is the
  package that will later verify against them.

  The claim first written here, that opaque route identities "cannot match a wildcard", was
  too absolute and is corrected: a deliberately broad pattern (`*`, or `nocx-v1-*`) does
  match one, because the identity carries port 22 like any other. What the digest actually
  prevents is _accidental_ capture by the domain-shaped patterns people really write —
  `*.example.com` cannot reach a `nocx-v1-…` host — which is why that bug reached the direct
  route only. The conclusion held; the reasoning was stronger than the facts.
