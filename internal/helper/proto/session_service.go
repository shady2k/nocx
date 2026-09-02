package proto

import (
	"encoding/hex"
	"errors"
	"time"
)

// The second half of the frozen helper ABI: what a session IS, how one is
// started, and the inventory of the sessions a generation holds.
//
// nocx-k6p18.1 froze the attach/ack/detach/reset half and deliberately stopped
// there, because freezing `spawn` and `sessions` without their semantics would
// have been worse than leaving them. Their semantics land here, with the
// service that answers them, and so do the shapes — this is the last moment
// either is free, since a published generation serves the shape it shipped for
// the life of its sessions.
//
// # Launch is the authority; the OS is evidence (D10)
//
// The single most likely design error in this area is to let /proc answer
// "what is this session". It cannot: argv is mutable by the process itself, a
// process can be replaced by exec, /proc does not exist on macOS and
// proc_pidinfo answers a different set of questions there. So the helper
// RECORDS what it launched, at the moment it launches it — LaunchRecord, which
// no later reading can contradict — and offers OS inspection separately, as
// Observation, which is nil when nobody could be asked. Two fields, never one:
// merging them would report a lie with the authority of a launch record.
//
// # No human-authored name, ever (D3)
//
// The helper may report DERIVED diagnostics — cwd, argv, foreground process,
// start time — because those are facts about a process and the OS is their
// source. It may not persist a name a person typed. In level 1 a friendly
// alias is a local projection owned by the local server; in level 2 the host's
// ledger becomes its owner. One owner ever, and TestTheHelperPersistsNoHuman-
// AuthoredName is that decision made enforceable.

// The session service's lifecycle operations. They are service-level additions
// to the frozen frame ABI: an older helper answers unknown_op without changing
// how frames are decoded.
const (
	// OpSpawn starts a shell under a new PTY and returns its inventory entry.
	OpSpawn = "spawn"
	// OpSessions is the inventory: every live host session this generation
	// holds.
	OpSessions = "sessions"
	// OpResize sets a session's window size.
	OpResize = "resize"
	// OpCloseSession deliberately ends one helper-hosted session and removes it
	// from the inventory.
	OpCloseSession = "close-session"
	// OpSignal sends one signal to the session's process group.
	OpSignal = "signal"
)

// CloseSessionParams deliberately ends one helper-hosted session.
type CloseSessionParams struct {
	Session HostSessionID `json:"session"`
}

// CloseSessionResult is empty; success is the answer that the session ended.
type CloseSessionResult struct{}

// SignalParams sends Signal to the session's process group.
type SignalParams struct {
	Session HostSessionID `json:"session"`
	Signal  int           `json:"signal"`
}

// SignalResult is empty; success is the answer that the signal was delivered.
type SignalResult struct{}

// The events a helper sends unsolicited, as TypeNotify frames on the same wire
// as the data frames — so a reader sees exactly which bytes each fact sits
// between.
const (
	// EventSessionReset is the LIVE reset of one subscriber whose cursor fell
	// behind the window's base. Its params are a SessionReset.
	EventSessionReset = "reset"
	// EventSessionExit is the process ending. Its params are a SessionExit.
	// The helper owns exit status (D3), and it is a notification rather than
	// only an inventory field because a reader waiting on a command must not
	// have to poll to learn it finished.
	EventSessionExit = "exit"
)

// Notification is the payload of a TypeNotify frame: a service, an event and
// the event's own params. It is shaped like Request minus the id, because an
// unsolicited fact has no answer — and it carries its service so a later
// service's events cannot be mistaken for this one's.
type Notification struct {
	Service string `json:"service"`
	Event   string `json:"event"`
	// Params is the event's payload. It is `any` rather than json.RawMessage
	// because the sender marshals a typed value; the receiver decodes the
	// whole notification into a shape naming the event it expects.
	Params any `json:"params"`
}

