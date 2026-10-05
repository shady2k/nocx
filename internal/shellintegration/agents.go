package shellintegration

// The agent wrappers a connected shell offers, GENERATED from the agent record
// (nocx-t5e7d, the owner's shell-bundle decision of 2026-10-05).
//
// # What this replaced, and what was wrong with it
//
// The bundle used to spell the wrapper out by hand:
//
//	claude() { __nocx_agent_run claude "$@"; }
//
// one line, with a comment saying a second name would be a second line. So the
// set of agents nocx offered in a shell was a fact about the SCRIPT, and the
// places that decide which agents exist — the driver registry, the agent record
// — could not change it. A person who added an agent got no wrapper; an agent
// this build ships a driver for got one whether or not it was switched on.
//
// # What travels, and what does not
//
// The bundle is published to hosts nobody here controls. So what the host
// receives is a NAME and nothing else: no argument, no environment line, no
// part of a person's record. That is a security boundary rather than tidiness —
// an environment line is where a key lives, and arguments are where a local
// path or a private model name lives — and it is why the record is read HERE
// and never shipped.
//
// A consequence worth stating plainly: the wrapper is `name() { ... }`, so a
// record whose id is not a name a shell can define a function with gets no
// wrapper. A name is a command name in this context, and one a shell cannot
// define is not one a person could type either, so the agent is dropped rather
// than wrapped into a syntax error that would take the whole block with it.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/shady2k/nocx/internal/agentrecord"
)

// shippedAgentNames is this build's own agent set, from the package that owns
// it. It exists so exactly one file here names the record: the embedded scripts
// are rendered at package init, before any profile exists, and the build is the
// only thing that can answer there.
func shippedAgentNames() []string { return agentrecord.ShippedNames() }

// agentBlockMarker is the line the authored scripts carry where the wrappers
// go. It is a comment in the source ON PURPOSE: the substitution runs before
// stripShellComments, so the marker is prose in the file a person reads and is
// gone from the bytes a host receives.
const agentBlockMarker = "# @NOCX_AGENT_WRAPPERS@"

// shellCommandName is what a shell can define a function with, and it is
// STRICTER than the rule for a record's file name (storage.ValidDocumentName,
// which allows a dot): a document may be named anything a file may be named,
// while a wrapper has to be a name the shell will parse. Hyphens are allowed
// because bash and zsh both accept them in a function name and an agent called
// `prime-agent` is exactly the case that needs it; the POSIX tier has no agent
// wrapper at all, so no shell here is asked for more than it accepts.
var shellCommandName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// agentNames is the canonical form of a set of agent names: the ones a shell
// can wrap, sorted. Every consumer takes this form — the generated block, the
// bundle's own record of what it was generated from, and the digest that names
// its generation — so two bundles built from one set are the same bundle
// whatever order the set arrived in.
func agentNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if !shellCommandName.MatchString(n) || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// agentWrappers renders the block: one wrapper per name, and nothing else on
// the line. The awkward spelling at the end is the shell's — "$@" passes the
// person's own arguments through untouched, which is the whole of a wrapper's
// job: nocx decides what to WRAP, never what the person meant to run.
func agentWrappers(names []string) string {
	var b strings.Builder
	for _, n := range agentNames(names) {
		fmt.Fprintf(&b, "%s() { __nocx_agent_run %s \"$@\"; }\n", n, n)
	}
	return b.String()
}

// renderScript is the ONE place a script's bytes are produced: the authored
// text with its marker replaced by the block for exactly these names, then
// comment-stripped as every delivered script is. Both delivery paths go through
// it — the embedded scripts a local pane is handed and the generation files a
// host is published — so the shape a person's own pane sees and the shape a far
// host sees cannot drift.
func renderScript(raw string, names []string) string {
	return stripShellComments(strings.ReplaceAll(raw, agentBlockMarker, agentWrappers(names)))
}
