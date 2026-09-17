package content

// THE SHAPE COMPARISON'S NORMALISER (nocx-lmb6v.6, moved out of the parity
// test by nocx-4yjwk.7).
//
// `validateOnDiskSchemaShapeFor` refuses a file whose stamp and contents
// disagree, by digesting everything in `sqlite_master`. It used to digest the
// stored CREATE statements VERBATIM, and that made it refuse a population it
// exists to protect: SQLite re-emits a table's DDL with its name QUOTED after
// `ALTER TABLE ... RENAME TO`, which is the last statement of the table
// rebuild every CHECK-widening rung has to perform. So a database that had
// ever been migrated held `CREATE TABLE "grant_scopes"` where a fresh install
// held `CREATE TABLE grant_scopes`, the digests differed, and the NEXT open of
// a successfully upgraded file was refused with "stamp and contents disagree"
// — telling a person to update to the build they were already running, about a
// migration that had worked. Measured on 2026-09-04 by reopening a 14→16
// upgrade; the walk itself was never the problem, only the reading of it.
//
// Digesting the NORMALISED text is the fix, and it is not a weakening. What is
// folded away is exactly the set of differences that cannot change the
// database the statement produces — identifier quoting, `IF NOT EXISTS`,
// whitespace, SQL comments, and case outside string literals. What is NOT
// folded away is everything a shape is judged by: the contents of string
// literals (so a CHECK widened to the wrong set of values is still caught),
// column names, types, order, nullability, defaults, primary-key membership,
// foreign-key targets and actions, index uniqueness and column order, STRICT
// and WITHOUT ROWID, and the set of objects itself.
//
// It lives here rather than in schema_parity_test.go because production reads
// it now. The parity test is its other caller and its adversarial half:
// TestTheSchemaComparisonReportsADivergenceRatherThanNormalisingItAway
// fabricates a stray column and a wrongly-widened CHECK and requires both to
// survive normalisation.

import (
	"strings"
	"unicode"
)

// normaliseDDL folds away the five kinds of insignificant difference named in
// this file's header and NOTHING else. Its whole design is the single piece of
// state it carries: whether the scanner is inside a string literal. Outside
// one, case, quoting, comments and whitespace are noise; inside one, every
// byte is the value a CHECK constraint or a DEFAULT is made of, and is kept
// exactly as written.
func normaliseDDL(ddl string) string {
	var b strings.Builder
	rs := []rune(ddl)
	inLiteral := false
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if inLiteral {
			b.WriteRune(c)
			if c == '\'' {
				// '' is an escaped quote and does not end the literal.
				if i+1 < len(rs) && rs[i+1] == '\'' {
					b.WriteRune('\'')
					i++
					continue
				}
				inLiteral = false
			}
			continue
		}
		switch {
		case c == '\'':
			inLiteral = true
			b.WriteRune(c)
		case c == '-' && i+1 < len(rs) && rs[i+1] == '-':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			b.WriteRune(' ')
		case c == '/' && i+1 < len(rs) && rs[i+1] == '*':
			i += 2
			for i+1 < len(rs) && !(rs[i] == '*' && rs[i+1] == '/') {
				i++
			}
			i++
			b.WriteRune(' ')
		case c == '"' || c == '`' || c == '[' || c == ']':
			// Identifier quoting. `ALTER TABLE ... RENAME` re-emits the name
			// quoted; a hand-written CREATE does not. The identifier itself is
			// kept — only the delimiters go.
		case unicode.IsSpace(c):
			b.WriteRune(' ')
		default:
			b.WriteRune(unicode.ToLower(c))
		}
	}
	out := strings.TrimSpace(collapseSpaces(b.String()))
	out = strings.ReplaceAll(out, "if not exists ", "")
	return squeezeAroundPunctuation(out)
}

// collapseSpaces reduces runs of spaces to one, outside string literals only —
// two spaces inside a DEFAULT are part of the value.
func collapseSpaces(s string) string {
	var b strings.Builder
	inLiteral := false
	previousWasSpace := false
	for _, c := range s {
		if inLiteral {
			b.WriteRune(c)
			if c == '\'' {
				inLiteral = false
			}
			continue
		}
		if c == '\'' {
			inLiteral = true
			previousWasSpace = false
			b.WriteRune(c)
			continue
		}
		if c == ' ' {
			if !previousWasSpace {
				b.WriteRune(c)
			}
			previousWasSpace = true
			continue
		}
		previousWasSpace = false
		b.WriteRune(c)
	}
	return b.String()
}

// squeezeAroundPunctuation drops the spaces that only separate a token from a
// bracket or a comma, so `CHECK (x IN ('a'))` and `check(x in('a'))` are one
// string. Literals are skipped for the same reason as everywhere else.
func squeezeAroundPunctuation(s string) string {
	isPunctuation := func(c byte) bool {
		return c == '(' || c == ')' || c == ',' || c == ';'
	}
	var b strings.Builder
	rs := []rune(s)
	inLiteral := false
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if inLiteral {
			b.WriteRune(c)
			if c == '\'' {
				if i+1 < len(rs) && rs[i+1] == '\'' {
					b.WriteRune('\'')
					i++
					continue
				}
				inLiteral = false
			}
			continue
		}
		if c == '\'' {
			inLiteral = true
			b.WriteRune(c)
			continue
		}
		if c == ' ' {
			if i+1 < len(rs) && rs[i+1] < 128 && isPunctuation(byte(rs[i+1])) {
				continue
			}
			emitted := b.String()
			if len(emitted) > 0 && isPunctuation(emitted[len(emitted)-1]) {
				continue
			}
		}
		b.WriteRune(c)
	}
	return b.String()
}
