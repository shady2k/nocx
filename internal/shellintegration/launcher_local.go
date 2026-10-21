package shellintegration

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// LocalBashRcfile renders the bash rcfile for a LOCAL enhanced session
// (nocx-u7uh.21): the same template the remote bash tier uses, so "how a
// shell learns its addressing and its capability" has exactly one owner —
// launcher.go's LaunchOptions plus the rcfile builders. The caller writes
// the result to a transient file and starts `bash --rcfile <path> -i`
// (pty.Config.Command/Args). The capability and the one-shot recovery fence
// are substituted into the rcfile TEXT, never the environment, exactly as
// the remote tier (ADR-0024 decision 2).
//
// The @NOCX_BASH@ slot carries the embedded script: the template already
// rewinds an installer-era install from the user's ~/.bashrc (it sources
// the user's rc first) and reinstalls with THIS session's authenticators,
// which is what makes the local channel live rather than the installed
// generation's stale one.
//
// Bash and Zsh also have the coordinator launch form. Native Fish uses the
// helper-hosted adapter, whose binary I/O client is part of the same helper;
// the POSIX tier remains remote-only.
func LocalBashRcfile(opts LaunchOptions) (string, error) {
	if !opts.Enhanced || opts.SessionID == "" {
		return "", fmt.Errorf("shellintegration: local lifecycle bootstrap requires an enhanced session with a session id")
	}
	return bashRcfile(localChild, launcherEnvBlock(opts), localScript(bashScriptRaw, opts),
		capabilityLiteral(bashUnsetExport, opts.Capability, opts.Recovery)), nil
}

// localScript renders an authored script for a LOCAL pane: this machine's
// enabled agents, and the delivery that carries a tool-surface argument
// (agents.go). The embedded constants are the same rendering for a caller that
// named no agents, so the two spellings cannot disagree about what a pane is.
func localScript(raw string, opts LaunchOptions) string {
	if len(opts.Agents) == 0 {
		return rawScript(raw)
	}
	return renderScript(raw, opts.Agents, DeliveryLocal)
}

// rawScript is the embedded script as this build ships it: rendered for a
// local pane from the set this BUILD carries.
func rawScript(raw string) string { return renderScript(raw, shippedAgentNames(), DeliveryLocal) }

// LocalShellKind classifies the account's actual shell, never a substitute.
// Fish is a helper-hosted native tier; recognizing it here does not claim the
// helper-less remote dispatcher's TCP transport. Other shells remain ordinary
// terminals rather than being replaced with Bash.
func LocalShellKind(path string) ShellKind {
	name := strings.TrimPrefix(filepath.Base(path), "-")
	switch name {
	case "bash":
		return ShellBash
	case "zsh":
		return ShellZsh
	case "fish":
		return ShellFish
	default:
		return ShellUnknown
	}
}

// LocalLaunch is how a local enhanced session is started: the shell binary, its
// argv, the extra environment it needs, and the removal of whatever transient
// artefact the tier wrote. Cleanup releases launch resources after the child
// starts. Abort cancels pending in-memory delivery and removes the artefact on
// failure, when no shell exists to remove it.
//
// THERE IS NO BOOTSTRAP LINE, and that is a property now rather than a
// convention (nocx-1ebrs.8). This struct used to carry bytes a caller wrote
// into the terminal — a command line the launcher typed on the user's behalf —
// which is the one thing in this design that shares a channel with the person
// and gets echoed into their scrollback. No tier has one: bash and the POSIX
// shells take the script as an rcfile during startup, and zsh takes a
// transient ZDOTDIR. Nothing a pane starts with is ever typed.
type LocalLaunch struct {
	Command    string
	Args       []string
	Env        []string
	ExtraFiles []*os.File
	Cleanup    func()
	Abort      func()
	// Refusal keeps an unsupported native startup layout conventional, without
	// passing it a credential pipe, lifecycle socket or activation marker.
	Refusal RefusalReason
}

