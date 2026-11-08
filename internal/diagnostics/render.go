package diagnostics

// The text Copy diagnostics pastes (nocx-3an1s.1.8).
//
// Plain text on purpose: it goes into an issue tracker, a chat and a mail, and
// only one of those renders markdown. One fact per line, nothing nested deeper
// than a stage list, and every duration stated so a reader does not have to
// subtract timestamps.
//
// NOTHING HERE MAY NAME A HOST, A USER, AN ADDRESS, A PATH, A FINGERPRINT OR
// ANYTHING TYPED. A destination is the per-run label the recorder minted
// ("connection 1"); a log is its size, never its path; a failure is its
// bounded reason, never the error text that may carry a path.

import (
	"fmt"
	"strings"
	"time"
)

// Render is the text a person copies. It carries the version block the About
// page already shows, then the record.
func Render(s Snapshot, f Facts) string {
	var b strings.Builder
	b.WriteString("nocx diagnostics\n")
	if f.ServerStarted.IsZero() {
		b.WriteString("server: up unknown\n")
	} else {
		b.WriteString(fmt.Sprintf("server: up %s\n", shortDur(s.Generated.Sub(f.ServerStarted))))
	}
	writeHelper(&b, f.Helper)
	if f.LogKnown {
		b.WriteString(fmt.Sprintf("log: %d bytes\n", f.LogBytes))
	} else {
		b.WriteString("log: size unavailable\n")
	}
	fmt.Fprintf(&b, "opens: %d pending, %d recent\n", len(s.Live), len(s.Recent))
	for _, o := range s.Live {
		writeOpen(&b, "pending", o)
	}
	for _, o := range s.Recent {
		writeOpen(&b, "recent", o)
	}
	writeRefusals(&b, s.Refusals)
	writeCreditOverruns(&b, s.CreditOverruns)
	writeLosses(&b, s.Losses)
	writeAdmissions(&b, f.Admissions)
	count, top := GoroutineSummary()
	fmt.Fprintf(&b, "goroutines: %d\n", count)
	if len(top) > 0 {
		fmt.Fprintf(&b, "goroutines-top: %s\n", strings.Join(top, ", "))
	}
	return b.String()
}

func writeHelper(b *strings.Builder, h HelperStatus) {
	switch {
	case !h.Known:
		b.WriteString("helper: status unavailable\n")
	case h.Connected:
		fmt.Fprintf(b, "helper: connected %s\n", shortDur(h.ConnectedFor))
	case h.Failure != "":
		fmt.Fprintf(b, "helper: not connected (%s)\n", h.Failure)
	default:
		b.WriteString("helper: not connected\n")
	}
}

func writeOpen(b *strings.Builder, state string, o OpenView) {
	fmt.Fprintf(b, "  %s [%s] %s trace=%s request=%s", state, o.Label, o.Kind, orNone(o.Trace), orNone(o.Request))
	if state == "pending" {
		stage := o.Current
		if stage == "" {
			stage = "between-stages"
		}
		fmt.Fprintf(b, " stage=%s for=%s elapsed=%s", stage, shortDur(o.CurrentSince), shortDur(o.Elapsed))
	} else {
		fmt.Fprintf(b, " outcome=%s duration=%s", orNone(o.Outcome), shortDur(o.Elapsed))
	}
	if o.FirstPrompt > 0 {
		fmt.Fprintf(b, " first_prompt=%s", shortDur(o.FirstPrompt))
	}
	if o.Publish != "" {
		fmt.Fprintf(b, " publish=%s", o.Publish)
	}
	if o.Integration != "" {
		fmt.Fprintf(b, " integration=%s", o.Integration)
	}
	if o.ClosedBy != "" {
		fmt.Fprintf(b, " lifecycle_closed_by=%s", o.ClosedBy)
	}
	if o.Detail != "" {
		fmt.Fprintf(b, " detail=%s", o.Detail)
	}
	b.WriteString("\n")
	if len(o.Stages) > 0 {
		parts := make([]string, 0, len(o.Stages))
		for _, st := range o.Stages {
			if !st.Done {
				// AN UNFINISHED STAGE, said as such: a 0 ms "ok" would read
				// as a stage that finished. Only the innermost one is where
				// the open IS; an outer one is waiting on its child.
				if st.Name == o.Current {
					parts = append(parts, fmt.Sprintf("%s %s running", st.Name, shortDur(o.CurrentSince)))
				} else {
					parts = append(parts, fmt.Sprintf("%s %s waiting", st.Name, shortDur(o.CurrentSince)))
				}
				continue
			}
			outcome := "ok"
			if st.Err {
				outcome = "failed"
			}
			parts = append(parts, fmt.Sprintf("%s %s %s", st.Name, shortDur(st.Took), outcome))
		}
		fmt.Fprintf(b, "    stages: %s\n", strings.Join(parts, "; "))
	}
}

