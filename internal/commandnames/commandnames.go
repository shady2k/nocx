// Package commandnames answers "what can this session run" once per host
// instead of once per tab.
//
// The split, and why it is the whole design (carrier design §8). Enumerating
// the executables on a `PATH` is thousands of directory reads; the set they
// produce is identical for every session to the same host, so it is computed
// ONCE per cache key and shared. The genuinely session-local part — the
// shell's builtins, functions and aliases — is enumerated separately, by the
// session's own shell, under its own small budget, so we never claim a
// function defined in one tab exists in another. Two truths, two owners; the
// renderer unions them.
//
// Three defects this replaces, all of them present before it:
//
//  1. A budget that stopped WAITING, not WORKING. The shell's 250 ms grace
//     abandoned the wait and left the pipeline running, and the exit cleanup
//     group-killed only when the job happened to be a process-group leader —
//     otherwise it killed the subshell and orphaned the enumeration. The scan
//     now runs under internal/proc's supervisor, which owns its process
//     group: the deadline terminates the group, kills it, and reaps it, and a
//     result is published only if it completed inside the deadline.
//  2. Every session re-ran the whole scan. Ten tabs to one host in an hour
//     meant ten full enumerations of the same thousands of files.
//  3. The surface lied. A missing snapshot always read as "command names are
//     still loading", which is true only while a scan is running — never for
//     one that timed out, failed, or is being served from a stale cache.
//     Hence five states, each of which the product tells apart (§11.36).
//
// Invalidation is by EVENT, not by clock, and the reasoning matters more
// than the mechanism. The name set changes when a package is installed or
// removed, which is rare; a short expiry would restore exactly the unbounded
// per-session enumeration this package exists to remove. The signal is the
// mtime of each PATH directory — adding or removing an entry moves it —
// which is a handful of stat calls against an enumeration of thousands of
// files, cheap enough to run every session. Replacing a binary in place does
// not move the directory mtime and does not need to: the set of NAMES is
// what is cached. A change to PATH itself is already in the key. The age
// bound is a backstop against a filesystem that reports mtime unreliably,
// never the mechanism.
//
// The cache is backend-owned and lives only in memory. It dies with the
// application: a working day of tabs to one host shares a single scan, and a
// restart simply recomputes. That is a natural bound that costs nothing to
// maintain — no cross-restart state to invalidate, and no file on our disk
// holding something recomputed in seconds. Discovery grows no persistent
// footprint on either side.
//
// There is no `off` state. D6 keeps discovery on, bounded and shared rather
// than removed; inventing an off state here would smuggle back the decision
// that was rejected.
package commandnames

import (
	"context"
	"errors"
	"time"
)

// State is what the product can honestly say about the shared name set. Five
// values, and each one is a different fact a user needs (§11.36).
type State string

const (
	// StateRunning is the caller's own knowledge that its request has not
	// answered yet. Names never returns it — a call to Names either produces
	// a terminal state or blocks — so it is the renderer's to hold between
	// asking and being answered, and it is the only state under which
	// "still loading" is true.
	StateRunning State = "running"

	// StateReady means a scan completed for exactly this key and nothing
	// observed since has invalidated it.
	StateReady State = "ready"

	// StateStale means a rescan was needed and could not be had, so the
	// previous snapshot is being served — with its age, because a claim
	// about a cached set is only honest with one.
	StateStale State = "stale"

	// StateTimedOut means the scan did not finish inside its deadline.
	// Nothing partial is ever published, so it carries no names of its own.
	StateTimedOut State = "timed-out"

	// StateFailed means the scan or the probe could not be run at all.
	StateFailed State = "failed"
)

