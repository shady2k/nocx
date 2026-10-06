package session

import (
	"bufio"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/sandbox"
)

const testSeatbeltNonce = "0123456789abcdef0123456789abcdef"

func TestSeatbeltCompactRecordRequiresAdjacentLaunchTag(t *testing.T) {
	parser := seatbeltLogParser{nonce: testSeatbeltNonce}
	const line = "2026-10-06 12:00:00.000 sandboxd[42] <Notice>: Sandbox: worker(7) deny(1) file-read-data /private/tmp/secret"
	if _, ok := parser.consume(line); ok {
		t.Fatal("denial was published before its launch tag")
	}
	observation, ok := parser.consume(testSeatbeltNonce)
	if !ok {
		t.Fatal("expected tagged multiline denial")
	}
	if observation.Source != sandbox.DiagnosticMacSeatbelt || observation.Precision != sandbox.PrecisionReportedDenial || observation.Operation != "file-read-data" || observation.Access != sandbox.DiagnosticRead || observation.Path != "/private/tmp/secret" || !observation.PathKnown || observation.Executable != "" {
		t.Fatalf("unexpected observation: %#v", observation)
	}
	if _, ok := parser.consume(testSeatbeltNonce); ok {
		t.Fatal("tag replay duplicated a consumed denial")
	}
	for _, intervening := range []string{"", "unrelated log entry", strings.Repeat("f", 32), testSeatbeltNonce + " trailing"} {
		parser.consume(line)
		parser.consume(intervening)
		if _, ok := parser.consume(testSeatbeltNonce); ok {
			t.Fatalf("tag crossed an intervening record: %q", intervening)
		}
	}
	parser.consume("Sandbox: worker allow file-read-data /tmp/secret")
	if _, ok := parser.consume(testSeatbeltNonce); ok {
		t.Fatal("non-denial became a reported denial")
	}
}

func TestParseSeatbeltLogLineKeepsUntrustworthyPathUnknown(t *testing.T) {
	parser := seatbeltLogParser{nonce: testSeatbeltNonce}
	parser.consume(`sandboxd: Sandbox: worker(7) deny(1) file-write-create "/private/tmp/name with spaces"`)
	observation, ok := parser.consume(testSeatbeltNonce)
	if !ok {
		t.Fatal("expected correlated denial")
	}
	if observation.PathKnown || observation.Path != "" || observation.Access != sandbox.DiagnosticWrite {
		t.Fatalf("untrustworthy path was attributed: %#v", observation)
	}
}

func TestReadBoundedSeatbeltLineDiscardsOversizeThroughNewline(t *testing.T) {
	oversize := strings.Repeat("x", seatbeltLogLineLimit+100)
	reader := bufio.NewReaderSize(strings.NewReader(oversize+"\nnext record\n"), seatbeltLogReadChunk)
	var storage [seatbeltLogLineLimit]byte
	line, _, err := readBoundedSeatbeltLine(reader, storage[:])
	if err != nil || len(line) != 0 {
		t.Fatalf("oversize record was not discarded: len=%d err=%v", len(line), err)
	}
	line, _, err = readBoundedSeatbeltLine(reader, storage[:])
	if err != nil || string(line) != "next record" {
		t.Fatalf("reader did not recover after oversize record: %q err=%v", line, err)
	}
}

func TestSeatbeltCompactRecordCannotCrossDiscardedOversizeLine(t *testing.T) {
	input := strings.Join([]string{
		"Sandbox: worker(7) deny(1) file-read-data /private/tmp/stale",
		strings.Repeat("x", seatbeltLogLineLimit+100),
		testSeatbeltNonce,
		"Sandbox: worker(7) deny(1) file-read-data /private/tmp/current",
		testSeatbeltNonce,
	}, "\n") + "\n"
	reader := bufio.NewReaderSize(strings.NewReader(input), seatbeltLogReadChunk)
	var storage [seatbeltLogLineLimit]byte
	parser := seatbeltLogParser{nonce: testSeatbeltNonce}
	for index := range 5 {
		line, _, err := readBoundedSeatbeltLine(reader, storage[:])
		if err != nil {
			t.Fatal(err)
		}
		observation, ok := parser.consume(string(line))
		if index == 4 {
			if !ok || !observation.PathKnown || observation.Path != "/private/tmp/current" {
				t.Fatalf("following complete record was lost: %+v %v", observation, ok)
			}
		} else if ok {
			t.Fatalf("incomplete record crossed discarded data at line %d: %+v", index, observation)
		}
	}
}

func TestSeatbeltNonceRequiresCanonicalLowercaseHex(t *testing.T) {
	for _, nonce := range []string{testSeatbeltNonce, strings.Repeat("0", 32)} {
		if !validSeatbeltNonce(nonce) {
			t.Fatalf("rejected valid nonce %q", nonce)
		}
	}
	for _, nonce := range []string{"", strings.Repeat("A", 32), strings.Repeat("g", 32), strings.Repeat("0", 31)} {
		if validSeatbeltNonce(nonce) {
			t.Fatalf("accepted invalid nonce %q", nonce)
		}
	}
}

func TestReadBoundedSeatbeltLinePreservesFollowingRecordsInSameChunk(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader("first\nsecond\nthird\n"), seatbeltLogReadChunk)
	var storage [seatbeltLogLineLimit]byte
	for _, expected := range []string{"first", "second", "third"} {
		line, _, err := readBoundedSeatbeltLine(reader, storage[:])
		if err != nil || string(line) != expected {
			t.Fatalf("buffered record lost: got %q, want %q, err %v", line, expected, err)
		}
	}
}

func TestParseSeatbeltLogLineNeverTruncatesUnquotedWhitespacePath(t *testing.T) {
	parser := seatbeltLogParser{nonce: testSeatbeltNonce}
	parser.consume("Sandbox: worker(7) deny(1) file-read-data /private/tmp/name with spaces")
	observation, ok := parser.consume(testSeatbeltNonce)
	if !ok || observation.PathKnown || observation.Path != "" {
		t.Fatalf("ambiguous path became truncated authority: %+v", observation)
	}
}
