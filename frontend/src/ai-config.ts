/**
 * "The assistant's configuration was written" — one renderer-side signal, and
 * the surfaces that show what it means (bead nocx-1ebrs.66.1.28).
 *
 * The model chip in the composer names the pair that will answer, and it
 * learns that pair by asking (`agent.status`). The pair is written on the
 * SETTINGS pane, so nothing about the write is visible where the chip is: the
 * chip catches up when the terminal comes back to the front (`setVisible`).
 * That seam is honest about the SHOW and blind to the WRITE. A person who
 * picks a model and is back in the composer inside one round trip reads the
 * answer from before the write, and no later read is ever issued — so the
 * chip keeps naming the previous model, which is a claim about which model
 * answers that is not true.
 *
 * So a write that lands announces itself, here. Every write to this
 * configuration already goes through one place (`EndpointClient`), which is
 * where the announcement is raised; the terminal pane listens while it is in
 * front and asks again. It is not a poll and not a timer: it fires on the
 * reply to the write, which is the moment the fact changed.
 *
 * Module-level state on purpose, the way `ui/toast` is: panes are created and
 * destroyed and there are several of them, and what changed is a property of
 * the app, not of any one pane. A subscription returns its own unsubscribe,
 * and a pane that is disposed must call it.
 */
type AiConfigListener = () => void

const listeners = new Set<AiConfigListener>()

/** Announce that the assistant's configuration was written. */
export function notifyAiConfigChanged(): void {
  for (const listener of [...listeners]) listener()
}

/** Subscribe to those announcements. Returns the unsubscribe. */
export function onAiConfigChanged(listener: AiConfigListener): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}
