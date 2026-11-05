package transport

// AN EPISODE, NOT A PASS (nocx-3an1s.1.7).
//
// The recorder learns that the store evicted bytes from its recording one
// APPEND at a time, and a session held at its output cap evicts on every
// append: measured on an installed build, 10 to 200 ms apart, for as long as
// the session produced output. Reporting each pass is what turned one busy
// terminal into a 1.3 GB log whose tail held nothing else.
//
// The event a person cares about is the episode — "this session's recording
// is being evicted, here is how much" — so this type is the whole rule, in one
// place, and both things the recorder does per pass ask it: the line it
// writes and the recovery revision it publishes (the second is a store query
// and a JSON-RPC frame, and was paying that cost per eviction too).
//
// The ends, stated as an interval: an episode opens with its first evicted
// byte and writes a line at once; while it lasts it writes at most one line
// per interval, each naming the bytes dropped since the previous line and the
// episode's total; when it stops — the store stops evicting, or the session
// ends — the closing line names whatever had no line of its own. No pass is
// forgotten and no byte is counted twice, which is what makes the lines a
// substitute for the flood and not a sample of it.

import "time"

// capReportInterval is how often a session that keeps evicting its recording
// says so. A minute is deliberately not "often": the subject is a session that
// is already failing to hold its whole recording, the total on the line is
// what a person acts on, and every extra line is one a real failure has to
// compete with in a tail.
const capReportInterval = time.Minute

// capEpisode coalesces the per-pass reports of one session's output-cap
// evictions. It is not safe for concurrent use; the recorder's loop is its one
// caller, and the loop is sequential by construction.
type capEpisode struct {
	interval time.Duration
	now      func() time.Time

	open      bool
	last      time.Time
	sinceLast uint64
	total     uint64
}

// newCapEpisode builds an episode that reports at most once per interval. now
// is the clock, so a test drives it instead of waiting.
func newCapEpisode(interval time.Duration, now func() time.Time) *capEpisode {
	return &capEpisode{interval: interval, now: now}
}

// evicted records one pass's n dropped bytes and reports whether this pass is
// the one that writes a line, with the numbers that line names: the bytes
// since the previous line, and the episode's total.
func (e *capEpisode) evicted(n uint64) (since, total uint64, reported bool) {
	e.total += n
	e.sinceLast += n
	if !e.open {
		// The first eviction is the episode's start, and a person reading the
		// log wants to know it started rather than a minute later.
		e.open = true
		e.last = e.now()
		return e.report()
	}
	if e.now().Sub(e.last) < e.interval {
		return 0, 0, false
	}
	e.last = e.now()
	return e.report()
}

// stopped ends the episode and reports the closing line, if anything is left
// unsaid. An episode whose last line already carried the total answers false:
// the record is complete, and a closing line that repeated it would be the
// second line the whole type exists to avoid.
func (e *capEpisode) stopped() (since, total uint64, reported bool) {
	if !e.open {
		return 0, 0, false
	}
	since, total = e.sinceLast, e.total
	e.open, e.sinceLast, e.total = false, 0, 0
	if since == 0 {
		return 0, 0, false
	}
	return since, total, true
}

func (e *capEpisode) report() (since, total uint64, reported bool) {
	since, total = e.sinceLast, e.total
	e.sinceLast = 0
	return since, total, true
}
