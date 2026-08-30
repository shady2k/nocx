package ssh

import (
	"errors"
	"time"
)

// LivenessObserver is told what the keepalive prober learns about the far end
// of ONE connection: false when a probe failed and the connection is still
// being given a chance, true when it answers again (nocx-iarf9).
//
// It is deliberately not told about the give-up. That failure closes the
// transport, which ends every session on it, and the end of a session is
// already reported — with a cause — by the exit notification. Reporting it
// here as well would be a second owner of the same fact.
//
// The observer is bound by whoever built the ConnectConfig, so it carries its
// own idea of which host this is; this package does not name it. That matters
// because the connection is POOLED (AD-4): several tabs to the same principal
// share one transport, and the observer belongs to whichever Connect dialed
// it. What it reports is therefore a fact about a machine, not about one tab —
// which is exactly what a keepalive knows.
type LivenessObserver func(Reachability)

// Reachability is one probe's finding about the far end: whether it answered,
// and how long it took to do so.
//
// The round trip is here because this prober is the ONLY thing in nocx that
// measures one. Without it the product can say a host is gone and cannot say
// a host is struggling — and "struggling" is the state a person actually
// meets, on a loaded server or a long link. It is a measurement and not a
// second liveness value: whether the host is REACHABLE and how FAST it
// answers are two questions, and folding the second into the first would make
// a slow host look like a half-dead one (AD-8: one owner per fact).
//
// RoundTrip is zero for a probe that never answered — an unanswered probe has
// no duration, and reporting the budget it spent would be reporting the
// timeout rather than the host.
type Reachability struct {
	Responsive bool
	RoundTrip  time.Duration
}

// keepaliveTarget is the part of *gossh.Client the prober uses. An interface
// so the fold above it can be driven without a server: the failure path is the
// one that matters here and it must not need a host that stops answering on
// cue. *gossh.Client satisfies it as written.
type keepaliveTarget interface {
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
	Close() error
}

// keepaliveVerdict is what one probe result means.
type keepaliveVerdict int

const (
	// keepaliveSteady: the host answered and had been answering. Nothing to
	// report — a probe that confirms what is already believed must not wake
	// the observer once per tick for the life of every connection.
	keepaliveSteady keepaliveVerdict = iota
	// keepaliveResponsive: the host answered after failing. This is the
	// return out of `unknown`, and the only success worth reporting.
	keepaliveResponsive
	// keepaliveUnresponsive: the probe failed and retries remain. The host is
	// not answering and we have NOT concluded anything — the evidence behind
	// a session reading `unknown` rather than alive or dead.
	keepaliveUnresponsive
	// keepaliveGiveUp: the retries are spent. The connection is closed, which
	// ends its sessions, and the exit notification says so with a cause.
	keepaliveGiveUp
)

// keepaliveTally folds probe results into verdicts. Split out of the goroutine
// so the sequence — fail, fail, give up; fail, succeed, reset — is tested as
// arithmetic rather than as a race against a ticker (AGENTS.md: a test may not
// depend on timing).
type keepaliveTally struct {
	countMax int
	failures int
}

func (t *keepaliveTally) probe(ok bool) keepaliveVerdict {
	if ok {
		if t.failures == 0 {
			return keepaliveSteady
		}
		t.failures = 0
		return keepaliveResponsive
	}
	t.failures++
	// countMax <= 0 keeps its inherited meaning: a single failure closes the
	// connection. There is no window in which the host is merely not
	// answering, so nothing reports `unknown` for such a connection.
	if t.countMax <= 0 || t.failures >= t.countMax {
		return keepaliveGiveUp
	}
	return keepaliveUnresponsive
}

// errProbeSilent is returned when a probe did not come back inside its budget.
// It is not one more failure: see the comment at its only use.
var errProbeSilent = errors.New("ssh: keepalive probe did not return")

