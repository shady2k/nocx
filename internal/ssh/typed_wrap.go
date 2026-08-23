package ssh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shady2k/nocx/internal/log"
)

// The typed-`ssh` wrapper (ADR-0035, design §4.3 and §4.4).
//
// What the binding texts already decided, before this says what it builds.
// AD-5 keeps this inside Tier A: what changes is how a bundle travels, never
// what is deployed. AD-8 puts variation behind an interface at one
// composition root, which is why the multiplex adapter is another
// implementation of the transport seam (internal/ssh/mux) rather than a mode
// flag. ADR-0015 made `ssh -G` the oracle for the user's configuration, so
// everything this file needs to know before rewriting a line — whether a
// RemoteCommand is configured, whether the user expressed their own multiplex
// policy, and where their socket would land — is ASKED, never parsed out of a
// config by us. ADR-0025 refused a pass-through for the user's typed options
// in a composed line; this does not widen it, because there is no composed
// line to inject into: we keep the user's own words and add two options of
// our own.
//
// # What it does
//
// For an accepted line we keep THE USER'S OWN `ssh` PROCESS AND ITS ARGV and
// add our `ControlMaster` and `ControlPath`. That is what keeps the agent,
// ProxyJump, an interactive password, keyboard-interactive and 2FA, the
// host-key prompt, identity selection, port forwards, their own -F and -o,
// and THE PROCESS'S EXIT STATUS all working: we did not reimplement any of
// them. The user's process is the transport master and carries the
// interactive session; auxiliary channels are opened on it after ownership is
// proven, and there is never a second interactive session.
//
// # Why a refusal here is cheap and a mistake here is not
//
// Every refusal below is decided BEFORE ANYTHING HAPPENS, and the line then
// runs with no nocx remote effect of any kind. That is not caution for its
// own sake: the multiplex spike measured that an over-long `ControlPath` does
// not degrade to no-multiplexing — `ssh` refuses to start, so there is no
// connection, no session and no user's shell — and that a socket directory
// that is missing or unwritable ends the same way. That failure class is
// worse than losing integration and is preventable only by construction, so
// the path is built from a short prefix and `%C`, the directory is created
// before the line is submitted, and if no safe short path can be built the
// session is decided raw before it is attempted.

// Control-socket bounds and shape.
const (
	// maxControlPathLen is the bound on the EXPANDED socket path. A unix
	// socket's sun_path is 108 bytes on Linux and 104 on macOS, and ssh
	// refuses to start rather than degrade when the path is longer. The
	// smaller of the two is the bound we hold ourselves to, so a path that
	// is safe here is safe on both — and it is checked against the oracle's
	// expansion rather than our template, because %C is 40 characters that
	// only ssh can produce.
	maxControlPathLen = 100

	// controlSocketPrefix names the sockets we create. Short by design: the
	// %C hash that follows is 40 characters and the whole path has to fit
	// the bound above regardless of how long the user's $HOME or hostname
	// is.
	controlSocketPrefix = "m-"

	// controlSocketMode is the directory mode. The control socket IS the
	// trust boundary — the mux protocol does not isolate destinations, so a
	// socket another user could plant is a socket that could answer for a
	// destination it was not created for.
	controlSocketMode os.FileMode = 0o700

	// expandedSocketNameLen is what ssh appends to the root once %C is
	// expanded: the separator, our prefix, and the hash — 40 hexadecimal
	// characters, measured against OpenSSH rather than assumed
	// (TestTypedWrapLive_TheOracleExpandsTheSocketPathPerDestination pins
	// the shape, `/m-[0-9a-f]{40}$`). It is the budget the ROOT does not
	// have: a root longer than maxControlPathLen-expandedSocketNameLen
	// cannot hold a socket ssh will open, whatever else is right about it.
	expandedSocketNameLen = 1 + len(controlSocketPrefix) + 40
)