// LocalEnhancedLaunchInMemory builds the helper's launch shape. Unlike the
// coordinator's local launch, a helper-hosted shell cannot rely on a script
// installed under the host's home: the helper is the integration and the
// installed Level-0 substrate may be absent. The existing renderers still own
// every marker and hook; only their delivery changes.
//
// The three tiers differ only in HOW a shell accepts a file at a point in its
// own startup, which is the shell's semantics and not a preference. Bash takes
// `--rcfile /dev/fd/3`; the POSIX tier takes `ENV=/dev/fd/3`; zsh has neither
// and takes a transient ZDOTDIR (launcher_zsh.go). None of them is handed
// anything by TYPING, so nothing nocx starts with ever appears in the person's
// transcript. What crosses a descriptor is a script for the two rcfile tiers
// and, for zsh, two bearer LINES read before any user code runs.
func LocalEnhancedLaunchInMemory(shellPath string, kind ShellKind, opts LaunchOptions) (LocalLaunch, error) {
	if !opts.Enhanced || opts.SessionID == "" {
		return LocalLaunch{}, fmt.Errorf("shellintegration: in-memory launch requires an enhanced session with a session id")
	}

	switch kind {
	case ShellBash:
		rc, err := LocalBashRcfile(opts)
		if err != nil {
			return LocalLaunch{}, err
		}
		return pipeLaunch(shellPath, []string{"--rcfile", "/dev/fd/3", "-i"}, nil, rc)
	case ShellFish:
		return localFishLaunch(shellPath, opts)
	case ShellZsh:
		// A TRANSIENT ZDOTDIR, and no line typed into the terminal (nocx-1ebrs.8).
		//
		// What was here: the whole rendered script on fd 3, and `. /dev/fd/3`
		// WRITTEN INTO THE PTY as a bootstrap line. Two things were wrong with
		// that and only one of them looked like a defect. The visible one:
		// the tty's line discipline echoed the line the moment it was written
		// — before the shell had even initialised its line editor — and zle
		// then redrew the same pending line when it consumed it at the first
		// prompt, so a person saw nocx's plumbing twice in their scrollback.
		// The one that broke the product: the line waited in the tty's input
		// queue for as long as the user's startup took, and fd 3 is a number a
		// user's startup file may legally read, drain or close in the meantime
		// — so the integration arrived only if the rc happened to be fast and
		// tidy. Measured on a shipped Mac with a well-behaved rc that reads a
		// low descriptor: `. /dev/fd/3` answered `bad file descriptor`, and the
		// banner blamed the startup files for a delivery that was nocx's.
		//
		// zsh has no --rcfile, so the delivery zsh itself offers is ZDOTDIR:
		// it names a directory whose .zshenv, .zprofile and .zshrc zsh sources
		// in a native login's order, and our copies replay the user's own file
		// of each phase. Nothing is typed, so nothing is echoed and no
		// descriptor has to survive a startup: the .zshenv phase reads the two
		// bearers from fd 3 and closes it (capabilityFromDescriptor) BEFORE the
		// user's .zshenv runs — before any user code exists to touch it.
		//
		// The bearer therefore still never becomes a filesystem object: what
		// the transient directory holds is the public script text and the
		// descriptor's NUMBER (NOCX_CAP_FD), never a value (ADR-0024
		// decision 2, capability_source.go).
		dir, derr := writeLocalZDOTDIR(opts)
		if derr != nil {
			return LocalLaunch{}, derr
		}
		launch, lerr := pipeLaunch(shellPath, []string{"-l", "-i"}, localZshEnv(dir, opts),
			capabilityLines(opts))
		if lerr != nil {
			_ = os.RemoveAll(dir)
			return LocalLaunch{}, lerr
		}
		// A spawn that never happened leaves nothing behind. A spawn that did
		// does not remove the directory here: zsh has not read it yet. The
		// shell removes it in its .zshrc phase and an EXIT trap covers a
		// startup that dies before that (zshCleanupTrap) — and the directory
		// holds no bearer, so a shell killed with SIGKILL leaves nothing worse
		// than a temp directory.
		innerAbort := launch.Abort
		launch.Abort = func() {
			innerAbort()
			_ = os.RemoveAll(dir)
		}
		return launch, nil
	case ShellUnknown:
		// The same substitution, in the POSIX spelling. `export -n` is not in
		// POSIX and dash refuses it, which is what capabilityLiteral's own
		// `2>/dev/null` covers: the assignment stands either way and the worst
		// case is a variable that stays exported on a shell that cannot
		// un-export one.
		script := launcherEnvBlock(opts) + "\n" +
			capabilityLiteral(bashUnsetExport, opts.Capability, opts.Recovery) + "\n" +
			posixScript + "\n"
		return pipeLaunch(shellPath, []string{"-i"}, []string{"ENV=/dev/fd/3"}, script)
	default:
		return LocalLaunch{}, fmt.Errorf("shellintegration: %q has no in-memory enhanced tier", shellPath)
	}
}