func writeRefusals(b *strings.Builder, refusals []Refusal) {
	if len(refusals) == 0 {
		b.WriteString("refusals: none\n")
		return
	}
	fmt.Fprintf(b, "refusals: %d\n", len(refusals))
	for _, r := range refusals {
		holders := make([]string, 0, len(r.Holders))
		for _, h := range r.Holders {
			holders = append(holders, fmt.Sprintf("%s(%s) %s", orNone(h.Method), orNone(h.Request), shortDur(h.Held)))
		}
		holderText := "none"
		if len(holders) > 0 {
			holderText = strings.Join(holders, ", ")
		}
		fmt.Fprintf(b, "  %s method=%s scope=%s held=%s holders=%s\n",
			r.At.UTC().Format("15:04:05.000"), orNone(r.Method), orNone(r.Scope),
			shortDur(r.Held), holderText)
	}
}

// writeCreditOverruns is the record of a peer that broke the one rule the
// credit window states: never send more than you were told you may have
// outstanding. It is deliberately separate from refusals: a refusal is the
// server saying no, an overrun is the server ending the connection because
// the client did not need to be told no.
func writeCreditOverruns(b *strings.Builder, overruns []CreditOverrun) {
	if len(overruns) == 0 {
		b.WriteString("credit overruns: none\n")
		return
	}
	fmt.Fprintf(b, "credit overruns: %d\n", len(overruns))
	for _, o := range overruns {
		fmt.Fprintf(b, "  %s conn=%d method=%s class=%s in_flight=%d window=%d\n",
			o.At.UTC().Format("15:04:05.000"), o.Conn, orNone(o.Method), orNone(o.Class),
			o.InFlight, o.Window)
	}
}

func writeLosses(b *strings.Builder, losses []LifecycleLoss) {
	if len(losses) == 0 {
		b.WriteString("lifecycle losses: none\n")
		return
	}
	fmt.Fprintf(b, "lifecycle losses: %d\n", len(losses))
	for _, l := range losses {
		fmt.Fprintf(b, "  %s lane=%s cause=%s closed_by=%s\n",
			l.At.UTC().Format("15:04:05.000"), orNone(l.Lane), orNone(l.Cause), orNone(l.ClosedBy))
	}
}

func writeAdmissions(b *strings.Builder, admissions []AdmissionView) {
	if len(admissions) == 0 {
		b.WriteString("admissions: none recorded\n")
		return
	}
	for _, a := range admissions {
		holders := make([]string, 0, len(a.Holders))
		for _, h := range a.Holders {
			holders = append(holders, fmt.Sprintf("%s(%s) %s", orNone(h.Method), orNone(h.Request), shortDur(h.Held)))
		}
		if len(holders) == 0 {
			fmt.Fprintf(b, "  %s held=0\n", orNone(a.Scope))
			continue
		}
		fmt.Fprintf(b, "  %s held=%d %s\n", orNone(a.Scope), len(a.Holders), strings.Join(holders, ", "))
	}
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// shortDur is the one duration spelling: milliseconds under ten seconds so a
// short stage is never "0s", seconds with one decimal above it.
func shortDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
