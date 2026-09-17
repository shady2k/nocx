# The wait and the close — nocx-dkawo.13

The epic's DONE WHEN names two calls that did not exist: _"creates three workers, gives each
a task, **waits on all three with ONE wait that returns when the first settles**, reads what
each produced, and **closes them**"_.

## 1. The wait

`wave.spawn` returns when a worker is **live** — its enrolment arrived — and nothing
returned when one **settled**.

**It answers what `wave.holdings` answers**, because it is the same question asked at a
different moment: what does my session hold. A wait with a shape of its own would be a
second account of that, and the two would disagree the first time either moved. "Which one
settled" is read from the states.

**Nothing rests on it** (§7.2). The backend watches the workers whether it is ever called
or not; a coordinator that never waits loses its own promptness and nothing else. That is
the whole difference from the blocking call and then the lease this design started with,
and it has to stay true — a wait anything depended on would be the lease back under a
friendlier name.

**It returns on three conditions**, and the third is the one that is easy to leave out:

| Condition                     | Why                                                                                                                                                                                                                                 |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| the session holds nothing     | there is nothing to wait for; blocking would hold a turn open for an event that cannot happen                                                                                                                                       |
| a participant has **settled** | the criterion's own case                                                                                                                                                                                                            |
| a fact is **owed judgement**  | the routing table has already decided the coordinator is needed and woken it; a wait that sat through that would be two mechanisms disagreeing about one moment — a worker that reported failure and is still running is exactly it |

**The wake-up channel is taken before the read.** A fact admitted between the read and the
select has already closed the channel the waiter is holding. The window is a few
instructions wide, so the test does not race two goroutines and hope: the store admits the
fact from inside `HeldBy`, after the rows are read, which makes the ordering the test's
rather than the scheduler's. Without that, the obvious mutation — taking the channel inside
the `select` — passes.

**An expired wait is an ANSWER.** The coordinator asked to be told promptly and was not;
what it holds is still true, and an error would only make it read the same thing again.

**Routine facts signal it.** The routing table decides whether to spend a coordinator's
TURN, and a routine completion is not worth one; a wait is a turn **already spent**, and
"the first of three settles" is exactly the routine case.

## 2. The close

**The first operation that reads a delegation.** `EffectClose` has been in `DefaultBundle`
since the record was built and nothing had ever consulted it, which made "membership is not
delegation" a comment. Mail is checked against membership; ending a worker is checked
against the delegation, and a human takeover suspends `send-input` and leaves close alone —
`DelegationState.Permits` has said so all along, and this is where it stops being
theoretical.

**It writes no state.** Ending the session produces a process exit, and that exit reaches
the record by the ordinary path. A close that also terminalized would be a second author of
a participant's state, and the two would disagree the first time a worker declared between
the kill and the write. A closed worker therefore reads `abandoned` — ended, and it never
said what it produced, which is exactly right.

**`mutate-destructive`, and it is not `session.wait`'s `stop`.** That one withdraws an
authority already in flight — a command the person authorized, which stopping can only
reduce. This ends a process the person may never have watched start, whose work is lost
with it and does not come back.

**Closing something already finished is not an error**: tidying up should not have to have
won a race.

## 3. What is still not here

**A MODEL.** The calls are asserted where they live and the sequence they drive is
asserted on real sessions in real panes, but nothing in the suite is an agent choosing to
make them. That is the last gap in the epic's "one automated check watches exactly that",
and it needs a provider endpoint.
