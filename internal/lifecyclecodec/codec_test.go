package lifecyclecodec

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
)

// gap records one skipped region reported through the gap sink.
type gap struct {
	bytes  int
	frames int
}

func sink(g *[]gap) GapSink {
	return func(b, f int) { *g = append(*g, gap{bytes: b, frames: f}) }
}

func testCapability() lifecycle.Capability {
	var c lifecycle.Capability
	for i := range c {
		c[i] = byte(i + 1)
	}
	return c
}

func env(kind lifecycle.EventKind, evt lifecycle.Event, seq uint64) lifecycle.Envelope {
	return lifecycle.Envelope{
		Version:    1,
		Lane:       "lane-1",
		Domain:     "dom-1",
		Epoch:      1,
		Sequence:   seq,
		Capability: testCapability(),
		Event:      evt,
	}
}

func helloEvt(shell string) lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: shell}}
}

func promptReadyEvt() lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{}}
}

func startEvt(id *lifecycle.AttemptID, command string) lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: id, Command: command}}
}

func fenceNonce() lifecycle.FenceNonce {
	var f lifecycle.FenceNonce
	for i := range f {
		f[i] = byte(i + 1)
	}
	return f
}

// writeRawFrame writes a length-delimited JSON frame with the raw body.
func writeRawFrame(w io.Writer, raw string) {
	var hdr [4]byte
	// #nosec G115 -- test fixture; the raw bodies are short literals.
	binary.BigEndian.PutUint32(hdr[:], uint32(len(raw)))
	_, _ = w.Write(hdr[:])
	_, _ = io.WriteString(w, raw)
}

