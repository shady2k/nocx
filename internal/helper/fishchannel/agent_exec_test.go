package fishchannel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAgentExecChildProcess(t *testing.T) {
	if os.Getenv("NOCX_AGENT_EXEC_TEST_CHILD") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	args := []string{"--data-fd", "3"}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	code, err := ExecAgent(ctx, args)
	stop()
	if err != nil {
		_, _ = fmt.Fprint(os.Stderr, err)
	}
	os.Exit(code)
}

type execObservation struct {
	PID          int
	Args         []string
	Input        string
	Env          map[string]string
	DataClosed   bool
	SentinelOpen bool
	SavedClosed  bool
}

func TestAgentExecTarget(t *testing.T) {
	if os.Getenv("NOCX_AGENT_EXEC_TEST_TARGET") != "1" {
		return
	}
	if os.Getenv("NOCX_AGENT_EXEC_TEST_SIGNAL") == "1" {
		_ = unix.Kill(os.Getpid(), unix.SIGTERM)
		select {}
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	if err != nil {
		os.Exit(99)
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	var dataStat unix.Stat_t
	dataErr := unix.Fstat(3, &dataStat)
	// Go may reuse a closed fd while initializing the exec target. Require
	// the actual request pipe to be gone, not a permanently vacant number.
	dataInode := os.Getenv("NOCX_AGENT_EXEC_TEST_DATA_INODE")
	dataClosed := dataErr == unix.EBADF || strconv.FormatUint(dataStat.Ino, 10) != dataInode
	_, sentinelErr := unix.FcntlInt(4, unix.F_GETFD, 0)
	var savedStat unix.Stat_t
	savedErr := unix.Fstat(5, &savedStat)
	savedClosed := savedErr == unix.EBADF || strconv.FormatUint(savedStat.Ino, 10) != os.Getenv("NOCX_AGENT_EXEC_TEST_SAVED_INODE")
	observation := execObservation{
		PID: os.Getpid(), Args: args, Input: string(input),
		Env: map[string]string{}, DataClosed: dataClosed, SentinelOpen: sentinelErr == nil,
		SavedClosed: savedClosed,
	}
	for _, name := range []string{"fish_trace", "status", "__nocx_private", "key with spaces", "PATH", "NOCX_AGENT_EXEC_TEST_INHERITED"} {
		observation.Env[name] = os.Getenv(name)
	}
	_ = json.NewEncoder(os.Stdout).Encode(observation)
	_, _ = io.WriteString(os.Stderr, "target-stderr")
	os.Exit(23)
}

func agentData(argv []string, overrides map[string]string) []byte {
	fields := []string{"/argv", "array", strconv.Itoa(len(argv))}
	for i, value := range argv {
		fields = append(fields, "/argv/"+strconv.Itoa(i), "string", value)
	}
	fields = append(fields, "/env", "object", "")
	for name, value := range overrides {
		escaped := strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
		fields = append(fields, "/env/"+escaped, "string", value)
	}
	return triples(fields...)
}

func startExecChild(t *testing.T, data []byte, stdin string, extra ...*os.File) *child {
	t.Helper()
	return startExecChildArgs(t, data, stdin, []string{"--data-fd", "3"}, extra...)
}

func startExecChildArgs(t *testing.T, data []byte, stdin string, args []string, extra ...*os.File) *child {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	argv := append([]string{"-test.run=^TestAgentExecChildProcess$", "--"}, args...)
	c := &child{cmd: exec.CommandContext(ctx, os.Args[0], argv...)} // #nosec G204 — executes this test binary with fixture-owned child arguments to observe real exec behavior.
	c.cmd.Env = append(os.Environ(), "NOCX_AGENT_EXEC_TEST_CHILD=1", "NOCX_AGENT_EXEC_TEST_INHERITED=inherited")
	var dataStat unix.Stat_t
	if err := unix.Fstat(int(reader.Fd()), &dataStat); err != nil {
		t.Fatal(err)
	}
	c.cmd.Env = append(c.cmd.Env, "NOCX_AGENT_EXEC_TEST_DATA_INODE="+strconv.FormatUint(dataStat.Ino, 10))
	c.cmd.ExtraFiles = append([]*os.File{reader}, extra...)
	c.cmd.Stdin = strings.NewReader(stdin)
	c.cmd.Stdout, c.cmd.Stderr = &c.out, &c.err
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	go func() {
		_, _ = writer.Write(data)
		_ = writer.Close()
	}()
	return c
}

func TestAgentExecPreservesPIDStdioAndTargetOnlyEnvironment(t *testing.T) {
	sentinel, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sentinel.Close() }()
	argv := []string{os.Args[0], "-test.run=^TestAgentExecTarget$", "--", "", "two words", "魚\n'\"$()"}
	data := agentData(argv, map[string]string{
		"NOCX_AGENT_EXEC_TEST_TARGET": "1", "fish_trace": "sensitive-trace", "status": "readonly-in-fish",
		"__nocx_private": "ordinary-config-value", "key with spaces": "literal-value", "PATH": "/target-only-path",
	})
	c := startExecChild(t, data, "original-agent-stdin\n", sentinel)
	pid := c.cmd.Process.Pid
	c.wait(t, 23)
	var got execObservation
	if err := json.Unmarshal(c.out.Bytes(), &got); err != nil {
		t.Fatalf("target output = %q (%v)", c.out.Bytes(), err)
	}
	if got.PID != pid || got.Input != "original-agent-stdin\n" || !got.DataClosed || !got.SentinelOpen {
		t.Fatalf("exec changed PID/stdio/descriptors: %+v", got)
	}
	if !equalStrings(got.Args, argv[3:]) || c.err.String() != "target-stderr" {
		t.Fatal("exec changed argv, status or stderr")
	}
	for name, want := range map[string]string{
		"fish_trace": "sensitive-trace", "status": "readonly-in-fish", "__nocx_private": "ordinary-config-value",
		"key with spaces": "literal-value", "PATH": "/target-only-path", "NOCX_AGENT_EXEC_TEST_INHERITED": "inherited",
	} {
		if got.Env[name] != want {
			t.Fatalf("target environment %q changed", name)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAgentExecUsesTargetPATHNotOriginalPATH(t *testing.T) {
	dir := t.TempDir()
	name := "nocx-agent-target-path-only"
	if err := os.Symlink(os.Args[0], filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	c := startExecChild(t, agentData([]string{name, "-test.run=^TestAgentExecTarget$", "--", "from-target-path"}, map[string]string{
		"PATH": dir, "NOCX_AGENT_EXEC_TEST_TARGET": "1",
	}), "")
	c.wait(t, 23)
	var got execObservation
	if err := json.Unmarshal(c.out.Bytes(), &got); err != nil || !equalStrings(got.Args, []string{"from-target-path"}) || got.Env["PATH"] != dir {
		t.Fatal("target PATH was not used")
	}
	// The helper's ambient PATH contains the running Go test executable's
	// directory on some hosts; an explicitly absent target must still fail.
	c = startExecChild(t, agentData([]string{name}, map[string]string{"PATH": t.TempDir()}), "")
	c.wait(t, 127)
	if c.out.Len() != 0 || strings.Contains(c.err.String(), name) {
		t.Fatal("lookup failure exposed request data")
	}
}

func TestAgentExecPreservesSymlinkPATHComponents(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "physical", "release")
	bin := filepath.Join(root, "physical", "bin")
	visible := filepath.Join(root, "visible")
	for _, dir := range []string{release, bin, visible} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(visible, "current")
	if err := os.Symlink(release, link); err != nil {
		t.Fatal(err)
	}
	name := "symlink-path-agent"
	if err := os.Symlink(os.Args[0], filepath.Join(bin, name)); err != nil {
		t.Fatal(err)
	}
	// The filesystem must resolve the symlink before "..", as native exec does.
	path := link + "/../bin"
	c := startExecChild(t, agentData([]string{name, "-test.run=^TestAgentExecTarget$", "--", "symlink-path"}, map[string]string{
		"PATH": path, "NOCX_AGENT_EXEC_TEST_TARGET": "1",
	}), "")
	c.wait(t, 23)
	var got execObservation
	if err := json.Unmarshal(c.out.Bytes(), &got); err != nil || !equalStrings(got.Args, []string{"symlink-path"}) || got.Env["PATH"] != path {
		t.Fatal("symlink-containing target PATH changed executable selection")
	}
}

func TestAgentExecMalformedRequestsAndExecFailuresAreSanitized(t *testing.T) {
	for _, data := range [][]byte{
		triples("/argv", "array", "0", "/env", "object", ""),
		triples("/argv", "array", "1", "/argv/0", "number", "1", "/env", "object", ""),
		triples("/argv", "array", "1", "/argv/0", "string", "", "/env", "object", ""),
		triples("/argv", "array", "1", "/argv/0", "string", "/never-print", "/env", "object", "", "/env/bad=name", "string", "secret"),
		triples("/argv", "array", "1", "/argv/0", "string", "/never-print", "/env", "object", "", "/env/x", "bool", "true"),
		triples("/argv", "array", "1", "/argv/0", "string", "/never-print", "/env", "object", "", "/extra", "string", "secret"),
		[]byte("unfinished-never-print"),
	} {
		c := startExecChild(t, data, "")
		c.wait(t, 1)
		if c.out.Len() != 0 || strings.Contains(c.err.String(), "never-print") || strings.Contains(c.err.String(), "secret") {
			t.Fatal("invalid exec request leaked")
		}
	}
	path := filepath.Join(t.TempDir(), "never-print")
	if err := os.WriteFile(path, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := startExecChild(t, agentData([]string{path}, nil), "")
	c.wait(t, 126)
	if strings.Contains(c.err.String(), path) {
		t.Fatal("exec failure exposed executable path")
	}
}

func TestAgentRequestDoesNotMutateParentEnvironment(t *testing.T) {
	before := os.Getenv("fish_trace")
	root, err := parseTriples(agentData([]string{"agent"}, map[string]string{"fish_trace": "never-export-to-parent", "PATH": "/target"}))
	if err != nil {
		t.Fatal(err)
	}
	inherited := []string{"PATH=/ambient", "fish_trace=old", "KEEP=present"}
	original := append([]string(nil), inherited...)
	_, env, err := agentRequest(root, inherited)
	if err != nil || !equalStrings(inherited, original) || os.Getenv("fish_trace") != before {
		t.Fatal("request mutated parent environment")
	}
	if !equalStrings(env, []string{"KEEP=present", "PATH=/target", "fish_trace=never-export-to-parent"}) {
		t.Fatalf("environment merge = %q", env)
	}
}

func TestAgentExecCancellationClosesOnlyDataDescriptor(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	fd := int(reader.Fd())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	code, _ := ExecAgent(ctx, []string{"--data-fd", strconv.Itoa(fd)})
	if code != 130 {
		t.Fatalf("canceled exec = %d", code)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != unix.EBADF {
		t.Fatal("data descriptor leaked")
	}
	if _, err := unix.FcntlInt(writer.Fd(), unix.F_GETFD, 0); err != nil {
		t.Fatal("exec closed another descriptor")
	}
}

func TestAgentExecTargetSignalIsNotTranslated(t *testing.T) {
	c := startExecChild(t, agentData([]string{os.Args[0], "-test.run=^TestAgentExecTarget$"}, map[string]string{
		"NOCX_AGENT_EXEC_TEST_TARGET": "1", "NOCX_AGENT_EXEC_TEST_SIGNAL": "1",
	}), "")
	err := c.cmd.Wait()
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("signal result = %v", err)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("signal translated to status: %v", exit.Sys())
	}
}

func TestAgentExecInputBound(t *testing.T) {
	c := startExecChild(t, bytes.Repeat([]byte{'x'}, maxInputBytes+1), "")
	c.wait(t, 1)
}

func TestAgentExecUsageAndNonPipePreserveDescriptors(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--fd", "3"},
		{"--data-fd", "0"},
		{"--data-fd", "-1"},
		{"--data-fd", "2147483648"},
		{"--data-fd", "3", "extra"},
		{"--data-fd", "3", "--stdin-fd", "3"},
		{"--data-fd", "3", "--stdin-fd", "0"},
		{"--data-fd", "3", "--stdin-fd", "-1"},
		{"--data-fd", "3", "--wrong-fd", "5"},
		{"--data-fd", "1", "--stdin-fd", "5"},
		{"--data-fd", "2", "--stdin-fd", "5"},
	} {
		if code, _ := ExecAgent(context.Background(), args); code != 2 {
			t.Fatalf("bad usage returned %d", code)
		}
	}
	socket, _ := socketPair(t)
	fd := int(socket.Fd())
	if code, _ := ExecAgent(context.Background(), []string{"--data-fd", strconv.Itoa(fd)}); code != 1 {
		t.Fatal("non-pipe data fd accepted")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
		t.Fatal("rejected data fd closed the lifecycle socket")
	}
}

func TestAgentExecRestoresExplicitOriginalStdin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "original-stdin")
	if err := os.WriteFile(path, []byte("saved-original-input\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(path) // #nosec G304 — opens the original-stdin fixture just written under this test's t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = original.Close() }()
	var savedStat unix.Stat_t
	if statErr := unix.Fstat(int(original.Fd()), &savedStat); statErr != nil {
		t.Fatal(statErr)
	}
	sentinel, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sentinel.Close() }()
	data := agentData([]string{os.Args[0], "-test.run=^TestAgentExecTarget$"}, map[string]string{
		"NOCX_AGENT_EXEC_TEST_TARGET":      "1",
		"NOCX_AGENT_EXEC_TEST_SAVED_INODE": strconv.FormatUint(savedStat.Ino, 10),
	})
	c := startExecChildArgs(t, data, "not-original-input", []string{"--data-fd", "3", "--stdin-fd", "5"}, sentinel, original)
	pid := c.cmd.Process.Pid
	c.wait(t, 23)
	var got execObservation
	if err := json.Unmarshal(c.out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Input != "saved-original-input\n" || got.PID != pid || !got.DataClosed || !got.SavedClosed || !got.SentinelOpen {
		t.Fatalf("explicit stdin restoration changed input/PID/descriptors: %+v", got)
	}
	if c.err.String() != "target-stderr" {
		t.Fatal("stdin restoration changed stderr")
	}
}

func TestAgentExecAcceptsRequestOnStdinWithReplacement(t *testing.T) {
	original, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = original.Close() }()
	if _, writeErr := io.WriteString(writer, "original-pipeline-input\n"); writeErr != nil {
		t.Fatal(writeErr)
	}
	_ = writer.Close()
	var savedStat unix.Stat_t
	if statErr := unix.Fstat(int(original.Fd()), &savedStat); statErr != nil {
		t.Fatal(statErr)
	}
	sentinel, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sentinel.Close() }()
	data := agentData([]string{os.Args[0], "-test.run=^TestAgentExecTarget$"}, map[string]string{
		"NOCX_AGENT_EXEC_TEST_TARGET":      "1",
		"NOCX_AGENT_EXEC_TEST_SAVED_INODE": strconv.FormatUint(savedStat.Ino, 10),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c := &child{cmd: exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentExecChildProcess$", "--", "--data-fd", "0", "--stdin-fd", "5")} // #nosec G204 — executes this test binary with fixed child arguments to observe real stdin descriptor restoration.
	c.cmd.Env = append(os.Environ(), "NOCX_AGENT_EXEC_TEST_CHILD=1")
	c.cmd.Stdin = bytes.NewReader(data)
	c.cmd.ExtraFiles = []*os.File{sentinel, sentinel, original}
	c.cmd.Stdout, c.cmd.Stderr = &c.out, &c.err
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.cmd.Process.Pid
	c.wait(t, 23)
	var got execObservation
	if err := json.Unmarshal(c.out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Input != "original-pipeline-input\n" || got.PID != pid || !got.SavedClosed || !got.SentinelOpen {
		t.Fatalf("stdin delivery/restoration changed input/PID/descriptors: %+v", got)
	}
	if c.err.String() != "target-stderr" {
		t.Fatal("stdin delivery changed stderr")
	}
}
