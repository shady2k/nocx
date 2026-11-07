# ADR-akhhb098 — An SSH connection has one owner, and the shell comes first

- **Status:** proposed (2026-11-06). Becomes accepted when the last stage's prototype holds; until then ADR-0049 stands whole.
- **Date:** 2026-11-06
- **Taken from** `nocx-oalo1`, the owner's decisions of 2026-11-06; full design in [`.internal/specs/2026-11-06-ssh-foundation-design.md`](../../.internal/specs/2026-11-06-ssh-foundation-design.md)
- **Supersedes on acceptance:** [ADR-0049](0049-the-channel-we-own-is-the-carrier.md), only for a typed `ssh` our transport can honour. It stands for wrapping a plain `ssh`.
- **Amends on acceptance:** AD-7: a tab has one active PTY or channel and may hold parked ones beneath it. The amendment lands in `docs/architecture.md` with the stage that needs it.
- **Reads, does not change:** AD-1, AD-4, AD-6, AD-8, ADR-0004, ADR-0015, ADR-0024, ADR-0025, ADR-0034, ADR-0068, ADR-0069, ADR-0105, ADR-8eej3e66.

## Context and problem

A person opening an SSH tab gets an empty screen, because no state called "opening" exists and the shell is the last thing created: probe, then a 15 MB helper install, then the shell. A struggling host therefore gets the heaviest work before the person has anything, and an install failure fails the open. After a closed lid a dead connection is noticed about two minutes later on the local route, and on a far-helper connection `[hypothesis]` only by TCP failure. Automatic reconnect exists as a setting and nothing calls it. A saved connection and a typed `ssh` are different mechanisms with different keys.

Facts about one connection live in several places, and a connection stops existing exactly when someone needs to say what is happening to it.

## Decision drivers

Owner's order: the terminal works; security; honest state; the session survives; convenience. Measured by the scenarios in the design's §3: a stage under 1 s; a host silent five minutes still connects; silence read within 30 s; a right state within 5 s of a wake; one sign-in for six tabs; no signature over a changed key; raw input in every failure.

## Considered options

- **A. A per-host supervisor in the local helper, panes as projections** (Supervisor, State, Happy Eyeballs). Chosen.
- **B. A per-pane state machine in the coordinator, pool unchanged.** Rejected: six tabs make six attempts and six vault prompts, and each pane guesses at a shared fact.
- **C. The system's OpenSSH as the only transport,** as kitty's ssh kitten does. Rejects the assumption that nocx holds the connection. Rejected: state from `ssh -v` prose, no signature-level stop on a changed key, and it undoes AD-4 and nocx-50w7p. It survives as the plain-ssh path.

## Decision

1. **One supervisor per `poolKey` (host, port, user, identity, jump route), in the local helper,** owns the connection's life and state, including while no connection exists. A pane is a projection of it.
2. **`pane.connection` replaces `session.liveness`,** keyed by pane because no session id exists while opening. `exit` still owns a session's end and `lifecycle.changed` still owns integration.
3. **The shell comes first.** After sign-in an integrated attempt races a plain shell opened after a short pause (owner's decision, RFC 8305's shape). Blocks show only after an accepted domain reaches `prompt_ready`. A helper install is never part of an open. A working shell is never replaced by nocx.
4. **Waiting is a state, not a failure.** `open` answers early, and each attempt and each ask is a catalogued operation with its own finite bound. Waiting for a person stops the no-progress clock and can last; an expired ask raises no new one until a person acts.
5. **Silence closes nothing by itself.** A transport error closes and reconnects unaided. Silence on a connection whose shells are all helper-owned closes on the evidence of a look-only dial, because reattaching loses nothing. Silence on a connection carrying a plain shell never closes it: the look-only dial's answer is shown, and the person chooses to wait or to reconnect (owner, 2026-11-06). A dropped connection reconnects unaided, and only confirmed absence of a far-helper shell opens another. A changed key or a rejected sign-in stops it. No command is ever re-run.
6. **One resolver answers `ours` or `plain`,** with the reason, and one rule chooses the credential: an explicit identity wins, one matching saved connection applies, several apply none. A typed line our transport honours binds a hosted session over the parked local one, in the same tab.
7. **A failure on our transport never falls through to plain ssh automatically,** because a rejected password and a changed key must not become other attempts. The head offers it on `failed`.

## Consequences

- A typed `ssh` no longer runs in the shell it was typed into when it is `ours`: local history and `$?` need deliberate handling, and this is the riskiest part, so it lands last and behind a prototype.
- `open` no longer means a shell exists, and every SSH caller moves to adoption.
- Probes double by default (15 s instead of 30 s).
- A plain shell on a silent flow waits for a person, so an unattended tab can sit `stalled` until someone returns. Waiting costs a click; a wrong close costs the work. The kernel's retransmission limit is the floor: a flow with unacknowledged bytes ends in the end, whatever nocx decides.
- First session on a new host has no helper, so it cannot return to the same shell after a drop.
- A hung shell behind a live sshd stays undiagnosed under AD-6; Stop and Reconnect are the answer.

## Assumptions

Script integration is cheap enough to race on a loaded host; 3 s suits healthy hosts; stage boundaries are observable without forking `x/crypto`; a pane can hold a session over another. The design's §8 says what would invalidate each.