// TestRoundTripAllKinds proves a well-formed frame decodes to the envelope
// that was encoded, for every event kind — including the wire-critical
// encodings: the capability as 64 hex chars, the fence as 64 hex chars, and
// a complete that names no attempt (the shell that attached never learns the
// app-minted id, protocol §8).
func TestRoundTripAllKinds(t *testing.T) {
	attID := lifecycle.AttemptID("att-1234")
	code := 2
	next := uint64(9)
	rid := lifecycle.RequestID("req-abc")
	envs := []lifecycle.Envelope{
		env(lifecycle.KindHello, helloEvt("bash"), 1),
		env(lifecycle.KindStart, startEvt(&attID, "ls -la"), 2),
		env(lifecycle.KindStart, startEvt(nil, ""), 3),
		env(lifecycle.KindComplete, lifecycle.Event{Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{
			AttemptID: &attID, ExitCode: &code, Fence: fenceNonce(),
		}}, 4),
		env(lifecycle.KindComplete, lifecycle.Event{Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{
			ExitCode: &code, Fence: fenceNonce(),
		}}, 5),
		env(lifecycle.KindPromptReady, promptReadyEvt(), 6),
		env(lifecycle.KindRefreshRequest, lifecycle.Event{Kind: lifecycle.KindRefreshRequest, RefreshRequest: &lifecycle.RefreshRequest{RequestID: rid}}, 0),
		env(lifecycle.KindSnapshot, lifecycle.Event{Kind: lifecycle.KindSnapshot, Snapshot: &lifecycle.Snapshot{
			RequestID: rid, ShellState: lifecycle.ShellAtPrompt,
			ActiveAttemptID: &attID,
			LastCompleted:   &lifecycle.CompletedRef{AttemptID: attID, ExitCode: &code},
			NextSequence:    next,
		}}, 7),
		env(lifecycle.KindAccept, lifecycle.Event{Kind: lifecycle.KindAccept, Accept: &lifecycle.Accept{}}, 0),
		env(lifecycle.KindDomainClosed, lifecycle.Event{Kind: lifecycle.KindDomainClosed, DomainClosed: &lifecycle.DomainClosedEvent{}}, 8),
		env(lifecycle.KindDomainRequest, lifecycle.Event{Kind: lifecycle.KindDomainRequest, DomainRequest: &lifecycle.DomainRequest{
			RequestID: "r-dom-1-0", Env: "ssh", Host: "box.example.com", User: "alice", Port: 2222,
		}}, 9),
		env(lifecycle.KindDomainGrant, lifecycle.Event{Kind: lifecycle.KindDomainGrant, DomainGrant: &lifecycle.DomainGrant{
			RequestID: "r-dom-1-0", Env: "sudo",
			Domain: "dom-2", Epoch: 2,
			Bootstrap: "sudo --preserve-fds=3,4 -i bash --rcfile /dev/fd/4 -i\n# with a \"quote\" and a \\backslash\n",
		}}, 0),
		env(lifecycle.KindAgentEnrol, lifecycle.Event{Kind: lifecycle.KindAgentEnrol, AgentEnrol: &lifecycle.AgentEnrol{
			RequestID: "r-agent-1-0", Agent: "claude", Cols: 120, Rows: 40,
		}}, 10),
		env(lifecycle.KindAgentEnrolled, lifecycle.Event{Kind: lifecycle.KindAgentEnrolled, AgentEnrolled: &lifecycle.AgentEnrolled{
			RequestID: "r-agent-1-0", Agent: "claude", Enrolled: true,
		}}, 0),
		env(lifecycle.KindAgentEnrolled, lifecycle.Event{Kind: lifecycle.KindAgentEnrolled, AgentEnrolled: &lifecycle.AgentEnrolled{
			RequestID: "r-agent-1-0", Agent: "claude", Reason: "too many panes are already watched",
		}}, 0),
		env(lifecycle.KindAgentWithdraw, lifecycle.Event{Kind: lifecycle.KindAgentWithdraw, AgentWithdraw: &lifecycle.AgentWithdraw{
			RequestID: "r-agent-1-0",
		}}, 11),
		env(lifecycle.KindAgentWithdrawn, lifecycle.Event{Kind: lifecycle.KindAgentWithdrawn, AgentWithdrawn: &lifecycle.AgentWithdrawn{
			RequestID: "r-agent-1-0",
		}}, 0),
		// The declaration a wave participant sends, both verdicts. A failure
		// carries no summary here on purpose: the two halves are independent
		// fields and a decoder that only ever saw them together would not
		// prove either.
		env(lifecycle.KindAgentReport, lifecycle.Event{Kind: lifecycle.KindAgentReport, AgentReport: &lifecycle.AgentReport{
			RequestID: "r-agent-1-1", OK: true, Summary: "read AGENTS.md; nothing to change",
		}}, 12),
		env(lifecycle.KindAgentReport, lifecycle.Event{Kind: lifecycle.KindAgentReport, AgentReport: &lifecycle.AgentReport{
			RequestID: "r-agent-1-1",
		}}, 13),
		env(lifecycle.KindAgentReported, lifecycle.Event{Kind: lifecycle.KindAgentReported, AgentReported: &lifecycle.AgentReported{
			RequestID: "r-agent-1-1", Recorded: true,
		}}, 0),
		env(lifecycle.KindAgentReported, lifecycle.Event{Kind: lifecycle.KindAgentReported, AgentReported: &lifecycle.AgentReported{
			RequestID: "r-agent-1-1", Reason: "this pane is not part of a wave",
		}}, 0),
	}

	for _, want := range envs {
		var buf bytes.Buffer
		if _, err := Encode(&buf, want); err != nil {
			t.Fatalf("Encode(%s): %v", want.Event.Kind, err)
		}
		dec := NewDecoder(&buf, Config{}, nil)
		got, err := dec.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame(%s): %v", want.Event.Kind, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip %s:\n got %+v\nwant %+v", want.Event.Kind, got, want)
		}
	}
}

// TestTwoFramesAndCleanEOF proves two back-to-back frames decode one per
// ReadFrame call, and a stream that ends at a frame boundary yields io.EOF.
func TestTwoFramesAndCleanEOF(t *testing.T) {
	var buf bytes.Buffer
	_, _ = Encode(&buf, env(lifecycle.KindPromptReady, promptReadyEvt(), 1))
	_, _ = Encode(&buf, env(lifecycle.KindHello, helloEvt("zsh"), 1))
	dec := NewDecoder(&buf, Config{}, nil)

	first, err := dec.ReadFrame()
	if err != nil || first.Event.Kind != lifecycle.KindPromptReady {
		t.Fatalf("first frame: kind=%s err=%v", first.Event.Kind, err)
	}
	second, err := dec.ReadFrame()
	if err != nil || second.Event.Kind != lifecycle.KindHello {
		t.Fatalf("second frame: kind=%s err=%v", second.Event.Kind, err)
	}
	if _, err := dec.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("after last frame: want io.EOF, got %v", err)
	}
}

