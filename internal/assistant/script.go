package assistant

// The script a command NAMES (nocx-872jc.3).
//
// The approval window showed a proposed command verbatim and, since
// nocx-4h0m7.5, what its variables currently read as. What it never showed is
// the FILE. `bash deploy.sh` is eleven characters and the whole of its meaning
// is somewhere else; a person approving it was approving a NAME, and the act
// they were consenting to was written by whoever last touched `deploy.sh`.
//
// SO THIS IS A READING, AND IT IS THE SAME KIND OF THING THE EXPANSION IS.
// nocx-4h0m7.5 settled the shape once and this follows it rather than
// inventing a second one: a fact carried BESIDE the verbatim command and never
// instead of it (ADR-0045/nocx-y47mi SETTLED 1 — the verbatim string is what
// runs), with its own honesty about what it is not. The bytes here are the
// file's CURRENT contents; the command in `arguments` is what is sent, byte
// for byte; and the file can change between the question and the run. All
// three are said on the surface, once.
//
// IT IS NOT A FENCE, DELIBERATELY. The expansion has one — VerifyExpansions
// re-reads the values before submitting and refuses when one moved — and the
// obvious next thought is to hash the script and refuse a changed one. That is
// a different feature with a different failure mode (a build that rewrites a
// generated script between the question and the answer would be refused), it
// changes what approving MEANS, and this bead asks for none of it: nothing
// here touches argHash, callId, attempt or what the buttons do.
//
// WHICH FILE, FROM THE EXISTING PARSE. `internal/content`'s command parser
// already answers "what does this command line touch" and says so in its own
// header — "policy consumers receive this result instead of tokenizing the
// command again". `bash x.sh`, `sh ./x.sh`, `./x.sh` and `source x.sh` each
// arrive here as a resource with verb execute or source, and those two verbs
// are the whole selection rule. Tokenizing the command a second time to find a
// script would be a second derivation of a question this repo already answers,
// and the day the two disagreed the window would show a file the policy never
// saw.
//
// AND NOTHING IS SCANNED. `skill.Scan` is the skill package's vocabulary for a
// body somebody is adopting; running it over every shell script a command
// names would be a new advisory surface on every approval, which is a policy
// widening nobody asked for. The bytes are shown. What they mean is the
// person's reading, which is the entire point of showing them.