// sendProbe sends one keepalive and waits at most budget for it.
//
// WHY A GOROUTINE AT ALL. x/crypto's SendRequest takes no context and no
// deadline: it writes the global request and then blocks on a bare channel
// receive (mux.go), holding globalSentMu for the whole wait. Against a socket
// whose peer has silently gone — a suspended laptop, a NAT that dropped the
// flow — that wait is bounded only by the kernel's own retransmit timer, and
// nothing above it can be interrupted. The keepalive that exists to notice
// exactly this condition was therefore the one thing that could not notice it.
//
// WHAT COUNTS AS ALIVE. Any answer, whatever it says. A server replies
// SSH_MSG_REQUEST_FAILURE to keepalive@openssh.com because the request type is
// not one it implements — that is what x/crypto's own DiscardRequests does and
// what OpenSSH's server does — so `ok` is false on a perfectly healthy link
// and OpenSSH's client counts any reply as proof of life for the same reason.
// The previous predicate (err == nil && ok) therefore called every healthy
// server unresponsive; nobody had noticed because production never started a
// prober at all (internal/session's option translation dropped the interval).
//
// The returned channel closes when the probe's goroutine has actually
// finished, so a caller that gives up on a probe can still wait for it rather
// than leak it.
func sendProbe(target keepaliveTarget, budget time.Duration) (<-chan struct{}, error) {
	type result struct{ err error }
	res := make(chan result, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _, err := target.SendRequest("keepalive@openssh.com", true, nil)
		res <- result{err: err}
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case r := <-res:
		return finished, r.err
	case <-timer.C:
		return finished, errProbeSilent
	}
}

// startKeepalive launches a goroutine that sends keepalive@openssh.com probes
// on the SSH connection at the given interval. It returns a stop function that
// signals the goroutine to exit, and a done channel that is closed when the
// goroutine has terminated (useful in tests to verify clean shutdown). Passing
// a zero interval is a no-op (returns nil, nil).
//
// Each probe requests a reply (wantReply=true). The verdict comes from
// keepaliveTally: a failure with retries left reports the host unresponsive to
// the observer, the last failure closes the connection, and a success reports
// it responsive again. A nil observer is a no-op — the prober behaved this way
// before it had one and must still work when the composition root wires none.
//
// The returned stop function is safe to call only once (close of closed
// channel panics). In practice it is called exactly once from
// pooledSSHConn.Close's closeOnce guard.
func startKeepalive(target keepaliveTarget, interval time.Duration, countMax int, observe LivenessObserver) (func(), <-chan struct{}) {
	if interval <= 0 {
		return nil, nil
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	report := func(r Reachability) {
		if observe != nil {
			observe(r)
		}
	}
	// The budget one silent probe is allowed to spend, and it is the SAME
	// budget the tally spends on refusals: interval x countMax. Two ways to
	// lose a host — it answers "no" countMax times, or it answers nothing at
	// all — and a prober that gave the second one a different allowance would
	// be two policies for one question.
	//
	// It is deliberately NOT the interval. A loaded host answering in two
	// intervals is SLOW, not gone, and killing its session is lost work; the
	// slowness is reported by how long the probe took, never by ending it.
	budget := interval * time.Duration(max(countMax, 1))

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(doneCh)
		tally := keepaliveTally{countMax: countMax}
		for {
			select {
			case <-ticker.C:
				started := time.Now()
				answered, err := sendProbe(target, budget)
				rtt := time.Since(started)
				if err != nil && errors.Is(err, errProbeSilent) {
					// The probe never came back inside the budget. This is
					// TERMINAL and cannot be counted as one failure of
					// countMax, because there is no way to retry it: the
					// call is still parked inside x/crypto holding
					// globalSentMu, and the only thing that can free it is
					// closing the connection — after which there is nothing
					// left to probe. Report the loss on the way out so the
					// session's last word is "not answering" rather than
					// silence (nocx-iarf9).
					report(Reachability{Responsive: false})
					_ = target.Close()
					// And WAIT for it. Closing the transport is what unparks
					// the blocked call; returning before it does leaves a
					// goroutine on every probe, which is the same leak
					// wearing a fix.
					<-answered
					return
				}
				switch tally.probe(err == nil) {
				case keepaliveGiveUp:
					_ = target.Close()
					return
				case keepaliveUnresponsive:
					report(Reachability{Responsive: false})
				case keepaliveResponsive:
					report(Reachability{Responsive: true, RoundTrip: rtt})
				case keepaliveSteady:
					// A healthy link says nothing about its LIVENESS — that
					// is what "steady" means and why it was silent. It does
					// say how long it took, and that is a different fact
					// with a different consumer: the indicator that tells a
					// person their host has become slow. Reported every
					// probe; whoever consumes it decides what is worth
					// publishing (internal/session grades it and republishes
					// only when the grade changes, so a healthy connection
					// still puts nothing on the wire).
					report(Reachability{Responsive: true, RoundTrip: rtt})
				}
			case <-stopCh:
				return
			}
		}
	}()
	return func() { close(stopCh) }, doneCh
}
