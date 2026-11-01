package shellintegration

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/waittest"
)

// nocx-40rlu. The refusal a REFUSED typed ssh actually meets on the wire is
// the codec's omission shape: an empty Bootstrap is dropped by
// `bootstrap,omitempty` (lifecyclecodec codec.go), so the grant frame carries
// no "bootstrap":" at all — not the `"bootstrap":""` the package's fake
// kernels have always sent. The shell's extraction is a # strip against that
// literal, which on an absent field returns the WHOLE FRAME, and only the ssh
// branch evals its grant in the parent shell, so the parent parses the
// frame's JSON as shell source. On the live pane this printed
// `(eval):1: parse error near ...' and the person's ssh never ran.
//
// These tests answer the production shape: the REAL codec encodes the
// refusal, the fake kernel sends those exact bytes, and the shell must run
// the user's line conventionally exactly once with no shell diagnostic.

// encodeRealFrame renders an envelope with the production codec and returns
// the frame BODY — the JSON bytes without the codec's own 4-byte length
// prefix, which the caller writes with its own.
func encodeRealFrame(t *testing.T, env lifecycle.Envelope) []byte {
	t.Helper()
	var body bytes.Buffer
	if _, err := lifecyclecodec.Encode(&body, env); err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	raw := body.Bytes()
	if len(raw) < 4 {
		t.Fatalf("codec frame too short: %d bytes", len(raw))
	}
	return raw[4:]
}

// writeRealRefusalGrant arms the fake kernel to answer every domain_request
// with the REAL codec's encoding of a grant whose Bootstrap is empty — the
// omission shape, because the field is `omitempty` on the wire.
func writeRealRefusalGrant(t *testing.T, k *fakeKernel, env string) {
	t.Helper()
	frame := encodeRealFrame(t, lifecycle.Envelope{
		Version: lifecycle.ProtocolVersion,
		Lane:    lifecycle.LaneID(testLane),
		Domain:  lifecycle.DomainID(testDom),
		Epoch:   testEpoch,
		Event: lifecycle.Event{Kind: lifecycle.KindDomainGrant, DomainGrant: &lifecycle.DomainGrant{
			RequestID: "PENDING", // replaced with the shell's own request id
			Env:       env,
		}},
	})
	k.onEvent = func(conn net.Conn, ev kernelEvent) {
		if ev.Evt != "domain_request" {
			return
		}
		rid, _ := ev.Body["request"].(string)
		patched := strings.Replace(string(frame), `"request":"PENDING"`,
			fmt.Sprintf(`"request":%q`, rid), 1)
		var hdr [4]byte
		// #nosec G115 — a grant frame, far under the protocol cap.
		binary.BigEndian.PutUint32(hdr[:], uint32(len(patched)))
		_, _ = conn.Write(append(hdr[:], []byte(patched)...))
	}
}

// writeStubSSH puts a stub ssh on PATH that reports it ran. It is a file a
// shell must FIND and RUN, so it carries the exec bit; 0o700 is already
// owner-only and it lives in a t.TempDir that goes away with the test.
func writeStubSSH(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	stub := filepath.Join(binDir, "ssh")
	// #nosec G306 — see the comment above.
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf 'STUB-SSH-RAN\\n'\n"), 0o700); err != nil {
		t.Fatalf("write stub ssh: %v", err)
	}
	return binDir
}

// TestZshNested_AnOmittedBootstrapRefusalRunsTheUsersCommand is the
// production-shape refusal on the zsh tier: the grant carries no bootstrap
// FIELD, the user's ssh must run exactly once, and no parse error may cross
// the pane.
func TestZshNested_AnOmittedBootstrapRefusalRunsTheUsersCommand(t *testing.T) {
	k := newFakeKernel(t, testCap)
	writeRealRefusalGrant(t, k, "ssh")
	s := startChannelShellCfg(t, "zsh", "nocx.zsh", zshScript, k, "", true)
	defer s.close()

	s.run("export PATH=" + writeStubSSH(t) + ":$PATH")

	promptsBefore := k.count("prompt_ready")
	if _, err := s.ptmx.Write([]byte("ssh refused.example.com\n")); err != nil {
		t.Fatalf("write the ssh line: %v", err)
	}
	waittest.WaitForTimeoutDetail(t, "the prompt after the omitted-bootstrap ssh refusal", 15*time.Second,
		func() string {
			return fmt.Sprintf("want %d, have %d; output=%q",
				promptsBefore+1, k.count("prompt_ready"), s.output())
		},
		func() bool { return k.count("prompt_ready") >= promptsBefore+1 },
	)

	out := s.output()
	if n := strings.Count(out, "STUB-SSH-RAN"); n != 1 {
		t.Errorf("the user's ssh ran %d times, want exactly 1.\npty:\n%s", n, out)
	}
	if strings.Contains(out, "parse error") {
		t.Errorf("the refusal leaked a shell diagnostic into the pane:\n%s", out)
	}
}

// TestBashNested_AnOmittedBootstrapRefusalRunsTheUsersCommand is the bash
// twin: the same omission-shaped refusal through the real generated script,
// on every bash this product must work on (the forEachBash matrix).
func TestBashNested_AnOmittedBootstrapRefusalRunsTheUsersCommand(t *testing.T) {
	forEachBash(t, testBashNestedOmittedBootstrapRefusal)
}

func testBashNestedOmittedBootstrapRefusal(t *testing.T, shell string) {
	k := newFakeKernel(t, testCap)
	writeRealRefusalGrant(t, k, "ssh")
	s := startChannelShellCfg(t, shell, "nocx.bash", bashScript, k, "", true)
	defer s.close()

	s.run("export PATH=" + writeStubSSH(t) + ":$PATH")

	promptsBefore := k.count("prompt_ready")
	if _, err := s.ptmx.Write([]byte("ssh refused.example.com\n")); err != nil {
		t.Fatalf("write the ssh line: %v", err)
	}
	waittest.WaitForTimeoutDetail(t, "the prompt after the omitted-bootstrap ssh refusal", 15*time.Second,
		func() string {
			return fmt.Sprintf("want %d, have %d; output=%q",
				promptsBefore+1, k.count("prompt_ready"), s.output())
		},
		func() bool { return k.count("prompt_ready") >= promptsBefore+1 },
	)

	out := s.output()
	if n := strings.Count(out, "STUB-SSH-RAN"); n != 1 {
		t.Errorf("the user's ssh ran %d times, want exactly 1.\npty:\n%s", n, out)
	}
	if strings.Contains(out, "command not found") || strings.Contains(out, "parse error") {
		t.Errorf("the refusal leaked a shell diagnostic into the pane:\n%s", out)
	}
}
