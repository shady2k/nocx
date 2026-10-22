package shellintegration

import "strings"

// stripShellComments removes comment text from a shell script so the shipped
// bytes carry none of the prose the repo keeps for humans (nocx-z9s9.17).
// The remote host is sent the bootstrap payload on every launch and never
// reads the comments — 62% of nocx.bash was measured to be prose — so the
// comments stay in the repo (they carry the reasoning AGENTS.md exists to
// preserve) and only the shipped bytes shrink.
//
// The stripper is conservative on purpose: it removes a comment only where
// shell grammar guarantees one, and leaves everything else untouched, so the
// worst case is a residual comment (a few bytes), never a corrupted script.
// The rules:
//
//   - a line whose first non-blank character is '#' is a comment line, dropped
//     whole (through its newline), except a shebang line ("#!" at byte 0),
//     which is preserved — the launch carrier is exec'd directly;
//   - a '#' preceded by whitespace starts a trailing comment, dropped to end
//     of line (the newline is kept);
//   - a '#' is NOT a comment — and is never touched — inside single quotes,
//     double quotes, $'…' ANSI-C quotes, heredoc bodies, ${…} parameter
//     expansions (${x#p} and ${#x} are operators, not comments), ((…)) /
//     $((…)) arithmetic (16#ff is base notation), and [[…]] conditionals;
//   - backtick and $(…) contents are nested scripts, where the same rules
//     apply (a '#' there follows normal comment semantics);
//   - every other '#' (${#x}, ${x#p}, case patterns, zsh %# prompts) is
//     syntax or data and passes through.
//
// Heredoc bodies are tracked by their delimiters (<<EOF, <<'EOF', <<"EOF",
// <<\EOF and tab-indented <<- forms): every line until the delimiter line is
// data, never comments, and is emitted verbatim. A heredoc nested inside a
// ${…} expansion is deliberately out of scope — none of the scripts use one,
// and the exec suites are the backstop that would catch a misparse.
func stripShellComments(src string, kind ShellKind) string {
	var b strings.Builder
	b.Grow(len(src))
	i, n := 0, len(src)

	// Lexical state that spans lines.
	var inSQ, inDQ, inANSIC, inBT bool
	var paramDepth, arithDepth, condDepth int
	var heredocDelim string
	var heredocTabs bool

	// lineStart is the index of the first byte of the current line;
	// pendingWS counts the blank bytes since lineStart that have not been
	// emitted yet. They are buffered so a comment line can take its own
	// indentation with it instead of leaving a whitespace-only line behind.
	lineStart := 0
	pendingWS := 0
	flushWS := func() {
		if pendingWS > 0 {
			b.WriteString(src[i-pendingWS : i])
			pendingWS = 0
		}
	}

	for i < n {
		// An active heredoc swallows every line verbatim until the
		// delimiter line; comments and quoting do not exist in a body.
		if heredocDelim != "" {
			j := i
			for j < n && src[j] != '\n' {
				j++
			}
			line := src[i:j]
			b.WriteString(line)
			if j < n {
				b.WriteByte('\n')
				j++
			}
			cmp := line
			if heredocTabs {
				k := 0
				for k < len(cmp) && cmp[k] == '\t' {
					k++
				}
				cmp = cmp[k:]
			}
			if cmp == heredocDelim {
				heredocDelim = ""
				heredocTabs = false
			}
			i = j
			lineStart = i
			continue
		}

		c := src[i]

		// Quoted regions: '#' inside is data. Backslash escapes the next
		// byte inside double quotes, ANSI-C quotes and backticks.
		if inSQ {
			b.WriteByte(c)
			i++
			if kind == ShellFish && c == '\\' && i < n && (src[i] == '\\' || src[i] == '\'') {
				// Fish, unlike the POSIX family, permits these two escapes
				// inside single quotes. An escaped quote must not end them.
				b.WriteByte(src[i])
				i++
			} else if c == '\'' {
				inSQ = false
			}
			continue
		}
		if inANSIC {
			b.WriteByte(c)
			i++
			if c == '\\' && i < n {
				b.WriteByte(src[i])
				i++
			} else if c == '\'' {
				inANSIC = false
			}
			continue
		}
		if inDQ {
			b.WriteByte(c)
			i++
			if c == '\\' && i < n {
				b.WriteByte(src[i])
				i++
			} else if c == '"' {
				inDQ = false
			}
			continue
		}

		switch {
		case c == ' ' || c == '\t':
			// Buffered, not emitted: a comment line may take it along.
			pendingWS++
			i++
		case c == '\\':
			// An escaped byte is literal: \# is a hash, not a comment.
			flushWS()
			b.WriteByte(c)
			i++
			if i < n {
				b.WriteByte(src[i])
				i++
			}
		case c == '\'':
			flushWS()
			inSQ = true
			b.WriteByte(c)
			i++
		case c == '"':
			flushWS()
			inDQ = true
			b.WriteByte(c)
			i++
		case c == '`':
			flushWS()
			inBT = !inBT
			b.WriteByte(c)
			i++
		case c == '$' && i+1 < n && src[i+1] == '\'':
			flushWS()
			inANSIC = true
			b.WriteString("$'")
			i += 2
		case c == '$' && i+1 < n && src[i+1] == '{':
			flushWS()
			paramDepth++
			b.WriteString("${")
			i += 2
		case c == '$' && i+2 < n && src[i+1] == '(' && src[i+2] == '(':
			flushWS()
			arithDepth += 2
			b.WriteString("$((")
			i += 3
		case c == '(' && i+1 < n && src[i+1] == '(':
			flushWS()
			arithDepth += 2
			b.WriteString("((")
			i += 2
		case c == '[' && i+1 < n && src[i+1] == '[':
			flushWS()
			condDepth++
			b.WriteString("[[")
			i += 2
		case c == '<':
			if i+1 >= n || src[i+1] != '<' {
				flushWS()
				b.WriteByte(c)
				i++
				continue
			}
			if i+2 < n && src[i+2] == '<' {
				// <<< here-string, not a heredoc: consume the run.
				flushWS()
				b.WriteString("<<<")
				i += 3
				continue
			}
			if i+2 < n && src[i+2] == '=' {
				// <<= (a here-string variant in some shells): not a heredoc.
				flushWS()
				b.WriteString("<<=")
				i += 3
				continue
			}
			// A heredoc opener in normal context: <<, <<- (tabs), with
			// an optionally quoted/escaped delimiter word. Content
			// starts on the following line.
			flushWS()
			b.WriteByte('<')
			i++
			b.WriteByte('<')
			i++
			heredocTabs = false
			if i < n && src[i] == '-' {
				heredocTabs = true
				b.WriteByte('-')
				i++
			}
			for i < n && (src[i] == ' ' || src[i] == '\t') {
				b.WriteByte(src[i])
				i++
			}
			var delim strings.Builder
			switch {
			case i < n && src[i] == '\'':
				b.WriteByte('\'')
				i++
				for i < n && src[i] != '\'' {
					delim.WriteByte(src[i])
					b.WriteByte(src[i])
					i++
				}
				if i < n {
					b.WriteByte('\'')
					i++
				}
			case i < n && src[i] == '"':
				b.WriteByte('"')
				i++
				for i < n && src[i] != '"' {
					delim.WriteByte(src[i])
					b.WriteByte(src[i])
					i++
				}
				if i < n {
					b.WriteByte('"')
					i++
				}
			case i < n && src[i] == '\\':
				b.WriteByte('\\')
				i++
				if i < n {
					delim.WriteByte(src[i])
					b.WriteByte(src[i])
					i++
				}
			default:
				for i < n && src[i] != ' ' && src[i] != '\t' && src[i] != '\n' {
					delim.WriteByte(src[i])
					b.WriteByte(src[i])
					i++
				}
			}
			if delim.Len() > 0 {
				heredocDelim = delim.String()
			}
			// A delimiter word left empty means the delimiter is the
			// first word of the next line (<<\nEOF); the heredoc body
			// starts after that line. That form is not in the shipped
			// scripts; skipping it only under-strips, never corrupts.
		case c == '#':
			// Region-protected: an operator or literal, never a comment.
			if paramDepth > 0 || arithDepth > 0 || condDepth > 0 || inBT {
				flushWS()
				b.WriteByte(c)
				i++
				continue
			}
			firstNonBlank := lineStart
			for firstNonBlank < i && (src[firstNonBlank] == ' ' || src[firstNonBlank] == '\t') {
				firstNonBlank++
			}
			if firstNonBlank == i {
				// A comment line; its indentation goes with it. The
				// shebang is preserved: only a "#!" at byte 0 of the
				// file is one, and it keeps the whole line.
				if i == 0 && i+1 < n && src[i+1] == '!' {
					flushWS()
					j := i
					for j < n && src[j] != '\n' {
						j++
					}
					b.WriteString(src[i:j])
					if j < n {
						b.WriteByte('\n')
						j++
					}
					i = j
					lineStart = i
					continue
				}
				// Drop the comment line, its indentation and its
				// newline; the previous line's newline keeps the
				// lines apart.
				pendingWS = 0
				for i < n && src[i] != '\n' {
					i++
				}
				if i < n {
					i++ // consume the newline
				}
				lineStart = i
				continue
			}
			if src[i-1] == ' ' || src[i-1] == '\t' {
				// Trailing comment: drop it and the whitespace that
				// introduced it, keep the newline.
				pendingWS = 0
				for i < n && src[i] != '\n' {
					i++
				}
				if i < n {
					b.WriteByte('\n')
					i++
				}
				lineStart = i
				continue
			}
			flushWS()
			b.WriteByte(c)
			i++
		case c == '}' && paramDepth > 0:
			flushWS()
			paramDepth--
			b.WriteByte(c)
			i++
		case c == ')' && arithDepth > 0:
			flushWS()
			arithDepth--
			b.WriteByte(c)
			i++
		case c == ']' && i+1 < n && src[i+1] == ']' && condDepth > 0:
			flushWS()
			condDepth--
			b.WriteString("]]")
			i += 2
		case c == '\n':
			flushWS()
			b.WriteByte('\n')
			i++
			lineStart = i
		default:
			flushWS()
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
