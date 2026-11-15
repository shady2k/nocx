package shellintegration

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestBashStartupTracePlacesHandshakeAndFirstPrompt(t *testing.T) {
	sh := requireShell(t, "bash")
	home := t.TempDir()
	trace := filepath.Join(home, "startup.trace")
	if err := os.WriteFile(trace, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	k := newFakeKernel(t, testCap)
	k.answerHello = false
	hello := make(chan net.Conn, 1)
	k.onEvent = func(conn net.Conn, ev kernelEvent) {
		if ev.Evt == "hello" {
			hello <- conn
		}
	}
	go k.serveLoop(ln)
	script := writeScriptFile(t, "nocx.bash", bashScript)
	rc := bashRcfile(localChild, launcherEnvBlock(LaunchOptions{
		Enhanced: true, Capability: testCap, SessionID: "startup-trace", Lane: testLane,
		Domain: testDom, Epoch: testEpoch, LifecyclePort: tcpPort(t, ln),
	}), ". "+ShellQuote(script), capabilityLiteral(bashUnsetExport, testCap, ""))
	rcPath := writeScriptFile(t, "trace-rc", rc)
	// #nosec G204 -- requireShell resolves the installed test shell; rcPath is our fixture.
	cmd := exec.Command(sh, "--rcfile", rcPath, "-i")
	cmd.Env = append(cleanEnv("HOME="+home, "TERM=xterm", "HISTFILE=/dev/null"), "NOCX_STARTUP_TRACE="+trace)
	master, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = master.Close() }()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := master.Read(buf); err != nil {
				return
			}
		}
	}()
	phases := func() string {
		// #nosec G304 -- trace is a private file created under t.TempDir above.
		b, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	wait := func(phase string) {
		t.Helper()
		waittest.WaitFor(t, "startup phase "+phase, func() bool { return strings.Contains(phases(), " "+phase+"\n") })
	}
	var conn net.Conn
	select {
	case conn = <-hello:
	case <-time.After(5 * time.Second):
		t.Fatal("no hello")
	}
	wait("accept_read_begin")
	if strings.Contains(phases(), "accept_header_complete") {
		t.Fatal("header completed before peer sent it")
	}
	body := []byte(fmt.Sprintf(`{"v":1,"lane":%q,"dom":%q,"epoch":1,"seq":0,"evt":"accept"}`, testLane, testDom))
	var hdr [4]byte
	// #nosec G115 -- body contains only the fixed fixture identity constants above.
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := conn.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	wait("accept_header_complete")
	if strings.Contains(phases(), "accept_body_complete") {
		t.Fatal("body completed before peer sent it")
	}
	if _, err := conn.Write(body); err != nil {
		t.Fatal(err)
	}
	wait("first_prompt_entered")
	got := phases()
	ordered := []string{"accept_read_begin", "accept_header_read_begin", "accept_header_complete", "accept_body_read_begin", "accept_body_complete", "accept_read_complete", "source_returned", "first_prompt_entered"}
	last := -1
	for _, phase := range ordered {
		pos := strings.Index(got, " "+phase+"\n")
		if pos <= last {
			t.Fatalf("phase %s out of order: %s", phase, got)
		}
		last = pos
	}
	if strings.Contains(got, testCap) || strings.Contains(got, testDom) {
		t.Fatal("trace exposed lifecycle data")
	}
	if _, err := master.Write([]byte("true\n")); err != nil {
		t.Fatal(err)
	}
	waittest.WaitFor(t, "second prompt", func() bool { return k.count("prompt_ready") >= 2 })
	if phases() != got {
		t.Fatalf("startup trace repeated after startup: %s", phases())
	}
}

func TestBashStartupTraceDoesNotCreateFilesOrRecordUnknownPhases(t *testing.T) {
	sh := requireShell(t, "bash")
	home := t.TempDir()
	trace := filepath.Join(home, "startup.trace")
	script := writeScriptFile(t, "nocx.bash", bashScript)
	run := func(phase string) {
		t.Helper()
		// #nosec G204 -- resolved test shell and quoted private script/fixed test phases.
		cmd := exec.Command(sh, "-c", ". "+ShellQuote(script)+"; __nocx_startup_trace "+ShellQuote(phase))
		cmd.Env = append(cleanEnv("HOME="+home), "NOCX_SHELL_INTEGRATION=1", "NOCX_STARTUP_TRACE="+trace)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("trace helper: %v: %s", err, out)
		}
	}
	run("accept_read_begin")
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatalf("trace created a file without opt-in preparation: %v", err)
	}
	if err := os.WriteFile(trace, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	run("secret-command-text")
	// #nosec G304 -- trace is a private file created under t.TempDir above.
	got, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("trace recorded data outside phase vocabulary: %q", got)
	}
}