// Bounds. Every one is a number rather than an adjective, for the reason D6
// gives: keeping the feature obliges it to be bounded, not merely
// supervised.
const (
	// ScanDeadline bounds the whole enumeration process group.
	ScanDeadline = 5 * time.Second

	// ProbeDeadline bounds the cheap per-session invalidation probe, on this
	// machine and over a remote route alike.
	//
	// It is the SCAN's deadline and deliberately not §8's 250 ms. That 250 ms
	// is the budget for the shell's own session-local enumeration, which sits
	// in front of a user's first prompt; this probe is a backend operation
	// behind one request whose state the surface shows, and the session is
	// fully usable while it is outstanding. Holding it to a prompt's budget
	// would be a number chosen for the wrong side: a remote probe pays a
	// session request and a network round trip before it stats anything, and
	// a local one forks a `stat` per PATH directory — up to 32 — so on a
	// loaded machine or a distant host it would time out every session, and
	// a probe that always times out turns the cache into a permanent miss.
	// That is the unbounded per-session enumeration this package removes,
	// reintroduced through the deadline.
	ProbeDeadline = ScanDeadline

	// SessionLocalBudget and MaxSessionLocalNames are the SHELL's numbers for
	// the other half — the grace its first prompt grants the enumeration of
	// its own tables, and the cap on what that enumeration may emit. They are
	// declared here so both halves of §8's budget table are stated in one
	// place, and they are held to the shell tiers by
	// TestSessionLocalBudget_ShellsDeclareTheSameBounds: a number that lives
	// in two files and is enforced in neither is how the two drift apart.
	SessionLocalBudget   = 250 * time.Millisecond
	MaxSessionLocalNames = 4096

	// MaxSharedNames and MaxSharedBytes bound the shared result.
	MaxSharedNames = 8192
	MaxSharedBytes = 64 * 1024

	// MaxPathDirs bounds the invalidation probe: at most this many PATH
	// directories are stamped and compared.
	MaxPathDirs = 32

	// BackstopAge is the oldest a cached snapshot may be and still be served
	// at all. It is not the invalidation mechanism (see the package doc);
	// it is the bound past which a snapshot is no longer claimed.
	BackstopAge = time.Hour

	// MaxScanBytes bounds what a scan may write. It is generous against
	// MaxSharedBytes because the raw form is one name per line, unbounded
	// duplicates included, and the trimming happens here rather than there.
	MaxScanBytes = 4 * 1024 * 1024
)

// ErrScanDeadline is the error a Source returns when its scan was stopped by
// the deadline rather than failing. The distinction is the whole of §11.35's
// first clause: a deadline means a PARTIAL answer exists and must not be
// published, which is a different fact from "the host refused".
var ErrScanDeadline = errors.New("commandnames: scan deadline")

// Identity is the part of the cache key that is known before anything is
// asked of the far side.
type Identity struct {
	// Route is the resolved route identity — "local" for this machine, and
	// the resolved user@host:port for a remote session. Resolved, not typed:
	// two aliases for one host must share one scan.
	Route string

	// Generation is the installed integration generation. A new generation
	// can change what the session-local half reports and what the scan
	// script is, so it separates the entries rather than silently reusing
	// one computed by a different version of ourselves.
	Generation string
}

// DirStamp is one PATH directory and an opaque token that changes when that
// directory's contents change. The token is deliberately opaque: the probe
// resolves it with whatever the far side has (GNU stat, BSD stat, or an
// `ls -ld` line), and nothing here parses a time out of it — only equality
// is ever asked.
type DirStamp struct {
	Dir   string
	Stamp string
}

// Probe is the cheap per-session answer: who and what the far side is, its
// effective PATH, and a stamp per PATH directory.
type Probe struct {
	User        string
	ShellFamily string
	Path        string
	Stamps      []DirStamp

	// Stamped is false when the far side could not stamp at least one PATH
	// directory — no GNU stat, no BSD stat, no ls. Such stamps compare
	// equal to each other forever, so an entry recorded from them would
	// look current for as long as the application ran. The service serves
	// it as `stale` instead, which puts the degrade in front of the user
	// with the snapshot's age beside it, rather than leaving it to a log.
	Stamped bool
}

// Scan is the expensive answer: the command names on that PATH.
type Scan struct {
	Names []string

	// Truncated is set by a Source that hit its own bound before the
	// service's. Either way the result reports itself cut.
	Truncated bool
}

// Source is one place command names come from: this machine, or one remote
// route. It owns HOW the two questions are asked; the service owns when.
type Source interface {
	// Identity is the half of the key known without asking anything.
	Identity() Identity

	// Probe runs the cheap per-session invalidation probe.
	Probe(ctx context.Context) (Probe, error)

	// Scan runs the expensive enumeration. It returns ErrScanDeadline when
	// its deadline stopped the work — never a partial Scan with a nil error.
	Scan(ctx context.Context, p Probe) (Scan, error)
}

// Result is what one session gets.
type Result struct {
	State State

	// Names is the shared PATH name set, sorted and deduplicated. Empty for
	// every state except ready and stale — nothing partial is published.
	Names []string

	// Age is how old the served snapshot is. Zero unless State is stale:
	// a stale claim without an age is not a claim a user can weigh.
	Age time.Duration

	// Reason names the failure for timed-out and failed. Empty otherwise.
	Reason string

	// Truncated says the name set was cut at a bound.
	Truncated bool
}