// TestOversizePrefixRejectedWithoutAllocating proves a length prefix above
// max_frame is refused before any body buffer exists: the prefix here claims
// 4 GiB, which an allocating decoder would try to allocate (and this test
// would die of). The decoder scans past it and delivers the next frame.
func TestOversizePrefixRejectedWithoutAllocating(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF}) // claims 4294967295 bytes
	_, _ = Encode(&buf, env(lifecycle.KindPromptReady, promptReadyEvt(), 1))

	var regions []gap
	dec := NewDecoder(&buf, Config{}, sink(&regions))
	got, err := dec.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame after oversize prefix: %v", err)
	}
	if got.Event.Kind != lifecycle.KindPromptReady {
		t.Fatalf("want the frame after the garbage, got kind %s", got.Event.Kind)
	}
	if len(regions) != 1 || regions[0].bytes != 4 {
		t.Fatalf("want one 4-byte gap for the oversize prefix, got %v", regions)
	}
}

// TestTruncatedFrameDoesNotBlockForever proves a frame whose body never
// arrives (the writer closes mid-frame) returns instead of blocking.
func TestTruncatedFrameDoesNotBlockForever(t *testing.T) {
	pr, pw := io.Pipe()
	dec := NewDecoder(pr, Config{}, nil)

	// The reader must be running before the writer: io.Pipe writes block
	// until a read consumes them.
	done := make(chan error, 1)
	go func() {
		_, err := dec.ReadFrame()
		done <- err
	}()

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 100) // claims 100 bytes
	_, _ = pw.Write(hdr[:])
	_, _ = pw.Write([]byte("0123456789")) // sends 10, then closes
	_ = pw.Close()

	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("want io.EOF for a truncated frame, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadFrame blocked forever on a truncated frame")
	}
}

// TestGarbageScannedPastAndReported proves arbitrary bytes are scanned past,
// bounded, and the skipped region reaches the gap sink so the kernel can
// enforce the desync budget (protocol §6: every skipped region is reported).
func TestGarbageScannedPastAndReported(t *testing.T) {
	garbage := "this is not a lifecycle frame at all, just bytes"
	var buf bytes.Buffer
	buf.WriteString(garbage)
	_, _ = Encode(&buf, env(lifecycle.KindPromptReady, promptReadyEvt(), 1))

	var regions []gap
	dec := NewDecoder(&buf, Config{}, sink(&regions))
	got, err := dec.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame after garbage: %v", err)
	}
	if got.Event.Kind != lifecycle.KindPromptReady {
		t.Fatalf("want the frame after the garbage, got kind %s", got.Event.Kind)
	}
	if len(regions) != 1 || regions[0].bytes != len(garbage) || regions[0].frames != 0 {
		t.Fatalf("want one gap of %d bytes, 0 frames; got %v", len(garbage), regions)
	}
}

// TestFrameFoundInsideGarbage proves the scan resynchronizes at byte
// granularity: the valid frame starts at offset 2 of the stream, inside what
// the first frame attempt consumed. A scanner that skipped by whole frame
// attempts would never find it.
func TestFrameFoundInsideGarbage(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("XY") // the frame's length prefix begins at offset 2
	_, _ = Encode(&buf, env(lifecycle.KindPromptReady, promptReadyEvt(), 1))

	var regions []gap
	dec := NewDecoder(&buf, Config{}, sink(&regions))
	got, err := dec.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Event.Kind != lifecycle.KindPromptReady {
		t.Fatalf("want the frame found inside the garbage, got kind %s", got.Event.Kind)
	}
	if len(regions) != 1 || regions[0].bytes != 2 {
		t.Fatalf("want one 2-byte gap, got %v", regions)
	}
}

// TestOversizeHelloIsGarbage proves a hello frame over the 1 KiB bound is
// scanned past as garbage while a following frame is still delivered.
func TestOversizeHelloIsGarbage(t *testing.T) {
	var buf bytes.Buffer
	_, _ = Encode(&buf, env(lifecycle.KindHello, helloEvt(strings.Repeat("x", 2048)), 1))
	_, _ = Encode(&buf, env(lifecycle.KindPromptReady, promptReadyEvt(), 2))

	var regions []gap
	dec := NewDecoder(&buf, Config{}, sink(&regions)) // default MaxHello = 1 KiB
	got, err := dec.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Event.Kind != lifecycle.KindPromptReady {
		t.Fatalf("want the frame after the oversize hello, got kind %s", got.Event.Kind)
	}
	if len(regions) != 1 || regions[0].frames != 1 {
		t.Fatalf("want one garbage frame reported, got %v", regions)
	}
}

