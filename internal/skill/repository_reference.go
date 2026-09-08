package skill

import "strings"

// The origin of an install route is a fact nocx establishes about a document
// it fetched itself — never a value the model asserts, and never inferred from
// what happened to be fetched most recently (nocx-89mi5's decision). These two
// functions are the whole of that establishment, and they live here because
// this package owns what a repository address is: the assistant may ask where
// a repository lives and whether a document names it, and may derive neither
// itself.

// RepositoryURL is the canonical web address of a resolved repository. The
// host is fixed because the address recognizer accepts no other one: a
// resolution exists only for github.com and raw.githubusercontent.com
// addresses, so every repository this package can pin is a GitHub repository.
// A second forge arrives as a second adapter, and this function moves behind
// the same seam the adapter does; it does not become a guess in the meantime.
func RepositoryURL(repository string) string {
	if repository == "" {
		return ""
	}
	return "https://github.com/" + repository
}

// DocumentNamesRepository reports whether text refers to the given
// owner/repository. It is the check that makes the approval window's sentence
// TRUE AS WRITTEN — "the page you gave named this repository" — rather than
// merely reported: nocx does not ask where an install came from, it confirms
// that the page it is holding does name the repository the resolution pinned.
//
// The match is case-insensitive, because GitHub owner and repository names are
// compared that way, and it is bounded at both ends so that a longer name is
// never mistaken for this one: `not-agentmail-to/agentmail-skills` and
// `agentmail-to/agentmail-skills-archive` are different repositories and
// neither names this one. A trailing `.` is a boundary rather than a name
// character, so `…/agentmail-skills.git` and a slug at the end of a sentence
// both still name it.
func DocumentNamesRepository(text, repository string) bool {
	if text == "" || repository == "" {
		return false
	}
	haystack := strings.ToLower(text)
	needle := strings.ToLower(repository)
	for at := 0; ; {
		found := strings.Index(haystack[at:], needle)
		if found < 0 {
			return false
		}
		start := at + found
		end := start + len(needle)
		if !nameByteBefore(haystack, start) && !nameByteAt(haystack, end) {
			return true
		}
		at = start + 1
	}
}

func nameByteBefore(text string, at int) bool {
	if at == 0 {
		return false
	}
	return isRepositoryNameByte(text[at-1])
}

func nameByteAt(text string, at int) bool {
	if at >= len(text) {
		return false
	}
	return isRepositoryNameByte(text[at])
}

func isRepositoryNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-', b == '_':
		return true
	default:
		return false
	}
}
