// Package probeoutcome defines the results and failures of bounded shell probes.
package probeoutcome

// Kind classifies why a probe did not run. Zero is not a kind: a probe that ran
// returns a Result and no error.
type Kind int

const (
	// KindSessionRefused means the far side would not give us a new session
	// channel — MaxSessions reached, ordinarily because the user's own
	// interactive shell holds the only one. It is a channel-level refusal.
	KindSessionRefused Kind = iota + 1
	// KindExecProhibited means the far side refuses exec requests at all
	// (ForceCommand, a restricted shell, an sshd policy).
	KindExecProhibited
	// KindConnectionLost means the transport died under the probe.
	KindConnectionLost
	// KindLeaseClosed means the CALLER closed the lease while the probe was in
	// flight. It is a fact about us and never about the host, which is why it
	// is separate from KindConnectionLost even though both end the same way.
	KindLeaseClosed
	// KindCommandTooLong means the composed command exceeded the bound a remote
	// execve can carry. It is refused by nocx before anything is sent.
	KindCommandTooLong
)

// String names a kind for a log line. It is not the wire's spelling — the wire
// uses the ssh service's own refusal codes — and exists so a failure is
// readable wherever it is reported.
func (k Kind) String() string {
	switch k {
	case KindSessionRefused:
		return "session-refused"
	case KindExecProhibited:
		return "exec-prohibited"
	case KindConnectionLost:
		return "connection-lost"
	case KindLeaseClosed:
		return "lease-closed"
	case KindCommandTooLong:
		return "command-too-long"
	}
	return "unknown"
}

// Error is a classified probe failure. Kind is the transport-neutral fact; Err
// carries the underlying cause so errors.Is still reaches it.
type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return "remote probe: " + e.Kind.String()
	}
	return "remote probe: " + e.Kind.String() + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Result is one probe's answer: the captured streams, the remote exit status,
// and whether a capture bound was hit — a truncated answer is a PREFIX, and a
// partial answer must never surface as a complete one.
type Result struct {
	Stdout     []byte
	Stderr     []byte
	ExitStatus int
	Truncated  bool
}