// TestMalformedCapabilityIsGarbage proves a JSON frame the codec cannot map
// (a non-hex capability, an unknown event kind) is garbage: scanned past,
// not delivered, and the next frame still arrives.
func TestMalformedCapabilityIsGarbage(t *testing.T) {
	var buf bytes.Buffer
	writeRawFrame(&buf, `{"v":1,"lane":"l","dom":"d","epoch":1,"seq":1,"cap":"not-hex!","evt":"hello","shell":"bash"}`)
	writeRawFrame(&buf, `{"v":1,"lane":"l","dom":"d","epoch":1,"seq":1,"cap":"0000000000000000000000000000000000000000000000000000000000000000","evt":"teleport"}`)
	_, _ = Encode(&buf, env(lifecycle.KindPromptReady, promptReadyEvt(), 1))
	var regions []gap
	dec := NewDecoder(&buf, Config{}, sink(&regions))
	got, err := dec.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Event.Kind != lifecycle.KindPromptReady {
		t.Fatalf("want the valid frame after two unmappable ones, got kind %s", got.Event.Kind)
	}
	// The two unmappable frames are adjacent, so they form one contiguous
	// garbage region spanning two frame boundaries.
	if len(regions) != 1 || regions[0].frames != 2 {
		t.Fatalf("want one region of 2 garbage frames, got %v", regions)
	}
}

// TestScanByteBudgetExhausted proves the byte budget bounds a scan: a
// decoder with a 64-byte budget gives up on a longer garbage run with
// ErrScanBudgetExhausted instead of scanning forever.
func TestScanByteBudgetExhausted(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(bytes.Repeat([]byte("g"), 4096))
	dec := NewDecoder(&buf, Config{ScanBytes: 64}, nil)
	_, err := dec.ReadFrame()
	if !errors.Is(err, ErrScanBudgetExhausted) {
		t.Fatalf("want ErrScanBudgetExhausted, got %v", err)
	}
}

// TestScanFrameBudgetExhausted proves the frame budget bounds a scan of
// frame-shaped garbage: plausible prefixes whose bodies are not JSON.
func TestScanFrameBudgetExhausted(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 8; i++ {
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], 4)
		buf.Write(hdr[:])
		buf.WriteString("XXXX") // 4 bytes that are not JSON
	}
	dec := NewDecoder(&buf, Config{ScanFrames: 2}, nil)
	_, err := dec.ReadFrame()
	if !errors.Is(err, ErrScanBudgetExhausted) {
		t.Fatalf("want ErrScanBudgetExhausted, got %v", err)
	}
}

// TestEncodeRefusesOversizeFrame proves the encoder refuses to emit a frame
// beyond max_frame before writing anything.
func TestEncodeRefusesOversizeFrame(t *testing.T) {
	// Derived from the bound, never a literal: max_frame moved once already
	// (64 KiB → 256 KiB, nocx-beib) and a hard-coded 70 KiB turned this from
	// "refuses oversize" into "refuses a size that is now legal".
	env := env(lifecycle.KindHello, helloEvt(strings.Repeat("x", lifecycle.MaxFrameBytes+1024)), 1)
	var buf bytes.Buffer
	if _, err := Encode(&buf, env); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("oversize frame wrote %d bytes; want nothing", buf.Len())
	}
}

// TestMultipleGarbageRegionsProveEachRegionIsReportedSeparately: two garbage
// runs separated by a good frame produce two gap sink calls, so the kernel
// accumulates desync bytes across episodes of a continuous stream.
func TestMultipleGarbageRegionsReportedSeparately(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("garbage-one")
	_, _ = Encode(&buf, env(lifecycle.KindPromptReady, promptReadyEvt(), 1))
	buf.WriteString("garbage-two")
	_, _ = Encode(&buf, env(lifecycle.KindHello, helloEvt("bash"), 2))

	var regions []gap
	dec := NewDecoder(&buf, Config{}, sink(&regions))
	for i := 0; i < 2; i++ {
		if _, err := dec.ReadFrame(); err != nil {
			t.Fatalf("ReadFrame %d: %v", i, err)
		}
	}
	if len(regions) != 2 {
		t.Fatalf("want two reported regions, got %v", regions)
	}
	if regions[0].bytes != len("garbage-one") || regions[1].bytes != len("garbage-two") {
		t.Fatalf("region byte counts wrong: %v", regions)
	}
}

