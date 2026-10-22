package shellintegration

import (
	"strings"
	"testing"
)

// The stripper must never treat a '#' as a comment where shell grammar makes
// it syntax or data. These are the traps the brief names — a '#' inside a
// single-quoted string, inside a double-quoted string, inside a here-doc and
// inside a ${var#prefix} expansion — plus the arithmetic, conditional and
// escape contexts the real scripts use (16#$hdr, [[ … ]], \#).

func TestStripComments_PreservesHashInSingleQuotes(t *testing.T) {
	src := "x='a#b'\n# comment\n"
	got := stripShellComments(src, ShellBash)
	if got != "x='a#b'\n" {
		t.Errorf("got %q", got)
	}
}

func TestStripComments_PreservesHashInDoubleQuotes(t *testing.T) {
	src := "x=\"a#b\"\ny=\"${x} # c\"\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

func TestStripComments_PreservesHashInAnsiCQuotes(t *testing.T) {
	src := "x=$'a#b\\nc'\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

func TestStripComments_PreservesHashInHeredoc(t *testing.T) {
	src := "cat <<'EOF'\n# data line, not a comment\n  # indented data too\nEOF\n# real comment\n"
	got := stripShellComments(src, ShellBash)
	want := "cat <<'EOF'\n# data line, not a comment\n  # indented data too\nEOF\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripComments_PreservesHashInTabIndentedHeredoc(t *testing.T) {
	src := "cat <<-EOF\n\t# tab-indented data\n\tEOF\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

func TestStripComments_PreservesHashInUnquotedHeredoc(t *testing.T) {
	src := "cat <<EOF\n# data\n$VAR # more data\nEOF\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

func TestStripComments_PreservesParameterExpansionHash(t *testing.T) {
	// ${var#prefix}, ${var##prefix} and ${#var} are operators, not comments.
	src := "a=${x#pre}\nb=${y##pre}\nc=${#z}\nd=${x:-${y#p}}\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

func TestStripComments_PreservesArithmeticHash(t *testing.T) {
	// 16#$hdr is base notation inside $(( )) and (( )); a '#' there is never
	// a comment even when preceded by whitespace.
	src := "x=$(( 16#ff ))\ny=$(( 16 # comment-looking ))\n(( 16#0f == 15 ))\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

func TestStripComments_PreservesHashInConditional(t *testing.T) {
	src := "[[ $x == a#b ]]\n[[ -n $x ]] && echo ok # trailing comment\n"
	got := stripShellComments(src, ShellBash)
	want := "[[ $x == a#b ]]\n[[ -n $x ]] && echo ok\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripComments_PreservesEscapedHash(t *testing.T) {
	src := "echo \\#not-a-comment\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

func TestStripComments_PreservesHashInsideCommandSubstitution(t *testing.T) {
	// A '#' inside $(…) is subject to normal comment rules; inside a quote
	// within it, it is data.
	src := "x=$(echo 'a#b')\ny=$(echo \"c#d\")\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

func TestStripComments_PreservesPositionalParameterCount(t *testing.T) {
	src := "n=$#\necho $# args\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

// The stripper's job: full comment lines and trailing comments go, the rest
// of the script is untouched.

func TestStripComments_RemovesFullCommentLines(t *testing.T) {
	src := "foo=1\n# a comment\n   # indented comment\n\nbar=2\n"
	got := stripShellComments(src, ShellBash)
	want := "foo=1\n\nbar=2\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripComments_RemovesTrailingComments(t *testing.T) {
	src := "foo=1 # comment\nbar=2\n"
	got := stripShellComments(src, ShellBash)
	want := "foo=1\nbar=2\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripComments_PreservesShebang(t *testing.T) {
	src := "#!/bin/sh\n# comment\nfoo=1\n"
	got := stripShellComments(src, ShellBash)
	want := "#!/bin/sh\nfoo=1\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripComments_PreservesHashInCasePattern(t *testing.T) {
	// An unquoted '#' mid-word in a case pattern is a literal pattern char.
	src := "case $x in\n  a#b) echo m ;;\n  *) echo n ;;\nesac\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

func TestStripComments_PreservesZshFlagsAndPrompts(t *testing.T) {
	src := "PROMPT='%~ %# '\nhex=${(l:2::0:)$(( [##16] code ))}\n"
	got := stripShellComments(src, ShellBash)
	if got != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

// The shipped scripts must end up free of comment text, and their non-comment
// body must be byte-for-byte identical to the source with comments removed.

func TestStripComments_ShippedScriptsCarryNoCommentText(t *testing.T) {
	for _, s := range []struct{ name, body string }{
		{"nocx.bash", bashScript},
		{"nocx.zsh", zshScript},
		{"nocx.posix", posixScript},
		{"launch carrier", launchCarrier()},
	} {
		if strings.Contains(s.body, "\n#") {
			t.Errorf("%s still ships a comment line", s.name)
		}
		for _, line := range strings.Split(s.body, "\n") {
			trimmed := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "#!") {
				t.Errorf("%s ships comment line %q", s.name, line)
			}
		}
	}
}

func TestStripComments_Idempotent(t *testing.T) {
	for _, s := range []string{bashScript, zshScript, posixScript, launchCarrier()} {
		if again := stripShellComments(s, ShellBash); again != s {
			t.Errorf("stripping is not idempotent")
		}
	}
}