// WorkspaceID is D15's reservation: opaque, coordinator-minted, and NEVER a
// display name. Human names bring rename, collision, normalisation, case and
// guessability into execution-host policy, and the helper owns no policy.
//
// It is unused by this generation — the workspace is coordinator-owned and
// `workspace.Default` is a coordinator-side constant — and it is required to
// stay. Document 2 makes one workspace reachable from two machines at once and
// needs no wire break to do it, precisely because the room is carried from the
// first day. A later optimisation looking only at what is READ would find the
// field unused and remove it; TestSpawnAndSessionsCarryTheReservedWorkspace is
// what stops that.
type WorkspaceID string

// SpawnParams starts one shell under one PTY.
//
// There is no argv, and there is no command: host.Register refuses any op whose
// params carry a free-form []string (D3), and that refusal is the point rather
// than an obstacle. The helper resolves the login shell through
// internal/loginshell, which is the same owner the coordinator's own local PTY
// asks — one answer to "which shell", not two.
type SpawnParams struct {
	// Workspace is D15's reservation. Empty is legitimate today.
	Workspace WorkspaceID `json:"workspace"`
	// Cwd is where the shell starts. Empty means the helper's own default,
	// which is the user's home — the same resolution internal/pty already
	// makes, rather than a second answer to the same question.
	Cwd string `json:"cwd"`
	// Env are additional environment entries for the shell, as a MAP rather
	// than the []string exec wants. Not only because D3's registration rule
	// refuses the slice: a map cannot express a positional argument, so no
	// caller can smuggle argv through it, and a duplicate key is impossible
	// rather than last-wins.
	Env map[string]string `json:"env,omitempty"`
	// Cols and Rows are the initial window size.
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
	// WindowBytes is the bound on this session's output window (D8). The
	// coordinator decides and the helper applies, clamped to the helper's own
	// floor, ceiling and aggregate budget — and the session keeps the bound it
	// was given for its whole life, so changing the setting affects the next
	// session and never a running one.
	//
	// int64 rather than int so the conversion on a 32-bit host cannot
	// overflow, which D8 asks for by name. Zero means the helper's default.
	WindowBytes int64 `json:"windowBytes"`
	// Lifecycle is optional for conventional sessions. When present, the
	// helper passes its descriptor-side channel and these values to the shell.
	// The capability is never copied into argv or environment.
	Lifecycle *LifecycleLaunch `json:"lifecycle,omitempty"`
}

// LifecycleLaunch carries the coordinator-minted authenticated channel
// bootstrap to the helper. Addressing is public; Capability and Recovery are
// bearer values and must be consumed only by the shell integration rcfile.
type LifecycleLaunch struct {
	Lane       string `json:"lane"`
	Domain     string `json:"domain"`
	Epoch      uint64 `json:"epoch"`
	Capability string `json:"capability"`
	Recovery   string `json:"recovery"`
}

// SpawnResult is the new session's inventory entry — the same shape `sessions`
// returns, so a caller that spawned one and a caller that found one hold the
// same value and cannot drift into two decoders.
type SpawnResult struct {
	Entry SessionEntry `json:"entry"`
}

// SessionsParams asks for the inventory. The workspace filter is D15's
// reservation on the read side; empty means every session this generation
// holds, which is what level 1 always asks for.
type SessionsParams struct {
	Workspace WorkspaceID `json:"workspace"`
}

// SessionsResult is the inventory (D10). Sessions is never null on the wire:
// an empty inventory is `[]`, because a decoder distinguishing "no sessions"
// from "no answer" needs the empty array to arrive as one.
type SessionsResult struct {
	Sessions []SessionEntry `json:"sessions"`
}