// TestDomainGrantBootstrapEscaping proves the grant's opaque bootstrap
// survives the wire byte-identical even when it carries the shell text that
// the shell-side extraction must not trip on: escaped quotes, backslashes,
// newlines and a payload near the frame bound. The bootstrap is the rcfile
// the child reads — a single mis-decoded byte is a corrupt rcfile and a
// silent conventional fallback — so the round trip is the contract.
func TestDomainGrantBootstrapEscaping(t *testing.T) {
	bootstrap := "saved=$(stty -g)\n" +
		`printf '%s\n' "a \"quoted\" value" '\' ` + "\n" +
		strings.Repeat("# line with 'quotes' and \\backslashes\\ and \t tabs\n", 500)
	want := env(lifecycle.KindDomainGrant, lifecycle.Event{Kind: lifecycle.KindDomainGrant, DomainGrant: &lifecycle.DomainGrant{
		RequestID: "r-dom-7-3", Env: "su",
		Domain: "dom-8", Epoch: 9,
		Bootstrap: bootstrap,
	}}, 0)

	var buf bytes.Buffer
	if _, err := Encode(&buf, want); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if buf.Len() > lifecycle.MaxFrameBytes {
		t.Fatalf("grant frame %d bytes exceeds the %d frame bound", buf.Len(), lifecycle.MaxFrameBytes)
	}
	dec := NewDecoder(&buf, Config{}, nil)
	got, err := dec.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grant round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// TestDomainRequestFieldPresence proves the optional request fields are
// absent when unset (a sudo request carries no host), so the shell's
// substring extraction of the grant never sees a spurious field.
func TestDomainRequestFieldPresence(t *testing.T) {
	want := env(lifecycle.KindDomainRequest, lifecycle.Event{Kind: lifecycle.KindDomainRequest, DomainRequest: &lifecycle.DomainRequest{
		RequestID: "r-dom-2-1", Env: "sudo",
	}}, 3)
	var buf bytes.Buffer
	if _, err := Encode(&buf, want); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	if strings.Contains(string(body), "host") || strings.Contains(string(body), "user") || strings.Contains(string(body), "port") {
		t.Fatalf("unset optional fields must be omitted from the wire, got %s", body)
	}
	dec := NewDecoder(&buf, Config{}, nil)
	got, err := dec.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// TestMaxFrameBytes_ShellsDeclareTheSameBound is the anti-drift check on the
// one number two sides have to agree about (nocx-beib).
//
// The shells announce max_frame in their hello and the Go side enforces
// lifecycle.MaxFrameBytes on every Encode. Nothing ties them together at
// build time, so a bump on one side is invisible: a frame the shell would
// accept is refused before it is written, or — worse — the backend writes
// one the shell will not read. The failure is silent in the direction that
// matters, which is how the ssh child came to hang for five seconds with no
// diagnostic: the grant simply never arrived.
func TestMaxFrameBytes_ShellsDeclareTheSameBound(t *testing.T) {
	want := strconv.Itoa(lifecycle.MaxFrameBytes)
	for _, name := range []string{"nocx.bash", "nocx.zsh"} {
		path := filepath.Join("..", "shellintegration", "scripts", name)
		body, err := os.ReadFile(path) // #nosec G304 — a repo-relative script path.
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		decl := "__nocx_lc_max_frame=" + want
		if !strings.Contains(string(body), decl) {
			t.Errorf("%s does not declare %s; the shell and lifecycle.MaxFrameBytes "+
				"must name the same bound or frames are refused on one side only", name, decl)
		}
		// And it must be the ONLY bound in the file. Raising the advertised
		// max_frame while a second literal still guarded the reader is what
		// made the ssh child's grant unreadable: the shell rejected a frame
		// the kernel was entitled to send, instantly and silently
		// (nocx-beib). Any bare 5-or-6-digit number next to a frame length
		// is that mistake coming back.
		for _, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, "__len") || strings.Contains(line, "__nocx_lc_max_frame") {
				continue
			}
			if regexp.MustCompile(`[0-9]{4,}`).MatchString(line) {
				t.Errorf("%s bounds a frame length against a literal, not the single "+
					"declaration: %s", name, strings.TrimSpace(line))
			}
		}
	}
}

