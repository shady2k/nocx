package shellintegration

// Startup fidelity: the ONE place that says how an integrated session
// differs from the login a plain `ssh <host>` would have given the user on
// the same host, and the one place that names the system files the
// emulation reads (design §9, nocx-m8jwn.9).
//
// # Why one place
//
// Before this file the answer lived in three source comments — one per tier
// — each declaring its own equivalence set and its own deviation, and none
// of them able to say what the other two did. That is two answers to one
// question in the sense AD-8 names: they agreed everywhere anybody looked
// and disagreed where nobody did. Measured while writing this file: the zsh
// comment said the user's ~/.zlogin was "shadowed by the transient ZDOTDIR
// and not replayed", and it had not been true since the .zshrc phase began
// restoring ZDOTDIR — zsh resolves $ZDOTDIR again at every phase, so the
// user's own ~/.zlogin was already being read. A comment cannot be tested;
// this table is, by startup_fidelity_test.go.
//
// # What the reference is
//
// Not "an ordinary interactive shell" — a NATIVE LOGIN. The user's mental
// model is `ssh <host>`, which is sshd exec'ing their login shell with no
// command; every entry below is a difference from that, measured against
// that. So /etc/bash.bashrc is not on the list: a login bash does not read
// it either (a Debian login bash reaches it through /etc/profile, which the
// bash tier now sources, and an Arch one never reads it at all).
//
// # Why there is anything to declare
//
// Because nocx hands sshd a COMMAND rather than asking for a shell (design
// D1, kept). sshd then updates the login records and skips both the banner
// and the login shell, and every entry below is downstream of that one
// decision. It is not caused by the carrier change and is not fixed by it.

// etcDir is the system configuration directory the login emulation reads:
// the banner at etcDir/motd and the system profile at etcDir/profile.
//
// It is a variable for exactly one reason, and it is a RENDER-time value
// rather than a runtime knob: the exec tests cannot write to the real /etc,
// and assertion 37 asks for a fixture with a real /etc/motd read by a real
// shell. The rendered carrier and the rendered rcfile carry whatever this
// says at the moment they are built, so the SHIPPED bytes name the literal
// /etc paths and no far host has a switch that redirects either read —
// TestStartupFidelity_ShippedPathsAreTheRealOnes is what holds that.
var etcDir = "/etc"

// motdPath is the login banner sshd printed for an interactive session and
// skips for a command. hushloginFile is the file, in the user's home, that
// says "not on this host" — sshd consults it and we must too, because a
// banner the user already switched off is worse than no banner at all.
func motdPath() string { return etcDir + "/motd" }

// systemProfilePath is where a centrally managed fleet keeps PATH additions,
// proxy variables, module systems and corporate profile scripts. A shell
// that never reads it behaves differently from every other shell on the
// host, silently, and in a way the user attributes to the host.
func systemProfilePath() string { return etcDir + "/profile" }

const hushloginFile = ".hushlogin"

// systemProfileMode says how a tier comes by systemProfilePath(). It is the
// allowlist assertion 38 checks: a tier either observes the file or names,
// here, the reason it does not — and a tier that stops observing it without
// a declaration fails the test rather than shipping a silent difference.
type systemProfileMode string

const (
	// profileSourcedByNocx: the tier's own startup file sources it,
	// because the shell it starts is not a login shell and would never
	// reach it. bash.
	profileSourcedByNocx systemProfileMode = "sourced-by-nocx"
	// profileReadByLoginShell: the tier execs a login shell, which reads
	// the file itself, at its own hard-coded path, before anything of
	// ours runs. posix.
	profileReadByLoginShell systemProfileMode = "read-by-the-login-shell"
	// profileNotRead: the file is not read, and the declaration carries
	// why. zsh.
	profileNotRead systemProfileMode = "not-read"
)

// startupDeviation names one difference between an integrated session and a
// native login of the same shell on the same host. ID is a stable token —
// the value a wire result carries and a surface keys off; Detail is what
// differs, in the user's terms; Reason is why it is not closed. A deviation
// with an empty Reason is not a declaration, and the test refuses it.
//
// The tier it belongs to is the row's, not the entry's: an entry appears in
// exactly one row's Deviations (or, for the ones true of every tier, in all
// three), so carrying the shell twice would be two places to get it wrong.
type startupDeviation struct {
	ID     string
	Detail string
	Reason string
}

