package shellintegration

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// It is one of two local tiers. zsh has LocalZshRcfile beside it since
// nocx-wwz0; the posix tier is still remote-only, so a login shell that is
// neither bash nor zsh is started conventionally with ReasonUnsupportedShell
// (LocalEnhancedLaunch).
func LocalBashRcfile(opts LaunchOptions) (string, error) {
	if !opts.Enhanced || opts.SessionID == "" {
		return "", fmt.Errorf("shellintegration: local lifecycle bootstrap requires an enhanced session with a session id")
	}
	return bashRcfile(localChild, launcherEnvBlock(opts), bashScript,
		capabilityLiteral(bashUnsetExport, opts.Capability, opts.Recovery)), nil
}

// WriteLocalRcfile writes the rendered rcfile to a transient file whose
// name matches the template's self-delete guard (`*/nocx-bash.??????` —
// exactly six characters after the prefix, which is the mktemp shape the
// guard was written for; a longer random suffix would never be removed and
// every session would leave a file containing the capability in TMPDIR).
// The file is created mode 0600 from the start (no create-then-chmod
// window) with O_EXCL (no symlink pre-emption), so the capability it
// carries is never world-readable. The shell removes it once bash has read
// it; the caller removes it on spawn failure.
func WriteLocalRcfile(rc string) (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("shellintegration: local rcfile name: %w", err)
	}
	path := filepath.Join(os.TempDir(), "nocx-bash."+hex.EncodeToString(b))
	//nolint:gosec // path is os.TempDir() plus a random name minted here, and
	// O_EXCL with mode 0600 is precisely the defence: no pre-existing file is
	// opened and no other user can read the capability it carries.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("shellintegration: local rcfile: %w", err)
	}
	if _, err := f.WriteString(rc); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("shellintegration: local rcfile write: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("shellintegration: local rcfile close: %w", err)
	}
	return path, nil
}

// LocalShellKind classifies a login shell PATH into the local tier that starts
// it. It is the Go-side mirror of the remote dispatcher's case arms
// (autoDispatcherScript in launcher_auto.go), which is the codebase's existing
// answer to "given a shell's name, which tier is it": basename, leading `-`
// stripped (login(1) hands a login shell an argv[0] of "-zsh"), then an exact
// name match. Kept as a mirror rather than a second vocabulary — the dispatcher
// runs on the far host, where only a shell can ask the question, and this runs
// here, where only Go can; they must not disagree about what "zsh" is.
//
// ShellUnknown is the honest answer for fish, csh, tcsh, dash and anything
// else: the POSIX tier exists remotely but has no local launch semantics yet
// (nocx-k28e), so locally ShellUnknown means "start it, integrate nothing, and
// say so" rather than "substitute bash", which is the defect nocx-wwz0 is.
func LocalShellKind(path string) ShellKind {
	name := strings.TrimPrefix(filepath.Base(path), "-")
	switch name {
	case "bash":
		return ShellBash
	case "zsh":
		return ShellZsh
	default:
		return ShellUnknown
	}
}

// LocalZshRcfile renders the .zshrc for a LOCAL enhanced session — the same
// template and the same renderer the REMOTE zsh tier uses (zshRcfile), so the
// declared equivalence set documented on zshRcfileTemplate is one set and not
// two. What differs between the tiers is only how the file reaches a directory
// zsh will read: the remote tier can send a string and nothing else, so it
// ships a POSIX script that mktemp's the directory on the far host
// (zshOuterScript); locally the directory is made here, in Go, and that outer
// shell would be not merely unnecessary but a second thing that can fail.
//
// The capability and the recovery fence are substituted into the rcfile TEXT,
// never the environment, exactly as the bash tier (ADR-0024 decision 2), and
// the script is EMBEDDED rather than sourced from the installed generation for
// the same reason LocalBashRcfile embeds it: this session's authenticators must
// win over an installer-era install that the user's own ~/.zshrc may load.
func LocalZshRcfile(opts LaunchOptions) (string, error) {
	if !opts.Enhanced || opts.SessionID == "" {
		return "", fmt.Errorf("shellintegration: local lifecycle bootstrap requires an enhanced session with a session id")
	}
	return zshRcfile(launcherEnvBlock(opts), zshScript,
		capabilityLiteral(zshUnsetExport, opts.Capability, opts.Recovery)), nil
}