func pipeLaunch(command string, args, env []string, script string) (LocalLaunch, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return LocalLaunch{}, fmt.Errorf("shellintegration: in-memory launch pipe: %w", err)
	}
	written := make(chan struct{})
	go func() {
		_, _ = io.WriteString(writer, script)
		_ = writer.Close()
		close(written)
	}()

	var once sync.Once
	closeReader := func() {
		_ = reader.Close()
	}
	cleanup := func() {
		// The child owns the duplicated reader. Closing only this parent's
		// reader lets the writer finish without truncating an in-flight script.
		once.Do(closeReader)
	}
	abort := func() {
		_ = writer.Close()
		<-written
		once.Do(closeReader)
	}
	return LocalLaunch{
		Command:    command,
		Args:       args,
		Env:        env,
		ExtraFiles: []*os.File{reader},
		Cleanup:    cleanup,
		Abort:      abort,
	}, nil
}

// localScriptFD is the descriptor the in-memory tier delivers on: fd 3, the
// first entry of LocalLaunch.ExtraFiles. Named rather than spelled, because
// the .zshenv's read, the bash arm's /dev/fd/3 and the POSIX arm's
// ENV=/dev/fd/3 are three spellings of one fact.
const localScriptFD = 3

// capabilityLines is what a local zsh pane's descriptor carries: one bearer
// per line, in capabilityFromDescriptor's order. Two lines and not a script,
// which is the point (nocx-1ebrs.8) — the writer has always finished writing a
// hundred-odd bytes before the reader arrives, so there is no window in which
// a slow startup or a busy machine can change the outcome.
//
// The pane's AGENT TOOL TOKEN is deliberately not a third line here: this arm
// has never delivered it (capabilityLiteral does not carry it either), and
// making it start arriving is a different decision with a different blast
// radius. capabilityFromDescriptor reads a third line and, absent one, leaves
// the variable empty — the state this tier is in today.
func capabilityLines(opts LaunchOptions) string {
	return opts.Capability + "\n" + opts.Recovery + "\n"
}