// The closed vocabulary of deviation ids. A new difference gets a new
// constant here, which is what makes "every declared deviation" a countable
// set rather than a phrase.
const (
	devUserLoginFiles     = "user-login-files"
	devSystemProfile      = "system-profile"
	devShellNestingDepth  = "shell-nesting-depth"
	devBannerStaticOnly   = "banner-static-only"
	devLastLoginLine      = "last-login-line"
	devEnvAfterLoginFiles = "session-env-after-login-files"
)

// startupTier is one row: the tier, how it comes by the system profile, and
// what it still does not reproduce.
type startupTier struct {
	Shell         ShellKind
	SystemProfile systemProfileMode
	Deviations    []startupDeviation
}

// commonDeviations are true of every tier because they are properties of the
// banner emulation and of sshd's own login bookkeeping, not of any shell.
// Every row below appends them, so a row's Deviations is the WHOLE list for
// that tier and no caller has to remember to merge two.
var commonDeviations = []startupDeviation{
	{
		ID:     devBannerStaticOnly,
		Detail: "The banner is /etc/motd only; the dynamic motd is not produced.",
		Reason: "A dynamic motd is pam_motd's: it runs the scripts in /etc/update-motd.d as " +
			"the user and caches the result in /run/motd.dynamic. nocx does not execute " +
			"host scripts of its own accord, and a stale cached copy printed as though it " +
			"were fresh would be worse than the static file.",
	},
	{
		ID:     devLastLoginLine,
		Detail: "sshd's \"Last login:\" line is not reproduced.",
		Reason: "It comes from lastlog and wtmp, which no portable shell can read and which " +
			"differ per platform. A wrong last-login time is worse than none.",
	},
}

// startupFidelity is the table, and it DECIDES rather than describes:
// loginPhase below emits the system-profile source for a tier if and only if
// this table says the tier sources it. A row edited without the product
// following is not possible, and a product edited without the row following
// fails startup_fidelity_test.go — which is the whole difference between a
// declaration and a comment.
//
// The Detail and Reason strings name /etc paths literally. They are prose
// for a person, not the paths anything reads: what is read comes from
// motdPath() and systemProfilePath(), which the exec tests redirect.
var startupFidelity = []startupTier{
	{
		Shell:         ShellBash,
		SystemProfile: profileSourcedByNocx,
		Deviations: append([]startupDeviation{
			{
				ID: devUserLoginFiles,
				Detail: "~/.bash_profile, ~/.bash_login and ~/.profile are not read, and " +
					"~/.bash_logout is not run at exit. ~/.bashrc is read.",
				Reason: "A login bash reads the first of those three INSTEAD of ~/.bashrc, and " +
					"nearly every distribution's ~/.bash_profile sources ~/.bashrc itself — " +
					"so reading both would source ~/.bashrc twice, duplicating PATH entries " +
					"and prompt wrappers, and reading only the login file would drop the " +
					"interactive configuration every other terminal gives the user. There is " +
					"no portable way to tell from inside which of the two happened. " +
					"~/.bash_logout follows from the same fact: bash runs it only when a " +
					"LOGIN shell exits, and this one is not a login shell. The zsh tier has " +
					"no matching entry because ZDOTDIR is restored before the user's rc, so " +
					"their ~/.zlogout is found and run in its native place.",
			},
			{
				ID:     devShellNestingDepth,
				Detail: "SHLVL is higher than a native login's.",
				Reason: "The bootstrap is a chain of shells that exec one another — the loader, " +
					"the launch carrier, the tier's interpreter — and each increments SHLVL " +
					"on start. Rewriting it would be a lie told to every script that reads it.",
			},
		}, commonDeviations...),
	},
	{
		Shell:         ShellZsh,
		SystemProfile: profileNotRead,
		Deviations: append([]startupDeviation{
			{
				ID:     devSystemProfile,
				Detail: "/etc/profile is not read.",
				Reason: "A native login zsh does not read it either — zsh's system files are " +
					"/etc/zshenv, /etc/zprofile, /etc/zshrc and /etc/zlogin, and the tier runs " +
					"all four natively. Sourcing /etc/profile here would make a variable " +
					"visible under nocx that is invisible in every other zsh on the host, " +
					"which is the same defect pointing the other way. Distributions that " +
					"bridge the two (Debian's and Ubuntu's /etc/zsh/zprofile runs " +
					"`emulate sh -c 'source /etc/profile'`) do it natively, in the tier, and " +
					"are unaffected.",
			},
			{
				ID:     devShellNestingDepth,
				Detail: "SHLVL is higher than a native login's.",
				Reason: "The bootstrap is a chain of shells that exec one another — the loader, " +
					"the launch carrier, /bin/sh, then zsh — and each increments SHLVL on " +
					"start. Rewriting it would be a lie told to every script that reads it.",
			},
		}, commonDeviations...),
	},
	{
		Shell:         ShellUnknown,
		SystemProfile: profileReadByLoginShell,
		Deviations: append([]startupDeviation{
			{
				ID: devEnvAfterLoginFiles,
				Detail: "The session environment is not visible to /etc/profile or ~/.profile; " +
					"it appears with the integration, at the first prompt.",
				Reason: "An interactive POSIX shell reads $ENV — the minimal tier's only delivery " +
					"vehicle, since POSIX sh has neither --rcfile nor a startup directory — " +
					"after the login files and before the first prompt. The order is the " +
					"shell's, not ours.",
			},
			{
				ID:     devShellNestingDepth,
				Detail: "SHLVL is higher than a native login's.",
				Reason: "The bootstrap is a chain of shells that exec one another — the loader, " +
					"the launch carrier, /bin/sh, then the login shell — and each increments " +
					"SHLVL on start. Rewriting it would be a lie told to every script that " +
					"reads it.",
			},
		}, commonDeviations...),
	},
}