// TestDomainRequestOptsSurviveTheWire guards nocx-c6z0's second half.
//
// This codec does not marshal the envelope structs; it maps them field by
// field onto a wire struct. So a field added to lifecycle.DomainRequest and
// not added HERE is silently dropped — which is exactly what happened: the
// shell collected the user's ssh options and sent them, the composer read
// req.Opts and found nothing, and the only visible symptom was an ssh
// sitting at a host-key prompt the user had passed -o to suppress. Every
// other test passed, because every other test builds the struct it asserts.
//
// The arguments here are the awkward ones on purpose — spaces, an equals
// sign, a percent, a quote — because they are what an ssh -o argument
// actually contains, and because the composer shell-quotes each token on
// the strength of it arriving as its own token.
func TestDomainRequestOptsSurviveTheWire(t *testing.T) {
	opts := []string{
		"-i", "/home/u/.ssh/id key",
		"-o", "ProxyCommand=nc -X 5 %h %p",
		"-o", "SetEnv=GREETING=it's",
		"-J", "bastion.example.com",
	}
	for _, tc := range []struct {
		name string
		want lifecycle.Envelope
		get  func(lifecycle.Envelope) []string
	}{
		{
			name: "request",
			want: env(lifecycle.KindDomainRequest, lifecycle.Event{
				Kind: lifecycle.KindDomainRequest,
				DomainRequest: &lifecycle.DomainRequest{
					RequestID: "r-dom-2-1", Env: "ssh", Host: "h", Opts: opts,
				},
			}, 3),
			get: func(e lifecycle.Envelope) []string { return e.Event.DomainRequest.Opts },
		},
		{
			// The grant echoes them back, and the echo is what the bootstrap
			// builder reads — the kernel keeps no request state of its own.
			name: "grant echo",
			want: env(lifecycle.KindDomainGrant, lifecycle.Event{
				Kind: lifecycle.KindDomainGrant,
				DomainGrant: &lifecycle.DomainGrant{
					RequestID: "r-dom-2-1", Env: "ssh", Host: "h", Opts: opts,
					Domain: "dom-child", Epoch: 4, Bootstrap: "ssh -t h true",
				},
			}, 3),
			get: func(e lifecycle.Envelope) []string { return e.Event.DomainGrant.Opts },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if _, err := Encode(&buf, tc.want); err != nil {
				t.Fatal(err)
			}
			got, err := NewDecoder(&buf, Config{}, nil).ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, tc.want)
			}
			// Named separately from DeepEqual so a drop reports as a drop
			// rather than as a struct diff nobody reads.
			if !reflect.DeepEqual(tc.get(got), opts) {
				t.Errorf("the options the user typed did not survive the wire:\n got %q\nwant %q",
					tc.get(got), opts)
			}
		})
	}
}