// localZshEnv is the environment the transient ZDOTDIR needs, and it is the
// same protocol the remote tier's outer script computes in shell: ZDOTDIR
// points at ours, the user's original value and whether it EXISTED travel
// beside it (an empty value and no value must restore differently, or the
// shell is left with an exported empty ZDOTDIR — which zsh reads as "look for
// startup files in the current directory"), and NOCX_CAP_FD names the
// descriptor the .zshenv phase reads before any user code runs.
func localZshEnv(dir string, opts LaunchOptions) []string {
	orig, wasSet := os.LookupEnv("ZDOTDIR")
	env := []string{
		"ZDOTDIR=" + dir,
		"NOCX_ZDOTDIR_ORIG=" + orig,
		fmt.Sprintf("%s=%d", CapabilityFDEnv, localScriptFD),
	}
	// The lifecycle descriptor's NUMBER travels in the SPAWN's environment
	// rather than in the rcfile's env block, and that is not a detail: the
	// .zshenv phase MOVES the channel to a high number before any user code
	// runs, and an env block that re-assigned the original at the .zshrc phase
	// would undo it — handing the script a number the shell no longer holds.
	// One writer of this fact per phase; writeLocalZDOTDIR keeps the other one
	// out.
	if opts.LifecycleFD > 0 {
		env = append(env, fmt.Sprintf("NOCX_LIFECYCLE_FD=%d", opts.LifecycleFD))
	}
	if wasSet {
		return append(env, "NOCX_ZDOTDIR_WAS_SET=1")
	}
	return append(env, "NOCX_ZDOTDIR_WAS_SET=0")
}

// writeLocalZDOTDIR materialises the transient directory a local zsh pane's
// ZDOTDIR points at: the three user-level phases zsh sources, in a native
// login's order, each our own file replaying the user's file of that phase and
// the .zshrc installing nocx's hooks after it (launcher_zsh.go owns the
// templates; this owns only where they land).
//
// IT HOLDS NO BEARER. The capability and the recovery fence are read from the
// descriptor by the .zshenv phase, so the only thing on disk is the public
// script text and the descriptor's NUMBER — the property that lets a
// filesystem delivery coexist with ADR-0024 decision 2 (capability_source.go).
//
// The name is `nocx-zsh.` plus SIX characters because zshCleanupTrap's guard
// names exactly that shape before it recurses: the guard is what stops an
// empty or unexpected variable taking a recursive delete somewhere else, and a
// directory its own guard would not match would be left behind by every
// session. 0700 here and 0600 on every file, from creation, with O_EXCL: no
// create-then-chmod window and no name another user could pre-empt.
func writeLocalZDOTDIR(opts LaunchOptions) (string, error) {
	// The lifecycle descriptor's number is NOT in this env block: the .zshenv
	// phase already moved it and put the new one in the environment
	// (localZshEnv), and a block that re-assigned the original would break the
	// channel at the .zshrc phase. Everything else in the block is unchanged.
	envOpts := opts
	envOpts.LifecycleFD = 0
	rc := zshRcfile(launcherEnvBlock(envOpts), localScript(zshScriptRaw, opts), "")
	dir, err := mkTransientZDOTDIR()
	if err != nil {
		return "", err
	}
	// Written in the order zsh reads them, and all-or-nothing: a directory
	// holding only some of the phases would shadow the user's other files with
	// nothing at all, which is the gap these files close. A partial write takes
	// the whole directory with it (nocx-2ka0).
	for _, file := range []struct{ name, body string }{
		{".zshenv", zshEnvFileLocal()},
		{".zprofile", zshProfileFile()},
		{".zshrc", rc},
	} {
		if werr := writeTransientFile(filepath.Join(dir, file.name), file.body); werr != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("shellintegration: local zsh %s: %w", file.name[1:], werr)
		}
	}
	return dir, nil
}

// mkTransientZDOTDIR creates the directory zshCleanupTrap's guard matches.
func mkTransientZDOTDIR() (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		b := make([]byte, 3)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("shellintegration: local zdotdir name: %w", err)
		}
		dir := filepath.Join(os.TempDir(), "nocx-zsh."+hex.EncodeToString(b))
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("shellintegration: local zdotdir: %w", err)
		}
	}
	return "", errors.New("shellintegration: local zdotdir: no free name")
}

// writeTransientFile creates one file of the transient ZDOTDIR. O_EXCL with
// mode 0600 from the start is the defence every file of the directory
// inherits: no pre-existing file is opened and no create-then-chmod window
// exists.
func writeTransientFile(path, body string) error {
	//nolint:gosec // path is the caller's 0700 temp directory plus a fixed name.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