// WriteLocalZDOTDIR writes the rendered .zshrc into a transient directory,
// together with the two login-phase files that must sit beside it, and returns
// the directory — the value ZDOTDIR is pointed at.
//
// A DIRECTORY rather than a file because zsh has no --rcfile: ZDOTDIR names a
// directory, which is why the transient directory is structural for this tier
// where the bash tier needs only a transient file.
//
// And because it names a directory, it shadows the user's ~/.zshenv,
// ~/.zprofile and ~/.zlogin as well as their ~/.zshrc — zsh looks for every
// phase's file HERE. A directory holding only .zshrc therefore does not merely
// fail to replay those phases, it deletes them: zsh finds no file and runs
// nothing. So all three of ours go in, each replaying the user's file of the
// same phase from their own location, which is what makes the sequence a
// native login's. (~/.zlogin needs no file of ours: the .zshrc phase restores
// ZDOTDIR before the user's rc, and zsh resolves $ZDOTDIR again at every
// phase, so by then it is already found in its native place.)
//
// The remote tier has written the same three since nocx-m8jwn, from the same
// two renderers (zshEnvFile, zshProfileFile); this one wrote one of them, so
// on the ordinary macOS layout — Homebrew's documented install puts its
// shellenv eval in ~/.zprofile — a local tab reached the user's own ~/.zshrc
// with none of the PATH their login shell would have had, and a tool invoked
// there was `command not found` (nocx-2ka0).
//
// The directory's name matches the template's self-delete guard (`*/nocx-zsh.??????` — exactly six characters
// after the prefix, the mktemp shape the guard was written for), because the
// guard is what stops an empty or unexpected variable from taking a recursive
// delete somewhere else, and a directory the guard does not match would be left
// behind by every session with the capability inside it.
//
// The directory is 0700 and every file in it 0600, all from the start (no
// create-then-chmod window) and all refusing a name that already exists (no
// pre-emption by a symlink or a squatted name), so the capability the .zshrc
// carries is never readable by another user. The shell removes the whole
// directory at the top of the .zshrc, before any user code runs; the caller
// removes it on spawn failure.
func WriteLocalZDOTDIR(rc string) (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("shellintegration: local zdotdir name: %w", err)
	}
	dir := filepath.Join(os.TempDir(), "nocx-zsh."+hex.EncodeToString(b))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("shellintegration: local zdotdir: %w", err)
	}
	// Written in the order zsh reads them, and all-or-nothing: a directory
	// holding only some of the phases would shadow the user's other files with
	// nothing at all, which is the very gap these files close. A partial write
	// therefore takes the whole directory with it and the caller falls open to
	// a conventional session, exactly as the remote tier's outer script does.
	for _, file := range []struct{ name, body string }{
		{".zshenv", zshEnvFile()},
		{".zprofile", zshProfileFile()},
		{".zshrc", rc},
	} {
		if err := writeTransientFile(filepath.Join(dir, file.name), file.body); err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("shellintegration: local %s: %w", file.name[1:], err)
		}
	}
	return dir, nil
}

// writeTransientFile creates one file of the transient ZDOTDIR. O_EXCL with
// mode 0600 from the start is the defence the .zshrc needs and the other two
// inherit: no pre-existing file is opened, no create-then-chmod window, and no
// other user can read the capability the .zshrc carries.
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

