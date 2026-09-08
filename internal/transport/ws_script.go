package transport

// The script source (nocx-872jc.3): when the assistant is about to ask a
// person to approve `bash deploy.sh`, this is what answers "and what IS in
// deploy.sh, right now, on the machine that would run it".
//
// IT GOES THROUGH THE FILESYSTEM PROVIDER, WHICH IS THE OWNER. internal/
// filesystem already answers "read this path on the machine this session is
// on" for both arms — local through os, remote over SFTP on the session's own
// connection — and files.read is served through it. A second reading path
// here (os.ReadFile for local, a `cat` over the shell for remote) would be the
// second implementation of one behaviour, and the remote half of it would be
// executing a command to build a question about executing a command.
//
// A PROVIDER PER READING, and it is deliberate. files.open holds one across a
// binding's lifetime because a panel makes many calls; an approval question
// makes one or two and then the question is over, so there is nothing to hold
// it for and holding it would be a lease outliving its reason. The remote arm
// therefore pays an FSConn acquisition per approval — bounded by the shared
// deadline the assistant applies (assistant.scriptReadBudget), and reported as
// unread rather than as a wait if it does not arrive.
//
// A RELATIVE PATH NEEDS A DIRECTORY, AND A GUESSED ONE IS A DIFFERENT FILE.
// `bash deploy.sh` names a path only in a directory, so this resolves it
// against the run's OWN cwd — the one the question carried — and refuses in
// words when there is none. The local provider would happily fall back to the
// home directory (Provider.Root does exactly that, and correctly, for a file
// panel that has to show something); a window that showed ~/deploy.sh while
// the command runs /srv/app/deploy.sh would be the one failure on this surface
// a person cannot see, because both are real files with the right name.
//
// WHAT IT NEVER DOES. It executes nothing: not the path, not the command, not
// a shell asked to resolve either. It follows nothing the script itself
// references — one hop, the file the command named. And it scans nothing:
// the bytes go to the person, and reading them is their job.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/session"
)

// ReadScript implements assistant.ScriptSource over the same filesystem
// provider files.read is served through.
//
// Every refusal is a *assistant.ScriptUnreadableError carrying the sentence a
// person reads, because the approval question is a notification with nowhere
// to put an error: "there was no file to read" has to arrive as a fact inside
// the question or not at all, and a bare Go error rendered on a modal would
// be our internals in front of somebody deciding whether to run a command.
func (s *WSServer) ReadScript(ctx context.Context, sessionID, cwd, scriptPath string, maxBytes int) (assistant.ScriptContent, error) {
	absolute, err := absoluteScriptPath(cwd, scriptPath)
	if err != nil {
		return assistant.ScriptContent{}, err
	}
	if s.filesProviderFor == nil {
		return assistant.ScriptContent{}, &assistant.ScriptUnreadableError{
			Reason: "this build has no way to read files on the machine the command would run on, so the file was not read",
		}
	}
	if s.registry == nil {
		return assistant.ScriptContent{}, &assistant.ScriptUnreadableError{
			Reason: "this build has no way to read files on the machine the command would run on, so the file was not read",
		}
	}
	sess, err := s.registry.Get(session.ID(sessionID))
	if err != nil {
		return assistant.ScriptContent{}, &assistant.ScriptUnreadableError{
			Reason: "the session this command would run in is no longer open, so the file was not read",
		}
	}
	// No root override: the path is already absolute, and a root would only
	// narrow what this can reach without changing what it resolves to.
	provider, err := s.filesProviderFor(sess, "")
	if err != nil {
		return assistant.ScriptContent{}, &assistant.ScriptUnreadableError{
			Reason: "the machine this command would run on could not be reached to read the file",
		}
	}
	defer func() { _ = provider.Close() }()
	read, err := provider.Read(ctx, absolute, int64(maxBytes)+1)
	if err != nil {
		return assistant.ScriptContent{}, &assistant.ScriptUnreadableError{Reason: scriptReadRefusal(absolute, err)}
	}
	// One byte past the budget is how "over the budget" is learned, the way
	// skill.File learns it: Truncated says the provider stopped early, and
	// asking for maxBytes+1 makes a file of exactly maxBytes come back whole
	// instead of being reported as too large.
	if read.Truncated || len(read.Text) > maxBytes {
		return assistant.ScriptContent{TooLarge: true}, nil
	}
	if read.Binary {
		return assistant.ScriptContent{NotText: true}, nil
	}
	return assistant.ScriptContent{Text: read.Text}, nil
}

// absoluteScriptPath turns the path the COMMAND wrote into one a provider can
// open. Both providers require an absolute, cleaned path and own their own
// path syntax; slash joining is correct for both arms nocx targets (a POSIX
// local machine and SFTP, which specifies POSIX paths regardless of host OS).
func absoluteScriptPath(cwd, scriptPath string) (string, error) {
	if strings.TrimSpace(scriptPath) == "" {
		return "", &assistant.ScriptUnreadableError{Reason: "the command named no file to read"}
	}
	if path.IsAbs(scriptPath) {
		return path.Clean(scriptPath), nil
	}
	if !path.IsAbs(cwd) {
		return "", &assistant.ScriptUnreadableError{
			Reason: fmt.Sprintf(
				"%s is written relative to whatever directory the command runs in, and nocx does not know which that is, so the file was not read — a file of that name in the wrong directory is a different file",
				scriptPath,
			),
		}
	}
	return path.Join(cwd, scriptPath), nil
}

// scriptReadRefusal is the sentence for a read that came back empty-handed.
// The typed markers are the filesystem package's own, so the four cases it
// distinguishes are distinguished here rather than collapsed into one
// "could not read" that tells a person nothing about what to check.
func scriptReadRefusal(at string, err error) string {
	var notFound *filesystem.ErrNotFound
	var permission *filesystem.ErrPermission
	var notRegular *filesystem.ErrNotRegular
	var invalid *filesystem.ErrInvalidPath
	switch {
	case errors.As(err, &notFound):
		// Worth saying plainly: the command names a file that is not there,
		// which is a fact about the proposal and not only about the reading.
		return "there is no file at " + at + " on that machine, so nothing was read"
	case errors.As(err, &permission):
		return "nocx is not allowed to read " + at + " on that machine, so nothing was read"
	case errors.As(err, &notRegular):
		return at + " is not a regular file, so there is nothing to read there"
	case errors.As(err, &invalid):
		return at + " is not a path this machine's filesystem can be asked about, so nothing was read"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "reading " + at + " took longer than the window would wait, so it was not read"
	default:
		return at + " could not be read: " + err.Error()
	}
}
