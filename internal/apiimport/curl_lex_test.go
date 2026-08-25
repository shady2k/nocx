package apiimport

import (
	"strings"
	"testing"
)

// The lexer is the whole of "curl is parsed, never executed": every one of
// these inputs is a construct a shell would ACT on, and here each is a
// sequence of literal bytes.
func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"plain words", `curl https://a.example/x`, []string{"curl", "https://a.example/x"}},
		{"runs of space collapse", "curl   -X \t POST", []string{"curl", "-X", "POST"}},
		{"single quotes are literal", `curl -H 'A: b c'`, []string{"curl", "-H", "A: b c"}},
		{"no escapes inside single quotes", `curl -d 'a\nb'`, []string{"curl", "-d", `a\nb`}},
		{"double quotes group", `curl -H "A: b c"`, []string{"curl", "-H", "A: b c"}},
		{"double quotes honour \\\" and \\\\", `curl -d "a\"b\\c"`, []string{"curl", "-d", `a"b\c`}},
		{"double quotes keep other backslashes", `curl -d "a\nb"`, []string{"curl", "-d", `a\nb`}},
		{"backslash escapes outside quotes", `curl a\ b`, []string{"curl", "a b"}},
		{"backslash-newline continues the line", "curl \\\n  -X POST", []string{"curl", "-X", "POST"}},
		{"backslash-newline inside double quotes joins", "curl -d \"a\\\nb\"", []string{"curl", "-d", "ab"}},
		{"adjacent quoting concatenates", `curl -H 'A: '"b"c`, []string{"curl", "-H", "A: bc"}},
		{"empty quotes make an empty token", `curl -d ''`, []string{"curl", "-d", ""}},
		{"a bare newline separates", "curl\n-X\nPOST", []string{"curl", "-X", "POST"}},
		{"leading and trailing space", `  curl x  `, []string{"curl", "x"}},

		// The point of the whole file. A shell would run these.
		{"command substitution is text", `curl -d '$(touch /tmp/pwned)'`, []string{"curl", "-d", "$(touch /tmp/pwned)"}},
		{"unquoted command substitution is text", `curl -d $(touch /tmp/pwned)`, []string{"curl", "-d", "$(touch", "/tmp/pwned)"}},
		{"backticks are text", "curl -d '`touch /tmp/pwned`'", []string{"curl", "-d", "`touch /tmp/pwned`"}},
		{"unquoted backticks are text", "curl -d `id`", []string{"curl", "-d", "`id`"}},
		{"dollar in double quotes is text", `curl -d "$HOME/$(id)"`, []string{"curl", "-d", "$HOME/$(id)"}},
		{"escaped dollar in double quotes", `curl -d "\$HOME"`, []string{"curl", "-d", "$HOME"}},
		{"semicolon is text", `curl x;rm -rf /`, []string{"curl", "x;rm", "-rf", "/"}},
		{"pipe is text", `curl x|sh`, []string{"curl", "x|sh"}},
		{"redirection is text", `curl x >/etc/passwd`, []string{"curl", "x", ">/etc/passwd"}},
		{"tilde is text", `curl -d ~/.ssh/id_ed25519`, []string{"curl", "-d", "~/.ssh/id_ed25519"}},
		{"glob is text", `curl -d *`, []string{"curl", "-d", "*"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tokenize(tc.in)
			if err != nil {
				t.Fatalf("tokenize(%q) error: %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("tokenize(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("tokenize(%q)[%d] = %q, want %q (all: %#v)", tc.in, i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestTokenizeRejects(t *testing.T) {
	cases := []struct{ name, in string }{
		{"unterminated single quote", `curl -d 'abc`},
		{"unterminated double quote", `curl -d "abc`},
		{"trailing backslash", `curl -d abc\`},
		{"NUL byte", "curl -d a\x00b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tokenize(tc.in); err == nil {
				t.Fatalf("tokenize(%q) succeeded, want an error", tc.in)
			}
		})
	}
}

// The paired success case for every rejection above (AGENTS.md testing rule
// 3): on an ordinary line the lexer succeeds.
func TestTokenizeOrdinaryLineSucceeds(t *testing.T) {
	in := `curl -X POST 'https://api.example/users' -H 'Content-Type: application/json' -d '{"a":1}'`
	got, err := tokenize(in)
	if err != nil {
		t.Fatalf("tokenize error: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("got %d tokens %#v, want 8", len(got), got)
	}
	if got[len(got)-1] != `{"a":1}` {
		t.Fatalf("last token = %q", got[len(got)-1])
	}
}

func TestTokenizeRefusesOverlongLine(t *testing.T) {
	if _, err := tokenize("curl " + strings.Repeat("a", maxCurlLineBytes)); err == nil {
		t.Fatal("an over-long line was accepted")
	}
	if _, err := tokenize("curl " + strings.Repeat("a", 100)); err != nil {
		t.Fatalf("an ordinary line was refused: %v", err)
	}
}