// localZshEnv builds the three variables the transient ZDOTDIR bootstrap needs.
// It is the local counterpart of the four lines zshOuterScript runs on the far
// host — the same protocol, computed in Go because there is no outer shell here
// to compute it in.
//
// NOCX_ZDOTDIR_WAS_SET is what distinguishes "the user had no ZDOTDIR" from
// "the user had ZDOTDIR set to the empty string", which the template restores
// differently (unset versus export ZDOTDIR=""). One variable cannot carry that
// difference — an empty NOCX_ZDOTDIR_ORIG is both cases — and getting it wrong
// leaves the shell with an exported empty ZDOTDIR, which zsh reads as "look for
// startup files in the current directory".
//
// The original is read from THIS process's environment, which is precisely the
// value the child would have inherited had nocx not intervened: the child's
// environment is os.Environ() plus these.
func localZshEnv(dir, orig string, wasSet bool) []string {
	env := []string{"ZDOTDIR=" + dir, "NOCX_ZDOTDIR_ORIG=" + orig}
	if wasSet {
		return append(env, "NOCX_ZDOTDIR_WAS_SET=1")
	}
	return append(env, "NOCX_ZDOTDIR_WAS_SET=0")
}

// LocalLaunch is how a local enhanced session is started: the shell binary, its
// argv, the extra environment it needs, and the removal of whatever transient
// artefact the tier wrote. Cleanup is the caller's to run when the spawn FAILS
// — on success the shell removes the artefact itself, before any user code
// runs, which is what keeps the capability off the disk for longer than one
// startup.
type LocalLaunch struct {
	Command string
	Args    []string
	Env     []string
	Cleanup func()
}

// LocalEnhancedLaunch builds the start shape for a local enhanced session on
// the user's own login shell. The caller has already classified the shell with
// LocalShellKind and must not call this for ShellUnknown: a shell with no local
// tier is started conventionally and reports ReasonUnsupportedShell, and
// building a lifecycle channel for it would mint a capability nothing can use.
//
// Both tiers run the user's LOGIN shell BY PATH, not a `bash` found on PATH.
// That is nocx-wwz0 on the bash side too: the shell a terminal opens is the one
// the account database names, and on a machine with several bashes (a homebrew
// one, a nix one, Apple's 3.2.57) "whichever PATH finds" is not that shell.
//
// The argv differ because the shells differ, and each difference is the shell's
// own semantics rather than a preference. bash reads one file named by
// --rcfile, so it is started `--rcfile <path> -i` and is deliberately NOT a
// login shell — unchanged from before this bead, because a bash user must get
// exactly what they got. zsh has no --rcfile, so it is started `-l -i` with
// ZDOTDIR pointing at the transient directory: `-l` because the login startup
// files (/etc/zprofile, and macOS's path_helper inside it) are where a
// GUI-launched app gets a usable PATH at all, and `-i` explicitly because zsh
// reads $ZDOTDIR/.zshrc ONLY when interactive — inferring it from "stdin is a
// tty" would, on the one launch where it is not, skip our rcfile entirely and
// leave the transient directory behind with the capability in it.
func LocalEnhancedLaunch(shellPath string, kind ShellKind, opts LaunchOptions) (LocalLaunch, error) {
	switch kind {
	case ShellBash:
		rc, err := LocalBashRcfile(opts)
		if err != nil {
			return LocalLaunch{}, err
		}
		path, err := WriteLocalRcfile(rc)
		if err != nil {
			return LocalLaunch{}, err
		}
		return LocalLaunch{
			Command: shellPath,
			Args:    []string{"--rcfile", path, "-i"},
			Cleanup: func() { _ = os.Remove(path) },
		}, nil
	case ShellZsh:
		rc, err := LocalZshRcfile(opts)
		if err != nil {
			return LocalLaunch{}, err
		}
		dir, err := WriteLocalZDOTDIR(rc)
		if err != nil {
			return LocalLaunch{}, err
		}
		orig, wasSet := os.LookupEnv("ZDOTDIR")
		return LocalLaunch{
			Command: shellPath,
			Args:    []string{"-l", "-i"},
			Env:     localZshEnv(dir, orig, wasSet),
			Cleanup: func() { _ = os.RemoveAll(dir) },
		}, nil
	default:
		// Never a best-effort guess, and never a substituted bash: a shell
		// with no local tier is the caller's to start conventionally.
		return LocalLaunch{}, fmt.Errorf("shellintegration: %q has no local enhanced tier", shellPath)
	}
}
