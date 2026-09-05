package skill

import (
	"regexp"
	"strings"
)

// Finding identifies a suspicious pattern, the FILE it was found in, and the
// source line where it first occurs. The line is included so an approval or a
// read result can name the evidence without returning a second copy of the
// content; the path is included because a line number with no file attached
// points at nothing a person can open.
//
// The path is a field of the finding and not a wrapper around it. There was a
// second shape for exactly one release — an audit-only struct that carried a
// path beside an embedded Finding — and it existed because only the audit
// scanned more than one file. Now that an install fetches a bundle and the
// viewer reads any file of one, every producer has a path to name, so the
// wrapper would be a second vocabulary for one fact with no caller left that
// needs the narrower one (nocx-872jc.4).
//
// The json tags are the wire spelling declared once, here: four fields for
// one fact, so a finding travelling in an approval request, a finding
// travelling in a skill preview, a finding travelling in an audit and a
// finding travelling beside a file a person opened cannot come to disagree
// about what a finding is (contracts/agent.approvalRequested.schema.json,
// skills.preview, skills.audit, skills.file).
type Finding struct {
	Path       string `json:"path"`
	PatternID  string `json:"patternId"`
	Line       string `json:"line"`
	LineNumber int    `json:"lineNumber"`
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

// Scan examines at most 64 KiB of ONE FILE's bytes and returns one finding
// per matching pattern, in stable pattern-table order. It is advisory:
// callers surface findings but do not turn them into an unreadable result.
//
// The path is a PARAMETER and not something a caller fills in afterwards, so
// there is one way to produce a finding and it names a file by construction.
// Callers that scan a single document still pass its name — "SKILL.md" — for
// the same reason the wire carries it: a surface that had to guess which file
// an unnamed finding came from would guess wrong the first time a bundle
// carried two.
//
// lineNumber counts from the first byte of THAT file, so it is checkable
// against skills.file, which returns the same bytes.
func Scan(path string, b []byte) []Finding {
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
		findings = append(findings, Finding{Path: path, PatternID: pattern.id, Line: line, LineNumber: lineNumber})
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
