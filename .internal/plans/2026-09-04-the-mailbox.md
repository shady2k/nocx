# The mailbox — nocx-dkawo.11

The sixth thing §6 of the 2026-08-24 orchestration mechanism design says the record holds,
and the one `nocx-dkawo.2`…`.4` left unbuilt.

## 1. Why a mailbox and not a queue

It exists because of a measured failure: two readers, one mailbox, seventeen minutes in
which mail consumed by the first was invisible to the second and to everybody looking for
it. The cure is not a better queue, it is a different verb. **Reading is not taking.** A
message is a row that stays where it is; what moves is a cursor, and every reader has its
own.

So a mailbox here cannot be drained. Two readers are handed the same messages in the same
order, and neither one's read moves the other's cursor.

## 2. Four acknowledgements, and the three a backend can witness

D8 keeps four facts apart and never merges them: **committed**, **fetched**, **present in
the model's context**, **acted upon**.

The backend can witness three. It knows what it committed, what it handed out, and what a
reader **claimed** it acted on. It cannot know what reached a model's context — nothing
outside the model can — so this slice has no word for it and does not invent one. A column
claiming it would be the self-matching sentinel the whole design is written against.

Two marks carry the rest, and both are load-bearing. `fetched_to` stops a message being
handed out forever; `acted_to` stops a retry of one response committing the same spawn
twice, because "read consumes nothing" prevents loss and does not prevent duplication.
`wave.holdings` returns the cursor with the mail and takes it back on a later call, which is
§7.2's sentence implemented as written.

## 3. Decisions this plan makes

**M1. Durable, unlike the undispatched fact set.** ADR-0056 put that set in memory because a
fact is a claim about a live process and a restart has already judged every process it could
be about. A MESSAGE is not that: it is a thing that was said, and it keeps its value after
the participants are gone — "committed and never taken" is exactly what you want to be able
to read afterwards. Schema 17→18, two tables and one index.

**M2. Delivery is the RECIPIENT's own cursor.** Another reader looking in — which is what
mesh brings — moves its own cursor and delivers nothing, because a message is delivered when
the participant it was addressed to has taken it, not when somebody has seen it.

**M3. Mail is checked against MEMBERSHIP and nothing else.** Membership makes a participant
addressable; delegation makes it controllable. A human takeover suspends `send-input` and
must not stop a coordinator writing to its own worker, so `wave.say` carries `observe` and
not `send-input`: leaving a message reaches nobody's keyboard and cannot answer a modal.

**M4. A reader is named by what outlives its runs.** A worker is its participant id; a
coordinator is its **session** (AD-7), which is what makes a restarted coordinator the same
reader — the property D3 already rests on. This answers §10.6's first two questions for this
slice; abandoned cursors, compaction and mesh's N² are still open.

**M5. A fetch whose cursor could not be written hands nothing over.** A reader that believed
it had a position it does not have would acknowledge past mail it never saw. Losing a fetch
costs one repeat; losing a message costs the wave.

## 4. What this slice does NOT reach, and why

**A worker cannot read its mail, for the same reason it cannot declare anything** — see
`nocx-dkawo.12`, filed today. `agent_report` has a complete receiving half and **no sender
anywhere in the product**: `nocx.bash` sends `agent_enrol` and `agent_withdraw` and nothing
else, so every worker terminalizes as `abandoned` and the epic's "reads what they did" does
not work end to end for a person. The declaration is a tool the agent calls (§7.1, D5), and
the launcher that would stage that tool surface is unbuilt and unbeaded. The worker's inbox
check needs exactly the same thing.

So the coordinator's half lands here — it writes with `wave.say` and reads on
`wave.holdings` — and the worker's half is `nocx-dkawo.12`'s to unblock.

Also out: retention and compaction of a mailbox (§10.9's bounds), the wait-for graph (D12),
and mesh's sender authentication and causal ordering (§10.10).
