package proto

// The LEASE and the NAMED PROBES: the ops by which the coordinator asks this
// machine's helper to run the fixed shell commands nocx owns, on a connection
// the helper dialed and keeps pooled.
//
// # Why these exist at all (D3)
//
// The coordinator used to hold its own ssh client for exactly these questions:
// what platform is this host, where is its home, which ports are listening,
// what does its shell think the line should be, which commands are on its PATH.
// The owner's invariant (2026-09-13) moves every dial to the helper, and the
// helper wire refuses a free-form command — `host.Schema` refuses a bare
// `[]string`, and a caller-supplied command string is the capability this whole
// level exists to not hand out.
//
// So a command cannot cross, and that leaves two shapes to choose between: one
// op that carries a command under a different name (which is the thing being
// refused), or one CLOSED SET of named operations, each with typed parameters,
// whose command text lives in the helper's own build. This is the second.
//
// # The text of each command lives in internal/remoteprobe
//
// A leaf package both ends link, so that "what nocx runs on a host" has one
// spelling rather than one per process. Nothing in these params names a
// command, a script or an argv; what a caller supplies is a lease, a member of
// a closed enum, and typed arguments (a nonce, a completion line).
//
// # A lease, and why it is not a channel
//
// A probe asked with no reference held would acquire and release inside the one
// request, closing the connection for the next probe to redial it — a second
// authentication per sample, on hosts where that is how an account gets banned.
// So `lease` takes a pooled reference and answers its id, every probe op runs
// its command on the connection that reference keeps alive, and `unlease` drops
// it. See lease_id.go.

// OpLease acquires one pooled reference for a destination.
//
// It is the ssh service's own `open` for a caller that wants no STREAM: the
// destination is the same resolved triple, the credential is the same identity,
// and what comes back is an id rather than a channel. A probe lease holds a
// connection open; it moves no bytes of its own.
const OpLease = "lease"

// OpUnlease releases one pooled reference. Like `close` and `unforward` it is
// idempotent: an id this helper does not hold is answered as released, because
// the ordinary caller is a consumer shutting down and a second release is not a
// disagreement about state.
const OpUnlease = "unlease"

// The named probes. One op each, because each has its own parameters and its
// own result: a new FIELD on a frozen op is a break (additionalProperties:
// false), while a new op is one an older helper answers `unknown_op` to — and
// an older helper is exactly what a coordinator has to be able to read.
const (
	// OpUname is the platform probe (D20): the kernel and machine in one line.
	// The caller maps the answer onto Go's GOOS/GOARCH vocabulary — the helper
	// returns what the host said and decides nothing about it.
	OpUname = "uname"
	// OpHome answers where the account's home directory is.
	OpHome = "home"
	// OpSamplePorts runs ONE rung of port discovery's ladder, named by the
	// caller. The rung is a member of a closed set and not a command: the
	// caller chooses WHICH probe to run, and the helper owns what it is.
	OpSamplePorts = "sample-ports"
	// OpCompletion runs the shell-completion probe for one line at one caret.
	//
	// It carries the line, and that is not a command crossing: the line is an
	// ARGUMENT to a fixed script, quoted by the composer, and the script is the
	// same bytes whatever is in it. What a caller can influence is the answer
	// it gets back, which is the point of asking.
	OpCompletion = "completion"
	// OpCommandNames runs one half of the PATH enumeration: the cheap
	// invalidation probe, or the full scan. Two members of a closed set rather
	// than a flag, because the two are bounded differently.
	OpCommandNames = "command-names"
)

// LeaseParams asks for one pooled reference to a resolved destination.
//
// Host, Port and User are RESOLVED values and the identity is the same one
// `probe` and `open` take, for the reason stated there at length: alias
// resolution, ~/.ssh/config merging and the credential's authorization against
// an endpoint stay in the coordinator, which is the party that reads the config
// and holds the binding.
type LeaseParams struct {
	Destination SSHDestination `json:"destination"`
	// AcceptOnTrust is the coordinator's answer to "may a host key this host
	// has never presented be recorded", carried exactly as `probe` carries it:
	// the helper may not decide that for itself. False is the ordinary value
	// and produces the `host-key-unknown` refusal below.
	AcceptOnTrust bool `json:"acceptOnTrust"`
}

// LeaseResult names the reference and reports the fingerprint observed at dial
// time.
//
// HostKeyFingerprint is not decoration: the helper-install path keys CONSENT by
// the machine's host key (ADR-0023), and its probe used to read the value off
// its own lease. Keeping it here means the coordinator's answer to "which
// machine did I just ask" is unchanged by the dial having moved one process
// out.
type LeaseResult struct {
	Lease              LeaseID `json:"lease"`
	HostKeyFingerprint string  `json:"hostKeyFingerprint"`
}

// UnleaseParams releases one reference.
type UnleaseParams struct {
	Lease LeaseID `json:"lease"`
}

// UnleaseResult is the empty answer an idempotent release gives. It is a named
// struct rather than `any` so the wire shape is declared once and can be
// frozen; an empty JSON object is its whole content.
type UnleaseResult struct{}

// ProbeRunParams is what every named probe needs and nothing more: the lease it
// runs on.
//
// It is embedded rather than repeated so that "a probe runs on a lease" has one
// declaration — and so that a probe added later cannot accidentally grow its own
// destination, which would be a second way to say which connection this is.
type ProbeRunParams struct {
	Lease LeaseID `json:"lease"`
}