// startupTierFor returns the row for a tier. ShellAuto has no row of its own:
// it is a build-time intent that resolves to one of the three on the far
// host, so asking for its differences is a question with three answers and
// the caller must ask about the tier that actually ran.
func startupTierFor(kind ShellKind) (startupTier, bool) {
	for _, t := range startupFidelity {
		if t.Shell == kind {
			return t, true
		}
	}
	return startupTier{}, false
}

// loginPhase is the shell text a tier's startup file runs to reproduce the
// login phase sshd skipped, and it is the table's decision made real: a tier
// sources the system profile if and only if its row says profileSourcedByNocx.
// Everything else gets nothing — because it reads the file natively (the
// minimal tier execs a login shell) or because the row declares, with a
// reason, that it does not read it at all (zsh).
//
// It exists only on the remote path: a local child was never a login and must
// not acquire one (startupMode below).
//
// Position is the contract, not a preference. It runs after the first
// bootstrap-progress fact and before the user's ~/.bashrc — the same position
// a native login bash reads /etc/profile in, inside the same interactive
// shell (so the `[ "$PS1" ]` and `$BASH` arms distributions guard with are
// taken exactly as they would be natively), and inside the span the two
// progress facts bracket, so a system profile that execs or exits is reported
// as a startup that did not return rather than as silence.
func loginPhase(kind ShellKind) string {
	t, ok := startupTierFor(kind)
	if !ok || t.SystemProfile != profileSourcedByNocx {
		return ""
	}
	return `# The login phase sshd skipped (design §9; fidelity.go owns the set).
if [ -r "` + systemProfilePath() + `" ]; then
    . "` + systemProfilePath() + `"
fi
`
}

// startupMode says whether a rendered startup file stands in for a login.
type startupMode int

const (
	// remoteLogin: this rcfile is what the user got instead of the login
	// shell `ssh <host>` would have started, so it reproduces the login
	// phase that never ran.
	remoteLogin startupMode = iota
	// localChild: nocx started this shell itself, on this machine, out of
	// a session that is already logged in. There was no login to
	// reproduce, and manufacturing one would change a local session's
	// environment for a reason that does not apply to it.
	localChild
)