// New refusal classes for the typed path (design §4.4). Each is decided
// before authentication, and each leaves the user's own line to run.
const (
	// ReasonUserMultiplexPolicy: the user expressed their own ControlMaster,
	// ControlPath or ControlPersist — as -M, -S, or -o, or in their config,
	// which is the same expression seen through the oracle. We never
	// override it. Reusing a master the user already runs is rejected in
	// ADR-0035 rather than deferred: liveness is checkable (`ssh -O check`
	// answers) but liveness is not identity — our own spike measured a mux
	// master accepting a session request that named a DIFFERENT destination
	// and executing it on its own connection.
	ReasonUserMultiplexPolicy RefusalReason = "user-multiplex-policy"

	// ReasonNoControlPath: no safe short control socket path could be built.
	// This covers the oracle failing to answer as well — without an answer
	// we do not know where the socket would land, and guessing is the one
	// mistake that costs the user their connection rather than their
	// integration.
	ReasonNoControlPath RefusalReason = "no-control-path"

	// ReasonSecretUnavailable: the per-session secret could not be
	// generated, so there is nothing to deliver and no reason to interpose.
	ReasonSecretUnavailable RefusalReason = "secret-unavailable"
)

// TypedInvocation is the user's own `ssh` line as the shell collected it:
// their option words in their order, and the destination they named. It is
// deliberately the same three-field destination shape ADR-0025 fixed, plus
// the options as separate tokens — each is one argv entry on the user's side
// and must stay one here.
type TypedInvocation struct {
	Opts []string
	Host string
	User string
	Port int
}

// Destination renders the `user@host` positional exactly as the user reached
// it.
func (inv TypedInvocation) Destination() string {
	if inv.User == "" {
		return inv.Host
	}
	return inv.User + "@" + inv.Host
}

// TypedWrap is an accepted line's decision: the options we add, and the
// socket we will prove ownership against.
type TypedWrap struct {
	// MuxOptions are the option words we add, in order. They are ours and
	// only ours — the user's own words are never rewritten.
	MuxOptions []string
	// ControlPath is the EXPANDED socket path, as the oracle resolved it
	// with our options in the argv. It is what mux.Open is pointed at;
	// nothing else may be.
	ControlPath string
}

// The LINE ITSELF is composed by the composition root, not here: quoting the
// user's option words is shellintegration.ShellQuote's job and internal/ssh
// does not depend on that package (the same reason RemoteInstaller is
// declared in this package rather than there). What this package owns is the
// DECISION — whether to interpose, and on which socket.

// TypedWrapper decides, for one typed line, whether nocx interposes at all
// and on which socket.
type TypedWrapper struct {
	log      log.Logger
	resolver ConfigResolver
	root     string
}

// NewTypedWrapper returns the wrapper. controlRoot is the directory our
// sockets live in, and it is a PARAMETER rather than a default with an
// override: where a control socket lives is a product decision — it decides
// who can plant one and how long the expanded path may be — and the
// composition root is where product decisions belong. DefaultControlRoot is
// what it passes.
func NewTypedWrapper(lg log.Logger, resolver ConfigResolver, controlRoot string) *TypedWrapper {
	return &TypedWrapper{log: lg, resolver: resolver, root: controlRoot}
}

// DefaultControlRoot is where nocx's own control sockets live: short, because
// the expanded path has a hard bound, and per-uid, because the control socket
// is the trust boundary and a directory another user could own is a socket
// another user could plant.
//
// SHORT IS A MEASUREMENT, NOT AN ADJECTIVE, and this returned os.TempDir()
// alone until macOS measured it. A macOS per-user confinement directory —
// /var/folders/<2>/<30>/T, which is what $TMPDIR names for a logged-in user
// and for a GUI-launched app alike — is 48 characters before we add
// anything; `nocx-mux-501` makes 61; ssh's expansion adds 43. That is 104 —
// past the bound here, and past the kernel's too: macOS's sun_path is 104
// BYTES, which a 104-character path cannot fit because the terminator needs
// one of them. So the wrapper refused every typed ssh with
// ReasonNoControlPath and the whole
// typed path was dead on the platform nocx ships first. Nothing was wrong
// with the refusal — it is the honest answer to a socket that cannot be
// bound — and nothing was wrong with the bound. What was wrong was choosing
// a base without asking whether a socket fits in it.
//
// So the base is chosen rather than assumed, and $TMPDIR keeps its
// precedence where it fits: it is private per-user, and it is what a test,
// a sandbox or a service manager redirects. /tmp is the fallback because it
// is the one short directory every unix has. It is SHARED, which the per-uid
// name and ensureControlRoot answer between them: a directory some other
// user owns cannot be chmod'ed to 0700 by us, so the wrapper refuses it and
// the line runs as plain ssh — a name another user can squat costs the
// integration and can never yield them our socket.
//
// If neither base fits — a uid long enough to push even /tmp past the
// budget — this still returns a root, and Wrap's bound is still the last
// word: the answer is a named refusal, never an unbindable socket.
func DefaultControlRoot() string {
	name := fmt.Sprintf("nocx-mux-%d", os.Getuid())
	for _, base := range []string{os.TempDir(), "/tmp"} {
		root := filepath.Join(base, name)
		if len(root)+expandedSocketNameLen <= maxControlPathLen {
			return root
		}
	}
	return filepath.Join("/tmp", name)
}