// UnameParams is the platform probe: no argument beyond its lease. The host is
// asked what it is; there is nothing for a caller to narrow.
type UnameParams struct {
	ProbeRunParams
}

// HomeParams is the home-directory probe: no argument beyond its lease.
type HomeParams struct {
	ProbeRunParams
}

// HomeResult is the home probe's answer.
//
// It is a VALUE rather than a ProbeExecResult, and it is the one probe that
// differs, for a reason worth stating: this probe is two commands and a rule
// ("ask, then fall back to the tilde"), and the rule belongs to the probe's
// definition — internal/remoteprobe's list — not to each caller. A raw stream
// would move that rule one process out and make every consumer re-implement it,
// with the two copies disagreeing the day one of them gained a step.
//
// Empty means the host could not answer, which is a state the caller reports
// rather than guesses around.
type HomeResult struct {
	Home string `json:"home"`
}

// PortProbe names one rung of the port-discovery ladder, in the spellings
// internal/discovery uses for them (the name is what a person sees reported as
// "which dialect produced this sample").
//
// The two halves are spelled here rather than imported because this package is
// the wire's leaf: it is linked by every helper, including the untagged artifact
// deployed to somebody else's host. TestPortProbeSpellingsMatchTheProbeVocabulary
// is the guard that keeps the two in step, and it lives with the code that
// converts between them (internal/helper/sshsvc).
type PortProbe string

const (
	PortSS             PortProbe = "ss"
	PortNetstat        PortProbe = "netstat"
	PortBusyboxNetstat PortProbe = "busybox-netstat"
	PortLsof           PortProbe = "lsof"
	PortSockstat       PortProbe = "sockstat"
)

// SamplePortsParams runs one ladder rung.
type SamplePortsParams struct {
	ProbeRunParams
	Probe PortProbe `json:"probe"`
}

// CompletionParams runs the completion probe for one line at one caret.
//
// Cwd and Line are the user's and they are the reason the command is composed
// from a fixed script: both are quoted ARGUMENTS to it. Pos is the caret's byte
// offset in the line and Limit the caller's own bound on candidates — the
// script applies it, and the answer is framed by Nonce so a banner-polluted
// reply is rejected whole rather than half-parsed.
type CompletionParams struct {
	ProbeRunParams
	Cwd   string `json:"cwd"`
	Line  string `json:"line"`
	Pos   int    `json:"pos"`
	Limit int    `json:"limit"`
	Nonce string `json:"nonce"`
}

// CommandNamesPhase names which half of the PATH enumeration to run.
type CommandNamesPhase string

const (
	CommandNamesProbe CommandNamesPhase = "probe"
	CommandNamesScan  CommandNamesPhase = "scan"
)

// CommandNamesParams runs one half of the enumeration.
type CommandNamesParams struct {
	ProbeRunParams
	Phase CommandNamesPhase `json:"phase"`
	Nonce string            `json:"nonce"`
}

// ProbeExecResult is what every named probe answers: the captured streams, the
// remote exit status, and whether a capture bound was hit.
//
// It is ONE shape for all five ops, and deliberately: each caller classifies
// the same facts its own way — discovery treats exit 127 as "tool absent" and
// exit 1 from lsof as a valid empty sample, completion rejects an unframed
// answer, deploy refuses a nonzero status outright — and none of those are
// decisions the helper may take. A richer, per-op result would move each
// caller's judgement into this process and leave the caller unable to disagree
// with it.
//
// Truncated means the answer is a PREFIX: the capture bound was hit, or the
// remote died mid-write. A caller must never read a truncated answer as a
// complete one, which for port discovery would be the difference between "no
// listeners" and "I could not see them".
type ProbeExecResult struct {
	Stdout     []byte `json:"stdout"`
	Stderr     []byte `json:"stderr"`
	ExitStatus int    `json:"exitStatus"`
	Truncated  bool   `json:"truncated"`
}

// # The codes a PROBE op ends in, and why a probe that RAN is never one
//
// A probe's own failure to run is a refusal and not a result, for the reason
// the probe op states: "it did not run" and "it ran and said nothing" are
// different answers, and collapsing them is how a host that refuses exec looks
// like a host with no listeners.
//
// The code is one of these four, each naming a state the caller acts on
// differently. There is deliberately no code for a NONZERO EXIT: that is in the
// result, because it is an answer rather than a failure — `ss` missing, `lsof`
// finding nothing and a policy wrapper refusing are three different facts and
// their interpreter is the caller.
const (
	// ErrCodeExecSessionRefused means the far side would not give us a new
	// session channel. It is the SAME fact as ErrCodeChannelRefused, and it
	// keeps its own code because the sentences differ and a caller reads them:
	// "the host allows one session and your shell holds it" is a thing a person
	// can act on. (The two codes are not merged for the same reason `probe` and
	// `open` are not: a caller's next move differs.)
	ErrCodeExecSessionRefused = "exec_session_refused"
	// ErrCodeExecProhibited means the far side refuses exec requests at all:
	// ForceCommand, a restricted shell or an sshd policy.
	ErrCodeExecProhibited = "exec_prohibited"
	// ErrCodeExecLost means the transport under the probe died, including the
	// case where the helper no longer holds the lease it was asked about (a
	// helper that restarted holds nothing, and the connection that lease named
	// is gone with it).
	ErrCodeExecLost = "exec_lost"
	// ErrCodeExecTooLong means the composed command exceeded the bound nocx
	// imposes on its own probes, and was refused here rather than sent to die
	// in a remote execve.
	ErrCodeExecTooLong = "exec_too_long"
)
