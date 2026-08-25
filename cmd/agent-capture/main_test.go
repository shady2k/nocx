package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCaptureJSONLRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	header := captureHeader{
		Agent:   "bash",
		Argv:    []string{"bash", "-i"},
		Cols:    80,
		Rows:    24,
		Started: "2026-08-25T12:00:00Z",
		Script:  []string{"+0ms \"\\r\""},
	}
	chunks := []captureChunk{
		{AtMs: 3, Offset: 0, Data: "hello\r\n"},
		{AtMs: 8, Offset: 7, Data: "\x1b[2J"},
	}
	if err := writeCapture(path, header, chunks); err != nil {
		t.Fatalf("writeCapture: %v", err)
	}
	gotHeader, gotChunks, err := readCapture(path)
	if err != nil {
		t.Fatalf("readCapture: %v", err)
	}
	gotHeaderJSON, err := json.Marshal(gotHeader)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	expectedHeaderJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal expected header: %v", err)
	}
	if !bytes.Equal(gotHeaderJSON, expectedHeaderJSON) {
		t.Fatalf("header mismatch: got %s, want %s", gotHeaderJSON, expectedHeaderJSON)
	}
	gotChunksJSON, err := json.Marshal(gotChunks)
	if err != nil {
		t.Fatalf("marshal chunks: %v", err)
	}
	expectedChunksJSON, err := json.Marshal(chunks)
	if err != nil {
		t.Fatalf("marshal expected chunks: %v", err)
	}
	if !bytes.Equal(gotChunksJSON, expectedChunksJSON) {
		t.Fatalf("chunks mismatch: got %s, want %s", gotChunksJSON, expectedChunksJSON)
	}
}

func TestParseScriptCommittedForms(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"idle", "working", "permission", "modal", "subagent"} {
		name := name
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "internal", "agentdriver", "testdata", "captures", "scripts", name+".script")
			steps, err := parseScript(path)
			if err != nil {
				t.Fatalf("parseScript(%s): %v", path, err)
			}
			if len(steps) == 0 {
				t.Fatalf("parseScript(%s) returned no steps", path)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "forms.script")
	if err := os.WriteFile(path, []byte("# comment\n\n12000\n500 hello\\r\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	steps, err := parseScript(path)
	if err != nil {
		t.Fatalf("parseScript(forms): %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].delay != 12*time.Second || len(steps[0].send) != 0 {
		t.Fatalf("delay-only step = %#v, want 12s and empty input", steps[0])
	}
	if steps[1].delay != 500*time.Millisecond || string(steps[1].send) != "hello\r" {
		t.Fatalf("escaped step = %#v, want hello\\r", steps[1])
	}
}

func TestParseScriptRejectsMalformedLines(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "missing mark", line: " hello", want: "missing millisecond mark"},
		{name: "negative mark", line: "-1 hi", want: "negative"},
		{name: "non integer", line: "nope hi", want: "not a millisecond mark"},
		{name: "trailing slash", line: "1 hi\\", want: "trailing backslash"},
		{name: "short hex", line: "1 hi\\x1", want: "short \\x escape"},
		{name: "bad hex", line: "1 hi\\xz0", want: "bad \\x escape"},
		{name: "unknown escape", line: "1 hi\\q", want: "unknown escape"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.script")
			if err := os.WriteFile(path, []byte(tc.line+"\n"), 0o600); err != nil {
				t.Fatalf("write script: %v", err)
			}
			_, err := parseScript(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseScript error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestChunksThroughMarkIncludesExactBoundary(t *testing.T) {
	t.Parallel()
	chunks := []captureChunk{
		{AtMs: 10, Offset: 0, Data: "a"},
		{AtMs: 20, Offset: 1, Data: "b"},
		{AtMs: 20, Offset: 2, Data: "c"},
		{AtMs: 21, Offset: 3, Data: "d"},
	}
	if consumed := chunksThroughMark(chunks, 9, 0); consumed != 0 {
		t.Fatalf("chunksThroughMark(..., 9) = %d, want 0", consumed)
	}
	consumed := chunksThroughMark(chunks, 20, 0)
	if consumed != 3 {
		t.Fatalf("chunksThroughMark(..., 20) = %d, want 3", consumed)
	}
	if consumed = chunksThroughMark(chunks, 21, consumed); consumed != 4 {
		t.Fatalf("chunksThroughMark(..., 21, 3) = %d, want 4", consumed)
	}
}

func TestParseMarksRejectsDescendingMarks(t *testing.T) {
	t.Parallel()
	_, err := parseMarks("20,10")
	if err == nil || !strings.Contains(err.Error(), "non-decreasing") {
		t.Fatalf("parseMarks error = %v, want non-decreasing", err)
	}
	marks, err := parseMarks("0,20,20")
	if err != nil {
		t.Fatalf("parseMarks valid input: %v", err)
	}
	if len(marks) != 3 || marks[2] != 20 {
		t.Fatalf("parseMarks = %v, want [0 20 20]", marks)
	}
}

func TestCaptureProgramEndsWhenScriptExhausted(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "capture.jsonl")
	var stderr bytes.Buffer
	steps := []scriptStep{
		{
			delay: 10 * time.Millisecond,
			send:  []byte("printf 'hello-from-capture\\n'\\r"),
			label: `+10ms "printf 'hello-from-capture\\n'\\r"`,
		},
		{
			delay: 30 * time.Millisecond,
			send:  []byte{},
			label: `+30ms ""`,
		},
	}
	if err := captureProgram(outPath, []string{"bash", "-i"}, 80, 24, 500*time.Millisecond, steps, true, &stderr); err != nil {
		t.Fatalf("captureProgram: %v", err)
	}
	if !strings.Contains(stderr.String(), "capture ended because script ended") {
		t.Fatalf("stderr = %q, want script-ended completion", stderr.String())
	}
	header, chunks, err := readCapture(outPath)
	if err != nil {
		t.Fatalf("readCapture: %v", err)
	}
	if header.Agent != "bash" || len(chunks) == 0 {
		t.Fatalf("capture = agent %q, %d chunks; want bash and output", header.Agent, len(chunks))
	}
}
