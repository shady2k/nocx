# The wake and the backstop — nocx-dkawo.3

The record now holds five of the six things §6 of the 2026-08-24 orchestration mechanism
design names. This slice builds the sixth: **undispatched facts and their deadlines**, and
the two routes out of that set — the coordinator by a wake (D14), the human by a deadline
(D2).

## 1. What is being claimed

Invariant 3 of §5: _a fact that needs judgement reaches somebody within a named deadline._
Today nothing reaches anybody. A worker declares, the record reduces, and a coordinator that
went idle four minutes ago learns nothing until a person switches to its tab and types.

The three criteria on the bead, restated as the assertions §11 already wrote:

- §11.6 — an idle coordinator is woken when a worker declares, and takes a turn without the
  user touching anything. The wake does **not** clear the fact; the coordinator's own next
  call does.
- §11.3 — a wave with nothing undispatched wakes nobody, and **no timer is running**.
- §11.7 — a wake that cannot be delivered is recorded with its reason and never reported as
  sent.

## 2. Four decisions this plan makes, which the design left open

**W1. The fact set is in memory, and a restart closes it rather than carrying it.**
The record's other five things are rows in the encrypted store; this one is not. The
argument is the sweep's own: a backend restart terminalizes every non-terminal participant
as `interrupted`, because the worker died with the backend that held it and no pin exists to
prove otherwise. A fact that survived that restart would need judgement about a participant
the same restart has already judged, and the coordinator it would be dispatched to is gone
with its run. So the durable half would describe nothing, and escalating it to a human would
be telling them about a wave that no longer exists. This is open question §10.7 answered for
this slice only — what the human is told about the restart itself is still open, and is not
this bead's.

**W2. A fact enters on each of the two facts, and on nothing else.** `Declared` and `Exited`
are the two admissions the record has, and both pass through one function. The sweep's
`interrupted` is our own act rather than a participant's fact, and it applies to every open
participant at once — waking a coordinator for each would be a storm addressed to a
coordinator that is also gone.

**W3. Dispatch is the FETCH, and the fetch is `wave.holdings`.** D8 keeps four
acknowledgements distinct and advances the cursor on the second: fetched by the participant.
`HeldBy` is the only call that returns facts to a coordinator today, so it is the only thing
that dispatches. Acting on the fact is a third acknowledgement and is deliberately not
observed here.

**W4. The wake carries a POINTER, never the participant's content.** The text names the
participant and its state and tells the coordinator to ask what it holds. A declaration's
summary is free text from the participant — the record says so in as many words — and
typing it into another agent's input region is prompt injection with our own hands. It is
also what makes every wake distinct without being long.

## 3. Where each thing lives

| Thing                                         | Where                                     | Why there                                                                                                                                                                                                                                                                                     |
| --------------------------------------------- | ----------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Fact`, the set, the deadline, the two routes | `internal/wave/backstop.go`               | The record owns its sixth thing. It reads no screen and imports no grid, exactly as the rest of the package does not.                                                                                                                                                                         |
| `Waker`, `Escalation`, `Alarms`               | same file, as interfaces                  | Reaching a coordinator means typing into a pane; that is `internal/agenttyping`'s, and this package may not import it.                                                                                                                                                                        |
| `Store.CoordinatorSession`                    | `internal/content`                        | The `waves` row already holds it. A fact must name who judges it, and by the time an alarm fires the wave may hold nothing to look it up from.                                                                                                                                                |
| `waveWaker` over the one `Typist`             | `internal/app/wave.go`                    | The composition root is where the pane id and the session id are known to be the same string.                                                                                                                                                                                                 |
| `waveEscalation` over `notify`                | `internal/app/wave.go`                    | Trust and routing belong to `internal/notify` and are not restated here (§6.1).                                                                                                                                                                                                               |
| `wave.undispatched` kind                      | `internal/notify/catalogue.go`            | One catalogue owns what can be routed where. Attested: it is our own record's fact.                                                                                                                                                                                                           |
| `needsJudgement` on a holdings row            | `internal/assistant` + `contracts/tools/` | The wake types "call `wave.holdings`", so holdings has to tell the coordinator which worker it was about. It is also what makes the undispatched read a product read rather than an observability stub — `deadcode` cannot see a dead method behind a live interface, so the check is by eye. |

## 3.1 The two numbers this slice names at the composition root

`WithBound` and `WithEnrolmentDeadline` existed unwired since the last slice — the
composition root took neither, so both were dead by the ratchet's own reckoning. They are
the product's numbers and they are named beside the fact deadline now, which is where the
comment on `WithEnrolmentDeadline` already said they belonged.

## 4. What is deliberately out

- **The deadline's VALUE.** §10.8 says a number wrong in either direction breaks it and that
  `nocx-dkawo.4` measures it. Both ends of the interval are named here and the number is
  injected, exactly as the enrolment deadline already is.
- **The escalated FRACTION**, which §12 calls the number the whole design is judged by. It
  needs fan-out to mean anything.
- **The remote half of D14** — the helper typing on the far side. Local is the general route
  and the remote one is `remote-host`'s.
- **The mailbox**, its cursors and the other three of §7.2's five calls. `nocx-dkawo.4`.
