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

// writeLocalRcfileIn writes the rendered rcfile to a transient file whose
// name matches the template's self-delete guard (`*/nocx-bash.??????` —
// exactly six characters after the prefix, which is the mktemp shape the
// guard was written for; a longer random suffix would never be removed and
// every session would leave a file containing the capability in TMPDIR).
// The file is created mode 0600 from the start (no create-then-chmod
// window) with O_EXCL (no symlink pre-emption), so the capability it
// carries is never world-readable. The shell removes it once bash has read
// it; the caller removes it on spawn failure.
func writeLocalRcfileIn(rc, dir string) (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("shellintegration: local rcfile name: %w", err)
	}
	path := filepath.Join(dir, "nocx-bash."+hex.EncodeToString(b))
	//nolint:gosec // dir is a trusted private runtime directory or os.TempDir;
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

// writeLocalZDOTDIRIn writes the rendered .zshrc into a transient directory
// and returns the directory — the value ZDOTDIR is pointed at.
//
// A DIRECTORY rather than a file because zsh has no --rcfile: ZDOTDIR names a
// directory, which is why the transient directory is structural for this tier
// where the bash tier needs only a transient file. Its name matches the
// template's self-delete guard (`*/nocx-zsh.??????` — exactly six characters
// after the prefix, the mktemp shape the guard was written for), because the
// guard is what stops an empty or unexpected variable from taking a recursive
// delete somewhere else, and a directory the guard does not match would be left
// behind by every session with the capability inside it.
//
// The directory is 0700 and the file 0600, both from the start (no
// create-then-chmod window) and both refusing a name that already exists (no
// pre-emption by a symlink or a squatted name), so the capability the .zshrc
// carries is never readable by another user. The shell removes the whole
// directory at the top of the .zshrc, before any user code runs; the caller
// removes it on spawn failure.
func writeLocalZDOTDIRIn(rc, parent string) (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("shellintegration: local zdotdir name: %w", err)
	}
	dir := filepath.Join(parent, "nocx-zsh."+hex.EncodeToString(b))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("shellintegration: local zdotdir: %w", err)
	}
	path := filepath.Join(dir, ".zshrc")
	//nolint:gosec // parent is a trusted private runtime directory or
	// os.TempDir; the 0700 directory and O_EXCL 0600 file prevent pre-emption
	// and keep the capability private.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("shellintegration: local zshrc: %w", err)
	}
	if _, err := f.WriteString(rc); err != nil {
		_ = f.Close()
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("shellintegration: local zshrc write: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("shellintegration: local zshrc close: %w", err)
	}
	return dir, nil
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
	artifactDir := opts.ArtifactDir
	if artifactDir == "" {
		artifactDir = os.TempDir()
	}
	switch kind {
	case ShellBash:
		rc, err := LocalBashRcfile(opts)
		if err != nil {
			return LocalLaunch{}, err
		}
		path, err := writeLocalRcfileIn(rc, artifactDir)
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
		dir, err := writeLocalZDOTDIRIn(rc, artifactDir)
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