// SessionEntry is one live host session as the helper knows it.
type SessionEntry struct {
	// Session is the durable handle, qualified by the generation that minted
	// it — which is this generation, since a helper can only report its own.
	Session HostSessionID `json:"session"`
	// Workspace is D15's reservation, echoed back as it was given.
	Workspace WorkspaceID `json:"workspace"`
	// StartedAt is when the helper spawned the process, in RFC 3339 with
	// nanoseconds and an offset. A wall-clock time rather than a monotonic
	// duration because the reader is a different process on a different
	// machine, and "how long ago" is a question only the reader's own clock
	// can answer honestly.
	//
	// It is the HELPER's record and not the kernel's. Observation.StartTime
	// is the kernel's answer about the process wearing this pid now, and the
	// pair is the pid-reuse guard: the two disagreeing is the only way
	// anything can notice that the pid was recycled. Never read one for the
	// other.
	StartedAt string `json:"startedAt"`
	// Launch is what the helper recorded when it spawned. AUTHORITY (D10).
	Launch LaunchRecord `json:"launch"`
	// Observed is what the OS says now. EVIDENCE, and null when the OS could
	// not be asked — never an empty record passed off as an answer, and never
	// an OMITTED field either: absent and null are different bytes, and a
	// reader must be able to tell "this generation reports no observation"
	// from "this generation does not send observations". That distinction is
	// what vault.status's missing defaultProvider cost a release to learn.
	Observed *Observation `json:"observed"`
	// Window is where the session's output stream currently stands. It is
	// what a reader with no position of its own attaches at: `base` is the
	// oldest byte that still exists, exactly as sessions.live's replayFrom
	// tells a fresh renderer today.
	Window WindowSpan `json:"window"`
	// LifecycleWindow is the bounded raw lifecycle-byte window. It is separate
	// from PTY output but uses the same Window/Resume owner and offsets.
	LifecycleWindow WindowSpan `json:"lifecycleWindow"`
	// Writer names the subscriber holding the session's one write capability,
	// and is null when nobody holds it — always present, like WriteGrant's own
	// Holder, so "nobody is writing" and "this helper does not say" are
	// different bytes.
	Writer *SubscriberID `json:"writer"`
	// WriterEpoch is that holder's lease, and zero when nobody holds it.
	WriterEpoch LeaseEpoch `json:"writerEpoch"`
	// Exit is the process's status once it has ended, and null while it runs.
	// The helper owns exit status (D3), and the entry keeps carrying it: a
	// coordinator replaced during a command comes back to an entry that can
	// still tell it how the command ended.
	Exit *SessionExitStatus `json:"exit"`
}

// LaunchRecord is what the helper recorded at the moment it spawned. Nothing
// read from the OS afterwards may overwrite it: this is the canonical identity
// of the session (D10), and OS inspection is a cross-check against it.
type LaunchRecord struct {
	// Shell is the binary the helper actually started, as exec resolved it.
	Shell string `json:"shell"`
	// Cwd is the directory the helper started it in — the resolved one, not
	// the requested one, so an empty request is answered with the answer.
	Cwd string `json:"cwd"`
	// Pid is the shell process. Pgid is its process group, which is what the
	// helper signals: it owns the PTY and its process group (D3).
	Pid  int `json:"pid"`
	Pgid int `json:"pgid"`
	// Cols and Rows are the size the session was started at. The CURRENT size
	// is not here: a resize changes it, so a launch record claiming it would
	// be a fact that silently goes stale.
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
	// WindowBytes is the bound this session actually got, after the helper's
	// floor, ceiling and budget were applied to what the coordinator asked
	// for. Reported rather than assumed: a caller whose request was clamped
	// must be able to see that it was.
	WindowBytes int64 `json:"windowBytes"`
}