// The refusal must survive the wire as a refusal, and it must do so by being
// ABSENT rather than by being present and false. `enrolled` is omitted when it
// is false, so a shell reads consent by finding `"enrolled":true` in the frame
// and reads everything else — a truncated frame, a frame from an older backend,
// a frame a hostile writer half-composed — as "not orchestrated". A field that
// travelled as `"enrolled":false` would have to be parsed correctly to be
// refused correctly, which is the wrong way round for a fail-closed answer.
func TestAgentEnrolmentRefusalIsAbsenceOnTheWire(t *testing.T) {
	var yes, no bytes.Buffer
	if _, err := Encode(&yes, env(lifecycle.KindAgentEnrolled, lifecycle.Event{
		Kind:          lifecycle.KindAgentEnrolled,
		AgentEnrolled: &lifecycle.AgentEnrolled{RequestID: "r-0", Agent: "claude", Enrolled: true},
	}, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(&no, env(lifecycle.KindAgentEnrolled, lifecycle.Event{
		Kind:          lifecycle.KindAgentEnrolled,
		AgentEnrolled: &lifecycle.AgentEnrolled{RequestID: "r-0", Agent: "claude", Reason: "no grid available"},
	}, 0)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(yes.String(), `"enrolled":true`) {
		t.Errorf("consent must be on the wire as \"enrolled\":true, got %s", yes.String())
	}
	// And the match a reader must use is the FIELD, not the word: the event
	// kind is itself `agent_enrolled`, so anything grepping for the bare word
	// finds consent in every refusal. This caught the first draft of the test.
	if strings.Contains(no.String(), `"enrolled":`) {
		t.Errorf("a refusal must carry no enrolled field at all, got %s", no.String())
	}
	if !strings.Contains(no.String(), "no grid available") {
		t.Errorf("a refusal must carry its reason for the person reading it, got %s", no.String())
	}
}

// TestNoOutboundFrameCarriesBearerMaterial is the assertion nocx-aqz7o asks
// for, made where the bytes actually are: a REAL kernel is driven through
// every path that answers a shell, every envelope it produces is encoded with
// the production encoder, and the resulting frames are searched for the
// domain's capability.
//
// Why this test and not a reading of Encode. The exposure it guards was never
// a codec bug: Encode wrote whatever the envelope carried, the kernel's four
// outbound constructors carried the capability, and the two halves were
// correct separately. It is also the direction that reaches an actor the
// transport does not stop — the local channel is a socketpair whose child end
// is handed over with exec.Cmd.ExtraFiles, which clears FD_CLOEXEC, so every
// descendant of the shell holds a reader on it for the shell's whole life.
// ADR-0024 makes the capability mandatory rather than belt-and-braces for
// precisely that actor and states twice that it "cannot produce the
// capability"; while the accept echoed the capability back onto that
// descriptor in cleartext, the property did not hold by construction.
//
// It searches for the capability's HEX, its raw bytes and the `cap` key
// independently: the first two are what a descendant would grep for, and the
// third catches a future encoder that puts a placeholder there rather than
// nothing.
func TestNoOutboundFrameCarriesBearerMaterial(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	port := &captureKernelPort{}
	if err := k.BindTransport("T", port); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	h, err := k.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	if h.Capability == (lifecycle.Capability{}) {
		t.Fatal("the minted domain has no capability, so this test cannot look for one")
	}

	shell := func(seq uint64, evt lifecycle.Event) []lifecycle.Outbound {
		t.Helper()
		outs, ierr := k.Ingest("T", lifecycle.Envelope{
			Version: lifecycle.ProtocolVersion, Lane: "L", Domain: h.Domain,
			Epoch: h.Epoch, Sequence: seq, Capability: h.Capability, Event: evt,
		})
		if ierr != nil {
			t.Fatalf("Ingest(%s): %v", evt.Kind, ierr)
		}
		return outs
	}

	var outs []lifecycle.Outbound
	// hello → accept. The first frame of the handshake, and the one the
	// integration script reads before it will speak at all.
	outs = append(outs, shell(1, lifecycle.Event{
		Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"},
	})...)
	for _, out := range outs {
		if derr := k.Deliver(out); derr != nil {
			t.Fatalf("Deliver(%s): %v", out.Envelope.Event.Kind, derr)
		}
	}
	// domain_request → domain_grant, the answer a nested sudo/su/ssh reads.
	outs = append(outs, shell(2, lifecycle.Event{
		Kind: lifecycle.KindDomainRequest, DomainRequest: &lifecycle.DomainRequest{
			RequestID: "r-dom-0", Env: lifecycle.EnvSSH, Host: "box.example.com", User: "alice", Port: 22,
		},
	})...)
	// agent_enrol → agent_enrolled, and agent_withdraw → agent_withdrawn.
	outs = append(outs, shell(3, lifecycle.Event{
		Kind: lifecycle.KindAgentEnrol, AgentEnrol: &lifecycle.AgentEnrol{
			RequestID: "r-agent-0", Agent: "claude", Cols: 120, Rows: 40,
		},
	})...)
	outs = append(outs, shell(4, lifecycle.Event{
		Kind: lifecycle.KindAgentWithdraw, AgentWithdraw: &lifecycle.AgentWithdraw{RequestID: "r-agent-1"},
	})...)
	// A garbage region → refresh_request, the fourth and last kind the
	// kernel sends.
	refresh, gerr := k.NotifyGap("T", h.Domain, 64, 1)
	if gerr != nil {
		t.Fatalf("NotifyGap: %v", gerr)
	}
	outs = append(outs, refresh...)

	kinds := map[lifecycle.EventKind]bool{}
	for _, out := range outs {
		kinds[out.Envelope.Event.Kind] = true
	}
	for _, want := range []lifecycle.EventKind{
		lifecycle.KindAccept, lifecycle.KindDomainGrant, lifecycle.KindAgentEnrolled,
		lifecycle.KindAgentWithdrawn, lifecycle.KindRefreshRequest,
	} {
		if !kinds[want] {
			t.Fatalf("the kernel produced no %s, so this test does not cover it", want)
		}
	}

	capHex := hex.EncodeToString(h.Capability[:])
	for _, out := range outs {
		var frame bytes.Buffer
		if _, eerr := Encode(&frame, out.Envelope); eerr != nil {
			t.Fatalf("Encode(%s): %v", out.Envelope.Event.Kind, eerr)
		}
		body := frame.String()
		if strings.Contains(body, capHex) {
			t.Errorf("the %s frame carries the capability as hex: %s", out.Envelope.Event.Kind, body)
		}
		if bytes.Contains(frame.Bytes(), h.Capability[:]) {
			t.Errorf("the %s frame carries the capability as raw bytes", out.Envelope.Event.Kind)
		}
		if strings.Contains(body, `"cap"`) {
			t.Errorf("the %s frame carries a cap field at all: %s", out.Envelope.Event.Kind, body)
		}
		// And it is still addressed: what the shell identifies a frame by
		// are the names, and they must survive the removal.
		if !strings.Contains(body, `"dom":"`+string(h.Domain)+`"`) ||
			!strings.Contains(body, `"epoch":`+strconv.FormatUint(h.Epoch, 10)+`,`) {
			t.Errorf("the %s frame lost the addressing the shell identifies it by: %s", out.Envelope.Event.Kind, body)
		}
	}

	// The other direction is untouched: the shell still authenticates every
	// frame it sends, so a capability handed to Encode is still written.
	var inbound bytes.Buffer
	if _, eerr := Encode(&inbound, lifecycle.Envelope{
		Version: lifecycle.ProtocolVersion, Lane: "L", Domain: h.Domain, Epoch: h.Epoch,
		Sequence: 9, Capability: h.Capability,
		Event: lifecycle.Event{Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{}},
	}); eerr != nil {
		t.Fatalf("Encode(inbound): %v", eerr)
	}
	if !strings.Contains(inbound.String(), `"cap":"`+capHex+`"`) {
		t.Fatalf("an inbound frame must still carry the bearer, got %s", inbound.String())
	}
}

// captureKernelPort is the transport seam for the test above: the kernel
// delivers through it and it keeps nothing, because the assertion is made on
// the encoded frames rather than on the envelopes.
type captureKernelPort struct{}

func (*captureKernelPort) Send(lifecycle.Envelope) error { return nil }

// A frame that carries no verdict decodes to "not successful" and one that
// carries no answer decodes to "not recorded". Both fields are values rather
// than pointers precisely so a truncated or hostile frame cannot read as
// consent, and this is the assertion that keeps them that way.
func TestAnAgentReportWithNoVerdictDecodesAsUnsuccessful(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want func(lifecycle.Envelope) bool
	}{
		{
			name: "a report with no ok field",
			body: `{"v":1,"lane":"lane-1","dom":"dom-1","epoch":1,"seq":5,"cap":"` + strings.Repeat("a", 64) + `","evt":"agent_report","request":"r-agent-1-1"}`,
			want: func(e lifecycle.Envelope) bool { return e.Event.AgentReport != nil && !e.Event.AgentReport.OK },
		},
		{
			name: "an answer with no recorded field",
			body: `{"v":1,"lane":"lane-1","dom":"dom-1","epoch":1,"seq":6,"cap":"` + strings.Repeat("a", 64) + `","evt":"agent_reported","request":"r-agent-1-1"}`,
			want: func(e lifecycle.Envelope) bool {
				return e.Event.AgentReported != nil && !e.Event.AgentReported.Recorded
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeRawFrame(&buf, tc.body)
			dec := NewDecoder(&buf, Config{}, nil)
			got, err := dec.ReadFrame()
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !tc.want(got) {
				t.Fatalf("a frame with the field absent did not decode as a refusal: %+v", got.Event)
			}
		})
	}
}
