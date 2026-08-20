# ADR-0022: the ssh command line is the carrier, not a second channel

- **Status:** Superseded by
  [ADR-0035](0035-the-channel-we-own-is-the-carrier.md) (accepted 2026-08-20).
  Accepted 2026-08-05; kept for the record, and its measurements are still the
  ones anybody reasoning about this transport should read. What reversed the
  decision was not an error in them but two premises that have since gone —
  ADR-0035 names both. The three wiring rules in the Rationale below are carried
  forward there verbatim in substance, because they now govern a transport we
  build rather than one we declined.
- **Owner decision**, taken on measurements from `nocx-qtnp`
- **Related:** `nocx-mlm7`, [`ADR-0004`](0004-input-ownership-and-editor-abstraction.md),
  the delivery-modes design
  ([`.internal/specs/2026-08-05-nocxify-delivery-modes-design.md`](../../.internal/specs/2026-08-05-nocxify-delivery-modes-design.md)),
  the full spike report
  ([`.internal/reports/nocx-mlm7-spike-multiplex.md`](../../.internal/reports/nocx-mlm7-spike-multiplex.md))

## Context

When a user types `ssh user@host` by hand, that connection belongs to their shell. nocx has
no channel to it, so to make the far shell integrated it rewrites the submitted line and the
integration payload rides in the command OpenSSH runs on the far side. The visible result is
a line of roughly 190 bytes on screen, which is what the owner complained about and what
started this work.

An obvious-looking alternative exists: we already rewrite the line, so we could add
`-o ControlMaster=auto -o ControlPath=…`, make the user's own connection a master, and push
the payload over a second channel by SFTP — no second authentication. The argv line would
shrink to a bootstrap that waits for the file. Intuition says this is cleaner: files instead
of a command line, SFTP instead of shell quoting.

Intuition was wrong about the part that mattered, and the numbers are why this ADR exists.

## Decision

**The ssh command line stays the carrier for the script tiers.** nocx does not open a second
channel over ControlMaster to deliver integration.

The technique is not rejected as unworkable — it is measured, it works, and it is written
down here because the relay (`nocx-if6` phase B) will need exactly this: a binary cannot be
passed through a command line at all.

## Rationale

Measured against a real OpenSSH 10.4 client and a real sshd, 20 runs:

|                                  | result                                                                                                |
| -------------------------------- | ----------------------------------------------------------------------------------------------------- |
| 35,243-byte push over the master | 20/20, **zero** extra server-side authentications (server log: 20 connections, 20 auths, 40 sessions) |
| submit → usable master           | median 115.6 ms                                                                                       |
| submit → push complete           | median 126.7 ms                                                                                       |
| bootstrap wait for the file      | median 9.7 ms, max 11.7 ms — a 3 s timeout is ~250× the worst case                                    |
| rewritten line                   | **158 bytes**, against 189–207 today                                                                  |

The last row is the one that decided it. The premise of the alternative was "a short line from
the first connection", and there is no such prize: the payload never travelled in the line —
it is already behind `$(cat …)` — so the length comes from the `if/then/else` guard and the
paths, not from the 35 KB. All three shapes are the same order: 207 bytes today, 158 with
multiplexing, 130 for the installed-host form we already build. **None of them removes the
line from the screen**, which is the thing that was actually wanted.

Against a marginal gain, multiplexing adds preconditions the argv path does not have:

- `ControlMaster` must be permitted by the user's config and the server;
- the socket path must expand to under ~107 bytes (a `%C` path under a directory we create);
- the socket directory must be writable;
- and a timing window appears between "the master is usable" and "the file has landed" —
  measured tiny, but a window rather than none.

The argv path has **no** preconditions and **no** race: one channel, the same one the user
authenticated, with fail-open expressed as a shell `if` over a local file.

The spike also found three failure modes that would have been silent, recorded here because
they will bite whoever implements the relay: an SFTP push must pass `-o ControlMaster=auto`
explicitly or it quietly authenticates again; the control path must be `%C` or
per-destination, because the multiplexer does not reject a mismatched destination; and the
bootstrap's timeout fallback must be `if/else`, never `exec A || exec B`, which dead-exits
with 127.

## Consequences

- The script tiers keep delivering through the rewritten command line, with the local staged
  file and its consume-once rule.
- The line stays visible, and that remains a product decision rather than an accident
  (ADR-0004's rejection of renderer-side echo suppression is untouched). Hiding it at render
  is not ruled out for ever — the owner has deferred it until the whole path is tested end to
  end, tracked as `nocx-4vyb`.
- The persistent install keeps its value, but a smaller one than it was justified with: 130
  bytes against 190, and 35 KB saved per connection. It is not a fix for how the line looks.
- When the relay lands, this decision is where its transport starts: the numbers, the three
  traps and the harness are already in the spike report.