// muxPolicyOptions are the option spellings that mean "the user has expressed
// their own multiplex policy". -M and -S are the short forms; the -o forms
// are matched case-insensitively because OpenSSH's keywords are.
var muxPolicyKeywords = []string{"controlmaster", "controlpath", "controlpersist"}

// Wrap decides the typed line. ok=false is a refusal with a named reason, and
// the caller then runs the user's line unchanged — no socket, no directory
// entry, no oracle-driven rewrite, nothing on the far host.
func (w *TypedWrapper) Wrap(ctx context.Context, inv TypedInvocation) (TypedWrap, RefusalReason, bool) {
	// 1. The user's own words, before anything is asked of anything. A -M,
	//    a -S or an -o naming one of the three keywords is the user saying
	//    it in the shortest way there is, and it is answerable without the
	//    oracle — so it is answered without the oracle, and the refusal
	//    costs not even a subprocess.
	if reason, refused := refuseOwnMultiplexWords(inv.Opts); refused {
		w.refused(inv, reason, "the user expressed their own multiplex policy on the line")
		return TypedWrap{}, reason, false
	}

	// 2. The oracle, about the user's line exactly as they typed it. Our
	//    own options must not be in this argv: they would answer the next
	//    two questions about OUR line rather than theirs.
	userCfg, err := w.resolver.ResolveArgv(ctx, oracleArgv(nil, inv))
	if err != nil {
		w.refused(inv, ReasonNoControlPath, "the ssh -G oracle could not answer for this destination")
		return TypedWrap{}, ReasonNoControlPath, false
	}
	if userCfg.RemoteCommand != "" {
		// OpenSSH refuses to run a command-line remote command beside a
		// configured one ("Cannot execute command-line and remote
		// command"), so there is no line to build.
		w.refused(inv, ReasonRemoteCommand, "the destination configures a RemoteCommand")
		return TypedWrap{}, ReasonRemoteCommand, false
	}
	if userCfg.ControlMaster != "" || userCfg.ControlPath != "" || userCfg.ControlPersist != "" {
		w.refused(inv, ReasonUserMultiplexPolicy, "the destination's configuration expresses a multiplex policy")
		return TypedWrap{}, ReasonUserMultiplexPolicy, false
	}

	// 3. The socket. The template is a short prefix plus %C — a 40-character
	//    hash ssh computes from the local host, the remote host, the port
	//    and the remote user — which bounds the path regardless of how long
	//    the user's $HOME or hostname is, and makes a collision between two
	//    destinations impossible by construction for the sockets we create.
	template := filepath.Join(w.root, controlSocketPrefix+"%C")
	muxOpts := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + template,
		// The master must not outlive the user's session: §6.2's ownership
		// interval closes when the last owned session and auxiliary channel
		// have finished, and a persisting master is a footprint with no
		// end. `no` is also the default, so this states the interval rather
		// than changing it.
		"-o", "ControlPersist=no",
	}

	// The directory is created BEFORE the line is submitted and is checked
	// to be ours afterwards. Both halves matter: ssh does not create it, and
	// a directory that already exists and is not ours is a socket somebody
	// else could answer on.
	if rootErr := w.ensureControlRoot(); rootErr != nil {
		w.refused(inv, ReasonNoControlPath, "no control socket directory could be owned: "+rootErr.Error())
		return TypedWrap{}, ReasonNoControlPath, false
	}

	// The expansion is ssh's to compute, so it is ssh that is asked. Only
	// the answer is bounded here — the template's own length says nothing
	// about the path that will exist.
	wrappedCfg, err := w.resolver.ResolveArgv(ctx, oracleArgv(muxOpts, inv))
	if err != nil {
		w.refused(inv, ReasonNoControlPath, "the ssh -G oracle could not expand the control path")
		return TypedWrap{}, ReasonNoControlPath, false
	}
	expanded := wrappedCfg.ControlPath
	if expanded == "" {
		w.refused(inv, ReasonNoControlPath, "the oracle reported no control path for the wrapped line")
		return TypedWrap{}, ReasonNoControlPath, false
	}
	if len(expanded) > maxControlPathLen {
		w.refused(inv, ReasonNoControlPath,
			fmt.Sprintf("the expanded control path is %d bytes, past the %d-byte bound", len(expanded), maxControlPathLen))
		return TypedWrap{}, ReasonNoControlPath, false
	}

	return TypedWrap{MuxOptions: muxOpts, ControlPath: expanded}, ReasonNone, true
}

