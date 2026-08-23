package shellintegration

import "strings"

// Where a tier's two bearer values come from.
//
// One slot in each rcfile template (@CAPSRC@), one owner of what goes in it,
// and exactly two forms — because there are exactly two transports by which a
// bearer can reach a shell without appearing in argv, the environment, a named
// file, the history or a log (design D4):
//
//   - capabilityFromDescriptor: the shell reads an INHERITED, ALREADY UNLINKED
//     descriptor once and closes it. This is the remote path. The rcfile is
//     an installed generation file, published long before the session, so it
//     cannot carry a per-session value at all — which is the property that
//     makes it capability-free by construction rather than by care.
//
//   - capabilityLiteral: the value is written into the rcfile TEXT, and the
//     rcfile itself is handed over through a descriptor. This is the local
//     child path (sudo/su), where the bootstrap IS the rcfile and is read from
//     /dev/fd/N: the text never becomes a filesystem object, so a literal in
//     it is not an exposure.
//
// The form the CARRIER retired is a third one that used to share the literal's
// spelling: the same substitution into an rcfile that travelled inside the
// remote COMMAND, where both bearers reached the far host's process arguments
// and every recorder of the exec request. That is the defect this epic exists
// to remove, and it is gone — the command that carried it was deleted with
// its last caller (ADR-0035). What is left is the two forms above, and each
// of them has a transport that never becomes a filesystem name or an argv
// word.
//
// Both forms end by dropping the export attribute. A user rc running under
// `set -a` would auto-export the assignment and publish the capability in
// /proc/<pid>/environ; the hook drops it again at source time, and the two
// places doing it are not a duplication — this one covers the window between
// the assignment and the hook.

// capabilityFromDescriptor renders the read that the remote tiers use.
//
// Placement is the contract, not the text: the block is substituted AFTER the
// user's own startup file has run and returned (design §5.2 point 7), so
// nothing the user's rc executes can see either value, and the descriptor is
// closed in the same breath so no descendant of the shell inherits a reader.
//
// A REGULAR FILE is what makes two reads safe. The descriptor names an
// unlinked regular file, so a shell that reads a block and seeks back — bash
// does exactly that on a seekable descriptor — leaves the offset where the
// second read expects it. On a pipe the same builtin would swallow the fence
// with the capability, which is the short-read race launcher_bash.go already
// bought out once for the rcfile itself.
//
// unsetVar is the shell's spelling of "remove the export attribute": bash has
// `export -n`, zsh does not and uses `typeset +x` (`typeset -n` is a nameref
// and must never be used here).
func capabilityFromDescriptor(unsetVar string) string {
	var b strings.Builder
	b.WriteString("__nocx_cap=''\n")
	b.WriteString("__nocx_lc_recovery=''\n")
	b.WriteString("case \"${" + CapabilityFDEnv + ":-}\" in\n")
	b.WriteString("    ''|*[!0-9]*) ;;\n")
	b.WriteString("    *)\n")
	// One read pass, both values, then the descriptor is closed. The
	// redirection is inside a group whose stderr is discarded and which is
	// followed by an always-true fallback, for the same reason the
	// bootstrap-progress block is: a descriptor that is not open makes the
	// REDIRECTION fail, which under an inherited errexit would end the
	// session and which otherwise prints on the user's terminal.
	b.WriteString("        { IFS= read -r __nocx_cap <&\"${" + CapabilityFDEnv + "}\" && IFS= read -r __nocx_lc_recovery <&\"${" + CapabilityFDEnv + "}\"; } 2>/dev/null || :\n")
	// eval, not `exec {fd}<&-`: bash 3.2 has no {var} redirection form,
	// and this is the same idiom the bootstrap descriptor is closed with.
	b.WriteString("        { eval \"exec ${" + CapabilityFDEnv + "}<&-\"; } 2>/dev/null || :\n")
	b.WriteString("        ;;\n")
	b.WriteString("esac\n")
	// The NUMBER is not a secret, but it is also of no use to anything
	// downstream, and leaving it exported would advertise a descriptor
	// that is already closed.
	b.WriteString("unset " + CapabilityFDEnv + "\n")
	b.WriteString(unsetVar + " __nocx_cap 2>/dev/null\n")
	b.WriteString(unsetVar + " __nocx_lc_recovery 2>/dev/null\n")
	return b.String()
}

// capabilityLiteral renders the assignment form: the values are in the
// rcfile's own text, which is delivered through a descriptor rather than
// through a filesystem name.
func capabilityLiteral(unsetVar, capability, recovery string) string {
	var b strings.Builder
	b.WriteString("__nocx_cap='" + capability + "'\n")
	b.WriteString(unsetVar + " __nocx_cap 2>/dev/null\n")
	b.WriteString("__nocx_lc_recovery='" + recovery + "'\n")
	b.WriteString(unsetVar + " __nocx_lc_recovery 2>/dev/null\n")
	return b.String()
}

// bashUnsetExport and zshUnsetExport are the two spellings.
const (
	bashUnsetExport = "export -n"
	zshUnsetExport  = "typeset +x"
)