// Observation is what the OS says about the process NOW. It is evidence: a
// cross-check against the launch record and a source of the derived
// diagnostics D3 permits, never the canonical identity.
type Observation struct {
	// Source names where the evidence came from — "procfs" on Linux, "sysctl"
	// on macOS. Evidence that cannot say where it came from cannot be weighed
	// against a launch record it contradicts, and the two sources answer
	// different subsets: see Unavailable.
	Source string `json:"source"`
	// Cwd is the process's current working directory. It CHANGES as the user
	// cds, which is exactly why it is here and not in the launch record.
	Cwd string `json:"cwd,omitempty"`
	// Argv is the shell's own argument vector as the OS reports it. It is
	// mutable by the process itself, which is why it is evidence — and it is
	// a []string in a RESULT, which D3's registration rule does not touch:
	// that rule refuses argv as an INPUT, because an input reaches a command
	// line.
	Argv []string `json:"argv,omitempty"`
	// ForegroundPgid is the process group the terminal is currently giving
	// input to, and ForegroundCommand its command name: "what is running in
	// this session right now", which is the diagnostic a person actually
	// wants. Zero and empty when the shell itself is in the foreground.
	ForegroundPgid    int    `json:"foregroundPgid,omitempty"`
	ForegroundCommand string `json:"foregroundCommand,omitempty"`
	// StartTime is when the KERNEL says this process began, RFC 3339 with
	// nanoseconds — the same spelling FormatTime gives every other time on
	// this wire.
	//
	// # Why this is not SessionEntry.StartedAt under a second name (nocx-k6p18.12)
	//
	// The two are adjacent and they are not the same fact, and the reason
	// they must both exist is the reason they may disagree. StartedAt is the
	// HELPER's record of when it spawned — authority, one entry up, and
	// unfalsifiable by anything read later. StartTime is the OS's answer
	// about whatever process now wears that pid — evidence, in here with the
	// rest of the evidence, and it is the PID-REUSE GUARD: a pid alone cannot
	// say whether the process answering today is the one we launched, and
	// (pid, startTime) can. Merge them and the guard has nothing to compare
	// against; give the evidence the authority's name and a reader cannot
	// tell which one it is holding.
	//
	// So they are told apart by WHERE THEY SIT rather than by a longer name:
	// `entry.startedAt` is what we did, `entry.observed.startTime` is what
	// the kernel says, and every field inside `observed` is already scoped by
	// that. A `processStartTime` here would be the only field in the record
	// carrying a prefix its siblings do not need.
	//
	// Its resolution differs by platform and neither platform is exact:
	// macOS reports microseconds, Linux reports hundredths of a second (see
	// the procfs source). It is an identity, not a stopwatch.
	StartTime string `json:"startTime,omitempty"`
	// Ppid is the parent the OS reports NOW. It is evidence rather than a
	// constant: a helper that dies leaves its sessions reparented, and the
	// entry then says so instead of implying the helper still owns them.
	Ppid int `json:"ppid,omitempty"`
	// State is the kernel's process state, in the closed vocabulary
	// ProcessState declares. It is the answer to "is this shell running,
	// sleeping, stopped or a zombie", which a stopped session and a live one
	// are indistinguishable without.
	State ProcessState `json:"state,omitempty"`
	// Unavailable names every diagnostic above that this inspector was asked
	// for and could not supply. It is ALWAYS present — `[]` when everything
	// asked for was answered — and never omitted, for the same reason
	// `observed` itself is never omitted.
	//
	// # Why a field rather than an empty value (nocx-k6p18.10)
	//
	// A missing diagnostic and a stale one must not look alike, and here they
	// otherwise would. Every field above is `omitempty`, so a diagnostic the
	// OS could not answer arrives as nothing at all — and a reader with
	// nothing falls back to the LAUNCH record, which was true once and goes
	// stale the moment the user cds. That is a stale value presented as a
	// current observation: exactly the lie the authority/evidence split exists
	// to prevent, arriving by the back door.
	//
	// macOS is where it bites and why this exists. There is no /proc, and the
	// only route to another process's working directory is
	// proc_pidinfo(PROC_PIDVNODEPATHINFO), which needs cgo. Nothing forbids
	// cgo; what it costs is the build, and the cost is named in
	// nocx-k6p18.14. sysctl answers
	// argv and the foreground command cgo-free and cannot answer cwd at all,
	// so the shipped platform reports `["cwd"]` here and a reader can say "we
	// do not know where this shell is" instead of showing where it started.
	//
	// The rule is per-OBSERVATION and not per-platform: a /proc read that was
	// refused is named the same way, because the reader's problem is identical
	// and the reason is not its business.
	Unavailable []Diagnostic `json:"unavailable"`
}

