package skill

import (
	"regexp"
	"strings"
)

// Finding identifies a suspicious pattern and the source line where it first
// occurs. The line is included so an approval or read result can name the
// evidence without returning a second copy of the content.
type Finding struct {
	PatternID  string
	Line       string
	LineNumber int
}

// filler bounds the words an attacker may insert between key tokens
// ("ignore all PRIOR instructions"). The upstream Python bounded it against
// catastrophic backtracking; Go's regexp is RE2 and does not backtrack, so
// here the bound is about matching the same strings as upstream and keeping
// the patterns readable — not about safety.
const filler = `(?:\w+\s+){0,8}`

// maxScanBytes bounds the input. The scan is an advisory guard, not an
// archival search, and injected content is near the start of what it infects.
const maxScanBytes = 64 << 10

type scanPattern struct {
	id string
	re *regexp.Regexp
}

var scanPatterns = []scanPattern{
	// Classic instruction-override vocabulary. Ordinary imperative prose does
	// not match; the words identify an attempt to replace the instruction set.
	{id: "prompt_injection", re: regexp.MustCompile(`(?i)ignore\s+` + filler + `(previous|all|above|prior)\s+` + filler + `instructions`)},
	{id: "sys_prompt_override", re: regexp.MustCompile(`(?i)system\s+prompt\s+override`)},
	{id: "disregard_rules", re: regexp.MustCompile(`(?i)disregard\s+` + filler + `(your|all|any)\s+` + filler + `(instructions|rules|guidelines)`)},
	{id: "bypass_restrictions", re: regexp.MustCompile(`(?i)act\s+as\s+(if|though)\s+` + filler + `you\s+` + filler + `(have\s+no|don't\s+have)\s+` + filler + `(restrictions|limits|rules)`)},

	// Exfiltration and credential reads are attack vocabulary in any skill
	// file, including a reference file copied into an authored skill.
	{id: "exfil_curl", re: regexp.MustCompile(`(?i)curl\s+[^\n]{0,1000}\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)S?\b`)},
	{id: "exfil_wget", re: regexp.MustCompile(`(?i)wget\s+[^\n]{0,1000}\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)S?\b`)},
	{id: "read_secrets", re: regexp.MustCompile(`(?i)cat\s+[^\n]{0,1000}(\.env|credentials|\.netrc|\.pgpass|\.npmrc|\.pypirc)`)},
	{id: "send_to_url", re: regexp.MustCompile(`(?i)(send|post|upload|transmit)\s+[^\n]{0,1000}\s+(to|at)\s+https?://`)},
	{id: "context_exfil", re: regexp.MustCompile(`(?i)(include|output|print|share)\s+` + filler + `(conversation|chat\s+history|previous\s+messages|full\s+context|entire\s+context)`)},

	// Persistence into instructions/configuration changes future assistant
	// behavior and is therefore suspicious even when presented as a command.
	{id: "agent_config_mod", re: regexp.MustCompile(`(?i)(update|modify|edit|write|change|append|add\s+to)\s+[^\n]{0,1000}(AGENTS\.md|CLAUDE\.md|\.cursorrules|\.clinerules)`)},
	{id: "hermes_config_mod", re: regexp.MustCompile(`(?i)(update|modify|edit|write|change|append|add\s+to)\s+[^\n]{0,1000}\.hermes/(config\.yaml|SOUL\.md)`)},
}

// Scan examines at most 64 KiB and returns one finding per matching pattern,
// in stable pattern-table order. It is advisory: callers surface findings but
// do not turn them into an unreadable result.
func Scan(b []byte) []Finding {
	if len(b) > maxScanBytes {
		b = b[:maxScanBytes]
	}
	text := string(b)
	findings := make([]Finding, 0)
	for _, pattern := range scanPatterns {
		match := pattern.re.FindStringIndex(text)
		if match == nil {
			continue
		}
		line, lineNumber := sourceLine(text, match[0])
		findings = append(findings, Finding{PatternID: pattern.id, Line: line, LineNumber: lineNumber})
	}
	return findings
}

func sourceLine(text string, offset int) (string, int) {
	lineNumber := strings.Count(text[:offset], "\n") + 1
	start := strings.LastIndexByte(text[:offset], '\n') + 1
	end := strings.IndexByte(text[offset:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += offset
	}
	line := text[start:end]
	line = strings.TrimSuffix(line, "\r")
	return line, lineNumber
}
