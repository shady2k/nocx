package shellintegration

import (
	"fmt"
	"io"
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

// LocalLaunch is how a local enhanced session is started: the shell binary, its
// argv, the extra environment it needs, and the removal of whatever transient
// artefact the tier wrote. Cleanup releases launch resources after the child
// starts; legacy file-backed tiers also use it to remove an artefact when the
// child fails to start. Abort cancels pending in-memory delivery on failure.
type LocalLaunch struct {
	Command    string
	Args       []string
	Env        []string
	ExtraFiles []*os.File
	Bootstrap  []byte
	Cleanup    func()
	Abort      func()
}

// LocalEnhancedLaunchInMemory builds the helper's launch shape. Unlike the
// coordinator's local launch, a helper-hosted shell cannot rely on a script
// installed under the host's home: the helper is the integration and the
// installed Level-0 substrate may be absent. The existing renderers still own
// every marker and hook; only their delivery changes to an inherited pipe.
// Bash and POSIX shells consume fd 3 during startup. Zsh has no --rcfile and
// cannot use a descriptor as ZDOTDIR, so it starts its normal login shell and
// sources the same in-memory script through fd 3 after the coordinator writes
// the bootstrap command. No script path is created on the host.
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
		return pipeLaunch(shellPath, []string{"--rcfile", "/dev/fd/3", "-i"}, nil, rc, nil)
	case ShellZsh:
		// THE CAPABILITY IS IN THE SCRIPT TEXT, exactly as it is in the bash
		// arm above — which reaches it through LocalBashRcfile's own @CAP@
		// substitution, and which is why this arm's absence was invisible.
		//
		// Without it the shell learned its lane, its domain, its epoch and its
		// descriptor and had nothing to authenticate with, so it wrote a hello
		// no kernel could accept: an integrated-looking zsh that went
		// conventional ten seconds later with `handshake-timeout`. Nothing
		// noticed while the only helper-hosted sessions were remote ones
		// nobody ran zsh on; nocx-ie23r.3 made it every local zsh pane on this
		// machine, which is what found it.
		//
		// Between the environment block and the script, because nocx.zsh's
		// __nocx_lc_init reads __nocx_cap as it is sourced — a literal after
		// it would be an assignment nothing reads.
		script := launcherEnvBlock(opts) + "\n" +
			capabilityLiteral(zshUnsetExport, opts.Capability, opts.Recovery) + "\n" +
			zshScript + "\n"
		return pipeLaunch(shellPath, []string{"-l", "-i"}, nil, script, []byte(". /dev/fd/3\n"))
	case ShellUnknown:
		// The same substitution, in the POSIX spelling. `export -n` is not in
		// POSIX and dash refuses it, which is what capabilityLiteral's own
		// `2>/dev/null` covers: the assignment stands either way and the worst
		// case is a variable that stays exported on a shell that cannot
		// un-export one.
		script := launcherEnvBlock(opts) + "\n" +
			capabilityLiteral(bashUnsetExport, opts.Capability, opts.Recovery) + "\n" +
			posixScript + "\n"
		return pipeLaunch(shellPath, []string{"-i"}, []string{"ENV=/dev/fd/3"}, script, nil)
	default:
		return LocalLaunch{}, fmt.Errorf("shellintegration: %q has no in-memory enhanced tier", shellPath)
	}
}

func pipeLaunch(command string, args, env []string, script string, bootstrap []byte) (LocalLaunch, error) {
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
		Bootstrap:  bootstrap,
		Cleanup:    cleanup,
		Abort:      abort,
	}, nil
}