// Diagnostic names one derived observation the helper may report. The set is
// closed and matches Observation's own optional fields, so a reader can switch
// on it exhaustively: a free-form string here would let a later generation
// invent a name nothing understands, which is the same defect
// additionalProperties:false refuses one level up.
type Diagnostic string

const (
	// DiagnosticCwd is the process's current working directory — the one that
	// changes as the user cds, and the one macOS cannot answer cgo-free.
	DiagnosticCwd Diagnostic = "cwd"
	// DiagnosticArgv is the shell's own argument vector as the OS reports it.
	DiagnosticArgv Diagnostic = "argv"
	// DiagnosticForegroundCommand is the command name of whatever holds the
	// terminal's foreground group. It is named unavailable only when there
	// WAS a foreground group to ask about: a shell alone in the foreground is
	// an answer, said by omission, and not a diagnostic that went missing.
	DiagnosticForegroundCommand Diagnostic = "foregroundCommand"
	// DiagnosticStartTime is the kernel's own start time for the process —
	// the pid-reuse guard, and the fact whose absence quietly turns every
	// liveness answer into "some process has this pid".
	DiagnosticStartTime Diagnostic = "startTime"
	// DiagnosticPpid is the parent pid.
	DiagnosticPpid Diagnostic = "ppid"
	// DiagnosticState is the kernel's process state. It is named unavailable
	// both when the status read failed and when the kernel answered a code
	// this closed vocabulary cannot spell: inventing a value for a state we
	// cannot name is the same defect as leaving the field blank, one step
	// further from being noticed.
	DiagnosticState Diagnostic = "state"
)

// ProcessState is the kernel's process state, normalised into one closed
// vocabulary — because the two kernels spell it differently and a field
// carrying `R` from one and `2` from the other would be two facts wearing one
// name. Linux's /proc/<pid>/stat gives a letter, macOS's kinfo_proc gives an
// SRUN/SSLEEP/SSTOP/SZOMB integer, and both are mapped here rather than at the
// reader, which cannot know which kernel answered.
//
// The set is the INTERSECTION plus what one platform can say and the other
// cannot lie about. `uninterruptible` exists because Linux distinguishes it
// and a shell hung on a dead network mount is precisely the session a person
// is looking for; macOS does not distinguish it and reports `sleeping`, which
// is less precise and still true. A code outside this set is reported as the
// state being UNAVAILABLE — never as a value invented to fill the field.
type ProcessState string

const (
	// ProcessRunning is on a CPU or on a run queue. Linux `R`, macOS SRUN.
	ProcessRunning ProcessState = "running"
	// ProcessSleeping is an interruptible wait — what an idle shell at a
	// prompt is. Linux `S`, macOS SSLEEP.
	ProcessSleeping ProcessState = "sleeping"
	// ProcessUninterruptible is a wait no signal can break, which is how a
	// shell on a wedged mount looks. Linux `D`; macOS cannot distinguish it
	// and answers ProcessSleeping.
	ProcessUninterruptible ProcessState = "uninterruptible"
	// ProcessStopped is suspended — SIGSTOP, ^Z, or a debugger. Linux `T`
	// and `t`, macOS SSTOP. This is the state that makes a dead-looking
	// session explicable.
	ProcessStopped ProcessState = "stopped"
	// ProcessZombie has exited and nobody has reaped it. Linux `Z`, macOS
	// SZOMB. The session's window still exists; the shell does not.
	ProcessZombie ProcessState = "zombie"
)

// WindowSpan is the current extent of a session's output window: Base is the
// oldest offset that still exists, Written is the total ever produced. The
// interval it states has both ends, deliberately — a reader can tell from it
// exactly which offsets will be served and which will be reset.
type WindowSpan struct {
	Base    StreamOffset `json:"base"`
	Written StreamOffset `json:"written"`
}