// refused logs every refusal by name. A refusal that only the code knows
// about is the log-only degrade AGENTS.md forbids; this is the backend half,
// and the caller carries the reason to the product.
func (w *TypedWrapper) refused(inv TypedInvocation, reason RefusalReason, why string) {
	if w.log == nil {
		return
	}
	w.log.Info("typed ssh: nocx does not interpose; the line runs as plain ssh",
		"host", inv.Host, "user", inv.User, "port", inv.Port, "reason", string(reason), "why", why)
}

// ensureControlRoot creates the socket directory 0700 and refuses anything
// that is not a directory we own at that mode. os.MkdirAll applies the
// process umask, so the mode is set explicitly afterwards — "modes are set at
// creation, never left to umask".
func (w *TypedWrapper) ensureControlRoot() error {
	if w.root == "" {
		return fmt.Errorf("no control socket root is configured")
	}
	if err := os.MkdirAll(w.root, controlSocketMode); err != nil {
		return err
	}
	if err := os.Chmod(w.root, controlSocketMode); err != nil {
		return err
	}
	// Lstat, not Stat: a symlink at the root is somebody else's directory
	// wearing our name.
	fi, err := os.Lstat(w.root)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", w.root)
	}
	if perm := fi.Mode().Perm(); perm != controlSocketMode {
		return fmt.Errorf("%s is mode %o, want %o", w.root, perm, controlSocketMode)
	}
	return nil
}

// refuseOwnMultiplexWords answers "did the user express a multiplex policy on
// this line" from the words alone.
func refuseOwnMultiplexWords(opts []string) (RefusalReason, bool) {
	for i, o := range opts {
		if o == "-M" || strings.HasPrefix(o, "-S") {
			return ReasonUserMultiplexPolicy, true
		}
		if o != "-o" {
			// An attached form: -oControlPath=… is one token.
			if strings.HasPrefix(o, "-o") && matchesMuxKeyword(o[2:]) {
				return ReasonUserMultiplexPolicy, true
			}
			continue
		}
		if i+1 < len(opts) && matchesMuxKeyword(opts[i+1]) {
			return ReasonUserMultiplexPolicy, true
		}
	}
	return ReasonNone, false
}

// matchesMuxKeyword reports whether an -o argument names one of the three
// multiplex directives. OpenSSH accepts `Keyword=value` and `Keyword value`
// and is case-insensitive about the keyword, so all three shapes are matched.
func matchesMuxKeyword(arg string) bool {
	key := strings.ToLower(strings.TrimSpace(arg))
	if i := strings.IndexAny(key, "= \t"); i >= 0 {
		key = key[:i]
	}
	for _, k := range muxPolicyKeywords {
		if key == k {
			return true
		}
	}
	return false
}

// oracleArgv builds the `ssh -G` argv: our options (empty for the question
// about the user's own line), then the user's options in their order, then
// the port and the destination — the same shape the renderer's plan builds,
// so one getopt answers for both.
func oracleArgv(muxOpts []string, inv TypedInvocation) []string {
	argv := make([]string, 0, 4+len(muxOpts)+len(inv.Opts))
	argv = append(argv, "ssh", "-G")
	argv = append(argv, muxOpts...)
	argv = append(argv, inv.Opts...)
	if inv.Port != 0 {
		argv = append(argv, "-p", strconv.Itoa(inv.Port))
	}
	argv = append(argv, inv.Destination())
	return argv
}