import (
	"context"
	"errors"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

// MaxScriptBytes bounds one reading. It is skill.MaxReadBytes, the same
// ceiling a person's own read of a skill file gets, because it is the same
// question — how much of a file a person is shown in a window they are
// deciding in — and two budgets for it would be two numbers to tune.
const MaxScriptBytes = 64 << 10

// scriptReadBudget bounds the WHOLE set of readings for one question, not each
// one. A question that appears late is a worse defect than one whose file is
// missing from it: the escalation is already holding a suspended run and a
// person waiting on a modal, so the reads get one shared deadline and whatever
// is past it is reported as unread, in words, rather than making the window
// wait. Two seconds because the remote arm acquires an SFTP lease over the
// session's own connection before it can read anything, and a local read that
// needs two seconds has something wrong with it that the window should say out
// loud rather than absorb.
const scriptReadBudget = 2 * time.Second

// ScriptRefusal names why a named file's bytes are not being shown. The set is
// closed and the wire declares it. Three of the four values are skill.File's,
// spelled the same because they are the same sentences about the same facts —
// FileReadout in the kit owns the wording for all of them and must not learn a
// second vocabulary. The fourth is this surface's own: skills.file answers a
// REQUEST and can fail it, while an approval question is a notification with
// nowhere to put an error, so "there was no file to read" has to be a fact
// inside the question.
type ScriptRefusal string

const (
	// ScriptRefusalNone means nothing was refused and Text holds the file.
	ScriptRefusalNone ScriptRefusal = ""
	// ScriptRefusalNotText means the bytes are not something that can be
	// shown as lines.
	ScriptRefusalNotText ScriptRefusal = "not-text"
	// ScriptRefusalTooLarge means the file is bigger than MaxScriptBytes.
	// The head of it is deliberately NOT shown: a person who read the first
	// 64 KiB of a script would believe they had read the script, and the
	// line that matters is as likely to be in the tail.
	ScriptRefusalTooLarge ScriptRefusal = "too-large"
	// ScriptRefusalUnreadable means there was no file to describe at all —
	// no provider for the session, a path nothing could resolve, a file that
	// is gone, permission refused, the shared deadline spent. Reason says
	// which, in the words the person reads.
	ScriptRefusalUnreadable ScriptRefusal = "unreadable"
)

// ScriptReading is one file a proposed command names, as the person reads it.
//
// Path is the path THE COMMAND WROTE, not a resolved one: it is what the
// person is looking at on the line above, and answering a question about
// `deploy.sh` with a row saying `/home/x/work/deploy.sh` would be a second
// name for the subject in the one place the two must be obviously the same
// thing. Where it resolved to is the provider's business.
type ScriptReading struct {
	Path string               `json:"path"`
	Verb content.ResourceVerb `json:"verb"`
	// Text is the file verbatim. Empty whenever Refusal is set, because half
	// a refused file is neither the file nor a refusal.
	Text     string        `json:"text"`
	Refusal  ScriptRefusal `json:"refusal"`
	MaxBytes int           `json:"maxBytes"`
	// Reason is the sentence for an unreadable file, and empty otherwise.
	// It travels rather than being composed on the surface for the reason
	// ExpansionUnavailableError.Reason does: the reasons differ in ways that
	// matter to a person, and a renderer that wrote one of its own would be
	// putting our guess in front of them instead of what happened.
	Reason string `json:"reason"`
}

// ScriptContent is what a ScriptSource read: the bytes, or which of the two
// facts about the FILE stopped them being shown. It is deliberately not
// filesystem.Content — the assistant depends on the abstraction, and the two
// booleans are the whole of what a reading needs (AD-8).
type ScriptContent struct {
	Text     string
	NotText  bool
	TooLarge bool
}

// ErrScriptUnreadable is the class of "there was no file to describe". It is
// never an error that fails a run: a question with no reading in it is a
// thinner question, never a refusal.
var ErrScriptUnreadable = errors.New("script: the named file could not be read")

// ScriptUnreadableError carries the SENTENCE a person reads for why a named
// file was not read. It wraps ErrScriptUnreadable so a caller can still test
// the class.
type ScriptUnreadableError struct {
	Reason string
}

func (e *ScriptUnreadableError) Error() string { return e.Reason }

func (e *ScriptUnreadableError) Unwrap() error { return ErrScriptUnreadable }

// ScriptSource reads ONE file a proposed command names, on the machine that
// command would run on. It is a READ and nothing else: an implementation that
// executed the path, or that resolved it by asking a shell to evaluate the
// command line, would be doing the thing the approval question exists to
// prevent.
//
// cwd is where the run was asked from, and it is what turns `deploy.sh` into a
// path. An implementation that cannot resolve a relative path — no cwd, or a
// cwd it does not trust — must refuse with a sentence rather than guess a
// directory: a window showing the WRONG deploy.sh is worse than one showing
// none, and it is the only failure here that a person cannot see.
//
// A nil source is the ordinary shape for every caller that is not the
// transport, and it is also the honest answer wherever no provider can reach
// the machine: nothing is read and the window says so.
type ScriptSource interface {
	ReadScript(ctx context.Context, sessionID, cwd, path string, maxBytes int) (ScriptContent, error)
}

// ScriptReadingsFor reads every file the parse says this command executes or
// sources, in the order the parse named them.
//
// EVERY file, which is the difference between this and a window that lies by
// omission: `bash a.sh && bash b.sh` names two, and showing the first would
// look complete while being half the act. A path named twice is read once —
// the second row would be the same bytes under the same name, which reads as
// two files.
//
// It never returns an error. Failing to read is not a reason to refuse a call,
// and every way of failing lands as a reading that says so.
func ScriptReadingsFor(ctx context.Context, source ScriptSource, sessionID, cwd string, inv content.Invocation) []ScriptReading {
	named := scriptsNamedBy(inv)
	if len(named) == 0 {
		return nil
	}
	// ONE deadline for the whole set, taken here rather than per file, so a
	// command naming six scripts on a slow link cannot hold the window open
	// six times as long as one naming one.
	ctx, cancel := context.WithTimeout(ctx, scriptReadBudget)
	defer cancel()
	readings := make([]ScriptReading, 0, len(named))
	for _, at := range named {
		readings = append(readings, readScript(ctx, source, sessionID, cwd, at))
	}
	return readings
}

// scriptsNamedBy is the ONE selection rule: the parse's own resources, whose
// verb is execute or source. No tokenizing, no second opinion about what a
// command line means.
func scriptsNamedBy(inv content.Invocation) []content.Resource {
	if !inv.Parsed {
		return nil
	}
	var named []content.Resource
	seen := make(map[content.Resource]struct{}, len(inv.Resources.Resources))
	for _, resource := range inv.Resources.Resources {
		if resource.Verb != content.ResourceExecute && resource.Verb != content.ResourceSource {
			continue
		}
		if resource.Path == "" {
			continue
		}
		if _, already := seen[resource]; already {
			continue
		}
		seen[resource] = struct{}{}
		named = append(named, resource)
	}
	return named
}

func readScript(ctx context.Context, source ScriptSource, sessionID, cwd string, at content.Resource) ScriptReading {
	reading := ScriptReading{Path: at.Path, Verb: at.Verb, MaxBytes: MaxScriptBytes}
	if source == nil {
		reading.Refusal = ScriptRefusalUnreadable
		reading.Reason = "nothing in this session can read files on the machine this command would run on, so the file was not read"
		return reading
	}
	read, err := source.ReadScript(ctx, sessionID, cwd, at.Path, MaxScriptBytes)
	if err != nil {
		reading.Refusal = ScriptRefusalUnreadable
		reading.Reason = scriptUnreadableReason(err)
		return reading
	}
	switch {
	case read.TooLarge:
		// Asked BEFORE the text check, for skill.File's reason: an over-long
		// file is over-long whatever its bytes decode to, and reporting a
		// 40 MiB blob as "not text" names the less useful of two true facts.
		reading.Refusal = ScriptRefusalTooLarge
	case read.NotText:
		reading.Refusal = ScriptRefusalNotText
	default:
		reading.Text = read.Text
	}
	return reading
}

func scriptUnreadableReason(err error) string {
	var named *ScriptUnreadableError
	if errors.As(err, &named) && named.Reason != "" {
		return named.Reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "reading this file took longer than the window would wait, so it was not read"
	}
	// Never empty: the surface's unreadable state has one sentence to draw
	// and an empty one would be an affordance saying nothing.
	return "this file could not be read: " + err.Error()
}