// SessionExitStatus is how a session's process ended.
type SessionExitStatus struct {
	// Code is the exit status, or -1 when the process was killed by a signal
	// (Signal is then set) or when no status could be collected.
	Code int `json:"code"`
	// Signal is the signal that ended it, and zero otherwise.
	Signal int `json:"signal,omitempty"`
	// At is when the helper observed the end, RFC 3339 with nanoseconds.
	At string `json:"at"`
}

// SessionExit is the EventSessionExit notification's params.
type SessionExit struct {
	Session HostSessionID     `json:"session"`
	Status  SessionExitStatus `json:"status"`
}

// AckResult is deliberately empty, like ResizeResult: the answer to "did the
// cursor advance" is the absence of an error. It exists so every op has a
// result type, rather than one of them answering with a bare null that a
// decoder has to special-case.
type AckResult struct{}

// ResizeParams sets one session's window size.
type ResizeParams struct {
	Session HostSessionID `json:"session"`
	Cols    uint16        `json:"cols"`
	Rows    uint16        `json:"rows"`
}

// ResizeResult is deliberately empty: the answer to "did the resize land" is
// the absence of an error. It exists so the op has a result type at all, the
// way every other op does.
type ResizeResult struct{}

// FormatTime is how every time on this wire is spelled: RFC 3339, nanoseconds,
// with an offset. One spelling, in one place, because two would eventually be
// parsed by one decoder.
func FormatTime(t time.Time) string { return t.Format(time.RFC3339Nano) }

// ResumeAt is THE decision rule of the helper's bounded output window: is the
// requested offset still in the window, and where does the reader restart.
//
// nocx-k6p18.1 deliberately did not write it, so that it would land together
// with the window it decides for and have exactly one owner. That owner is
// here, beside the Resume shape that states its answer, and
// internal/transport's outputRing.snapshot — which has asked the same question
// since AD-9 — delegates its verdict to it rather than keeping a second
// derivation of the same predicate.
//
// The two windows differ in ONE thing and the difference is in the caller, not
// here: where a reset restarts. This window is capacity-reclaimed, so a
// request below the base is a fact about the stream — nobody ever held those
// bytes — and the honest restart is the oldest byte that still exists, with
// the hole stated. The coordinator's ring is lossless, so a byte leaves it
// only after a consumer passed it, and a request below ITS base is a stale
// cursor rather than a loss; that caller therefore reads only the verdict.
//
// base ≤ written is the caller's invariant, held by the window from the moment
// it is created until it is closed: base only advances by reclaiming bytes
// that were written, and written only grows.
func ResumeAt(base, written, requested StreamOffset) Resume {
	if requested < base {
		return Resume{
			Reset: true,
			From:  base,
			Gap:   &Gap{Start: requested, End: base, Reason: GapReasonWindow},
		}
	}
	if requested > written {
		// Ahead of the stream: a caller defect, and NOT a reset. Answering it
		// with a reset would tell the reader that bytes were lost which were
		// never produced — a false statement in the product. It is parked at
		// the end, where it waits for bytes that do not exist yet.
		return Resume{Resumed: true, From: written}
	}
	return Resume{Resumed: true, From: requested}
}

// SessionHex spells a session's 16 raw id bytes as the 32 lowercase hex
// characters HostSessionID.Session carries. The two spellings are one identity
// — the control plane addresses a session by the hex, the data frame by the
// raw bytes — and this pair is the only crossing between them, so a session
// reached on one plane is reachable on the other without a lookup.
func SessionHex(raw [16]byte) string { return hex.EncodeToString(raw[:]) }

// ErrSessionIDMalformed reports a session id that is not 32 hex characters.
var ErrSessionIDMalformed = errors.New("proto: session id is not 32 hex characters")

// SessionBytes is SessionHex's inverse.
func SessionBytes(s string) ([16]byte, error) {
	var out [16]byte
	if len(s) != 32 {
		return out, ErrSessionIDMalformed
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return out, ErrSessionIDMalformed
	}
	copy(out[:], raw)
	return out, nil
}
