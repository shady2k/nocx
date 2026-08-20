package shellintegration

import "strings"

// The ShellAuto DISPATCHER that used to live here went with the remote
// command that carried it (ADR-0035). It ran under /bin/sh, read the login
// shell's own argv[0] out of "$0", and picked one of three tier payloads that
// travelled beside it as quoted argv words — which is only a shape a ~90 KiB
// command can have. The dispatch itself did not go: the installed launch
// carrier does it on the far side (launch.go), from the stage-1 pin first and
// $SHELL second, with the same tiers and the same fail-open for a login shell
// it cannot detect.

// singleLine collapses a script's statement-separator newlines so the
// payload travels as one physical line. csh/tcsh parse a `-c` command
// line-by-line and split a single-quoted token that contains a newline —
// measured on tcsh 6.24.16: "Unmatched ”'", then the quoted lines are
// executed as separate commands — so any payload that must survive a csh
// login shell has to be one physical line. The zsh and posix outer
// scripts use newlines only as statement separators (no multi-line
// strings, here-docs or comments), so joining with "; " is
// semantics-preserving; the const templates stay multi-line for
// readability and are joined at build time. Not for the dispatcher: its
// case statements need `in` immediately followed by the first pattern, and
// a join would insert "; " there — it is authored single-line instead.
func singleLine(script string) string {
	return strings.TrimSpace(strings.ReplaceAll(script, "\n", "; "))
}
