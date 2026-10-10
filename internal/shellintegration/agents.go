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
// text with its markers replaced — the wrapper block for exactly these names,
// and the staged-exec block for this delivery — then comment-stripped as every
// delivered script is. Both delivery paths go through it, so what a person's
// own pane sees and what a far host receives can differ only in the ways the
// rule above allows, and a marker nobody replaced cannot survive unnoticed.
func renderScript(raw string, names []string, delivery Delivery) string {
	surface := toolSurfaceLocal
	out := strings.ReplaceAll(raw, agentBlockMarker, agentWrappers(names))
	if delivery == DeliveryPublished {
		surface = toolSurfacePublished
		out = stripAgentLaunchHelpers(out)
		out = strings.ReplaceAll(out, agentUnorchestratedMarker, `command "$__agent" "$@"`+"\n        return $?")
		out = strings.ReplaceAll(out, agentLaunchSetupMarker, `__nocx_agent_launch_local=0
        __nocx_agent_launch_valid=1
        __nocx_agent_launch_ticket=`)
		out = strings.ReplaceAll(out, agentEnrolRefusalMarker, `command "$__agent" "$@"`+"\n        return $?")
		out = strings.ReplaceAll(out, agentLaunchCancelMarker, "")
	} else {
		out = strings.ReplaceAll(out, agentUnorchestratedMarker, "return 1")
		out = strings.ReplaceAll(out, agentLaunchSetupMarker, agentLaunchSetupLocal)
		out = strings.ReplaceAll(out, agentEnrolRefusalMarker, agentEnrolRefusalLocal)
		out = strings.ReplaceAll(out, agentLaunchCancelMarker, "__nocx_agent_launch_cancel")
	}
	out = strings.ReplaceAll(out, toolSurfaceMarker, surface)
	return stripShellComments(out)
}

func stripAgentLaunchHelpers(script string) string {
	start := strings.Index(script, agentLaunchHelpersStartMarker)
	end := strings.Index(script, agentLaunchHelpersEndMarker)
	if start < 0 || end < start {
		return script
	}
	end += len(agentLaunchHelpersEndMarker)
	if end < len(script) && script[end] == '\n' {
		end++
	}
	return script[:start] + script[end:]
}

// Delivery is WHICH of the two paths a script is rendered for, and the
// distinction is a RULE rather than a detail: the embedded script a pane on
// THIS machine is handed may carry nocx's own per-agent plumbing, while a
// generation file published to a host carries agent NAMES and nothing else
// (the owner's decision of 2026-10-05). One generator renders both, so the
// rule lives at the generator's own boundary instead of in the difference
// between two functions that would drift.
type Delivery int

const (
	// DeliveryLocal is a script handed to a shell on the machine nocx runs on:
	// the pane the person is sitting in front of.
	DeliveryLocal Delivery = iota
	// DeliveryPublished is a generation file carried to a host nobody here
	// controls. Nothing of this machine's configuration travels with it.
	DeliveryPublished
)

// toolSurfaceMarker is where the staged-exec block goes in the authored
// scripts. The two rendered forms differ by more than an argument: a host has
// no tool surface to point an agent at, because the surface's own path and
// configuration are this machine's.
const toolSurfaceMarker = "# @NOCX_TOOL_SURFACE@"

const (
	agentLaunchHelpersStartMarker = "# @NOCX_AGENT_LAUNCH_HELPERS_START@"
	agentLaunchHelpersEndMarker   = "# @NOCX_AGENT_LAUNCH_HELPERS_END@"
	agentUnorchestratedMarker     = "# @NOCX_AGENT_UNORCHESTRATED@"
	agentLaunchSetupMarker        = "# @NOCX_AGENT_LAUNCH_SETUP@"
	agentEnrolRefusalMarker       = "# @NOCX_AGENT_ENROL_REFUSAL@"
	agentLaunchCancelMarker       = "# @NOCX_AGENT_LAUNCH_CANCEL@"
)

const agentLaunchSetupLocal = `if ! __nocx_agent_launch_resolve "$__agent"; then
        builtin printf 'nocx: not started — %s\n' "${__nocx_agent_launch_reason:-local agent launch could not be resolved}" >&2
        __nocx_agent_launch_cancel
        __nocx_agent_launch_clear
        return 1
    fi
    if (( __nocx_agent_launch_local && ! __nocx_agent_launch_valid )); then
        builtin printf 'nocx: not started — %s\n' "${__nocx_agent_launch_reason:-local agent record has no usable launch configuration}" >&2
        __nocx_agent_launch_clear
        return 1
    fi`

const agentEnrolRefusalLocal = `if (( __nocx_agent_launch_local )); then
            __nocx_agent_launch_cancel
            if (( __nocx_agent_launch_valid )); then
                unset __nocx_agent_token 2>/dev/null || true
                if __nocx_agent_launch_exec 0 "$@"; then __rc=0; else __rc=$?; fi
                __nocx_agent_launch_clear
                return $__rc
            fi
            __nocx_agent_launch_clear
            return 1
        else
            command "$__agent" "$@"
            return $?
        fi`

// toolSurfaceLocal is what a LOCAL pane runs: nocx's tool surface reaches the
// agent through an argument, and this is the only delivery that carries one.
//
// Claude's --mcp-config option is variadic, so it goes LAST: placed before
// "$@" it would swallow a user's positional prompt as another config path. If a
// future Claude subcommand rejects trailing flags, update this argv proof and
// feed the prompt through stdin instead of moving the flag ahead of the user's
// arguments.
const toolSurfaceLocal = `if (( __nocx_agent_launch_local )); then
        if (( __staged )); then
            __nocx_agent_launch_exec 1 "$@"
        else
            __nocx_agent_launch_exec 0 "$@"
        fi
    else
        command "$__agent" "$@"
    fi`

// toolSurfacePublished is what a HOST runs instead: the agent, the person's own
// arguments, and NO per-agent argument at all.
//
// The tool surface is not lost for want of a flag but because its target does
// not exist there: `mcp.json` is written into this launch's directory by the
// staging step, and the argv that points an agent at it is per-agent
// configuration, which this delivery refuses to carry (agents.go). Where a
// per-agent argument belongs is the launch record's `args`, applied where nocx
// runs — nocx-xn63t.5.2's argv builder — and until that exists a host-side
// agent starts with no tool surface rather than with a flag invented for it.
const toolSurfacePublished = `command "$__agent" "$@"`
