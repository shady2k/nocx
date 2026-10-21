package shellintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/fishchannel"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/waittest"
)

// The fixture executes the real helper adapter in a child, not a mock codec.
// Its private environment markers are removed before any configured target exec.
func TestMain(m *testing.M) {
	if os.Getenv("NOCX_TEST_FISH_ADAPTER") == "1" {
		_ = os.Unsetenv("NOCX_TEST_FISH_ADAPTER")
		// GORACE is parsed before TestMain. Disable only the fixture binary's
		// artificial exit sleep, then restore the caller's exact environment.
		if original, restore := os.LookupEnv("NOCX_TEST_FISH_ADAPTER_GORACE"); restore {
			_ = os.Unsetenv("NOCX_TEST_FISH_ADAPTER_GORACE")
			if value, present := strings.CutPrefix(original, "x"); present {
				_ = os.Setenv("GORACE", value)
			} else {
				_ = os.Unsetenv("GORACE")
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		args := os.Args[1:]
		code := 2
		var err error
		if len(args) > 0 {
			switch args[0] {
			case "fish-channel":
				code, err = fishchannel.Run(ctx, args[1:], os.Stdin, os.Stdout)
			case "agent-exec":
				code, err = fishchannel.ExecAgent(ctx, args[1:])
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		stop()
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func nativeFishFixture(t *testing.T, config string) (string, string, LaunchOptions) {
	t.Helper()
	fish, err := exec.LookPath("fish")
	if err != nil {
		if os.Getenv("NOCX_REQUIRE_NATIVE_FISH") != "" {
			t.Fatal("the native-Fish acceptance shell is required")
		}
		t.Skip("fish is not installed")
	}
	entrypoint, err := fishNativeConfig(fish, nil)
	if err != nil || !fishPreludeSupported(entrypoint) {
		if os.Getenv("NOCX_REQUIRE_NATIVE_FISH") != "" {
			t.Fatal("the installed Fish has no verified native startup adapter")
		}
		t.Skip("the installed Fish has no verified native startup adapter")
	}
	home := filepath.Join(t.TempDir(), "fish's home")
	configDir := filepath.Join(home, ".config", "fish")
	if mkdirErr := os.MkdirAll(filepath.Join(configDir, "conf.d"), 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := os.WriteFile(filepath.Join(configDir, "config.fish"), []byte(config), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "helper")
	// Each successful -race adapter exit otherwise sleeps for one second.
	// A configured invocation needs eight such exits before its completion,
	// unlike the shipped helper. Keep race reporting and all caller options;
	// only the child runtime's shutdown delay changes, never the target env.
	body := "#!/bin/sh\nNOCX_TEST_FISH_ADAPTER_GORACE=\"${GORACE+x}${GORACE-}\" GORACE=\"${GORACE-} atexit_sleep_ms=0\" NOCX_TEST_FISH_ADAPTER=1 exec " + ShellQuote(testBinary) + " \"$@\"\n"
	if writeErr := os.WriteFile(helper, []byte(body), 0o700); writeErr != nil { //nolint:gosec // executable test adapter, owned by t.TempDir
		t.Fatal(writeErr)
	}
	opts := localTestOpts()
	opts.AgentHelperPath = helper
	opts.EnvOverrides = []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"FISH_UNIT_TESTS_RUNNING=1",
	}
	return fish, home, opts
}

func waitFishOutput(t *testing.T, s *channelShell, text string) {
	t.Helper()
	waittest.WaitForTimeoutDetail(t, "Fish output "+text, 10*time.Second,
		func() string { return fmt.Sprintf("events=%v output=%q", s.kernel.events(), s.output()) },
		func() bool { return strings.Contains(s.output(), text) })
}

func assertFishExit(t *testing.T, s *channelShell, want int) {
	t.Helper()
	var start, complete kernelEvent
	for _, event := range s.kernel.events() {
		if event.Evt == "start" {
			start = event
		}
		if event.Evt == "complete" {
			complete = event
		}
	}
	if complete.Body["exit_code"] != float64(want) || complete.Body["attempt"] != start.Body["attempt"] {
		t.Fatalf("start/complete = %+v / %+v, want matching attempt with exit %d", start, complete, want)
	}
}

func sendFishInterrupt(t *testing.T, s *channelShell) {
	t.Helper()
	// Fish negotiates Kitty at a prompt and disables it for ordinary jobs.
	// Use the same encoder as the product, not a hard-coded legacy ETX.
	data, err := s.terminal.EncodeKey(emulator.KeyEvent{Key: emulator.KeyC, Mods: emulator.Mods{Ctrl: true}, Text: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ptmx.Write(data); err != nil {
		t.Fatal(err)
	}
}

func TestNativeFish_CommandLifecycleAndIdleInput(t *testing.T) {
	fish, home, opts := nativeFishFixture(t, `function fish_greeting; end
function fish_prompt; printf 'NATIVE_FISH> '; end
function localfishfunc; printf 'FISH_VALUE:%s\n' "$argv[1]"; end
function рыба; end
function 'semi;colon'; end
`)
	s := startLocalPane(t, fish, ShellFish, home, opts)
	defer s.close()
	waitFishOutput(t, s, "\x1b]133;B\x07")
	// Inspect the inventory before typing its function name, so input echo
	// cannot satisfy the assertion. This is the session-local half, not PATH.
	waittest.WaitForTimeoutDetail(t, "Fish session-local command inventory", 10*time.Second,
		func() string { return s.output() },
		func() bool {
			return s.hasSessionCommand("localfishfunc") && s.hasSessionCommand("рыба") && s.hasSessionCommand("semi;colon")
		})
	cwd := filepath.Join(home, "directory with spaces")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	s.run("begin\nset value 'рыба'; localfishfunc \"$value\"; cd " + fishQuote(cwd) + "; false\nend")
	waitFishOutput(t, s, "FISH_VALUE:рыба")
	assertFishExit(t, s, 1)
	if s.kernel.count("start") != 1 || s.kernel.count("complete") != 1 {
		t.Fatalf("one entered line must make one attempt: %v", s.kernel.events())
	}
	cwdMarks := regexp.MustCompile("\x1b\\]7;([^\x07]+)\x07")
	wantCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	waittest.WaitForTimeoutDetail(t, "the actual Fish cwd observation", 10*time.Second,
		func() string { return s.output() },
		func() bool {
			for _, mark := range cwdMarks.FindAllStringSubmatch(s.output(), -1) {
				u, err := url.Parse(mark[1])
				if err == nil && u.Scheme == "file" {
					path, err := filepath.EvalSymlinks(u.Path)
					if err == nil && path == wantCwd {
						return true
					}
				}
			}
			return false
		})
	s.run("printf 'SECOND_%s\\n' COMMAND")
	waitFishOutput(t, s, "SECOND_COMMAND")
	assertFishExit(t, s, 0)
	ready := s.kernel.count("prompt_ready")
	boundary := s.promptBoundaryCount()
	if _, err := s.ptmx.Write([]byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	waittest.WaitForTimeoutDetail(t, "blank Fish input returns ready", 10*time.Second,
		func() string { return fmt.Sprintf("events=%v output=%q", s.kernel.events(), s.output()) },
		func() bool { return s.kernel.count("prompt_ready") > ready && s.promptBoundaryCount() > boundary })
	sendFishInterrupt(t, s)
	// Native Fish repaints its cached prompt on idle Ctrl+C, without a new
	// fish_prompt event. A real next command is the processing barrier.
	s.run("builtin printf 'AFTER_IDLE_%s\\n' FISH")
	waitFishOutput(t, s, "AFTER_IDLE_FISH")
	if s.kernel.count("start") != 3 || s.kernel.count("complete") != 3 {
		t.Fatalf("blank Enter or idle Ctrl+C invented an attempt: %v", s.kernel.events())
	}
	completed := s.kernel.count("complete")
	if _, err := s.ptmx.Write([]byte("command sh -c 'printf \"FISH_RUN%s\\n\" NING; read -r value'\n")); err != nil {
		t.Fatal(err)
	}
	waitFishOutput(t, s, "FISH_RUNNING")
	sendFishInterrupt(t, s)
	waittest.WaitForTimeoutDetail(t, "the running Fish command is interrupted", 10*time.Second,
		func() string { return fmt.Sprintf("events=%v output=%q", s.kernel.events(), s.output()) },
		func() bool { return s.kernel.count("complete") > completed })
	s.waitForPromptAfterComplete()
	s.waitForRenderedThroughPrompt()
	assertFishExit(t, s, 130)
	s.run("builtin printf 'AFTER_INTERRUPT_%s\\n' FISH")
	waitFishOutput(t, s, "AFTER_INTERRUPT_FISH")
	assertFishExit(t, s, 0)
}

func TestNativeFish_CancellingAContinuationReturnsAReadyPrompt(t *testing.T) {
	fish, home, opts := nativeFishFixture(t, `function fish_greeting; end
function fish_prompt; printf 'NATIVE_FISH> '; end
`)
	s := startLocalPane(t, fish, ShellFish, home, opts)
	defer s.close()
	waitFishOutput(t, s, "\x1b]133;B\x07")
	ready := s.kernel.count("prompt_ready")
	if _, err := s.ptmx.Write([]byte("begin\r")); err != nil {
		t.Fatal(err)
	}
	waitFishOutput(t, s, "begin")
	sendFishInterrupt(t, s)
	waittest.WaitForTimeoutDetail(t, "cancelled Fish continuation returns ready", 10*time.Second,
		func() string { return fmt.Sprintf("events=%v output=%q", s.kernel.events(), s.output()) },
		func() bool { return s.kernel.count("prompt_ready") > ready })
	if s.kernel.count("start") != 0 || s.kernel.count("complete") != 0 {
		t.Fatalf("a cancelled parser continuation invented an execution: %v", s.kernel.events())
	}
	s.run("builtin printf 'AFTER_CANCEL_%s\\n' FISH")
	waitFishOutput(t, s, "AFTER_CANCEL_FISH")
	assertFishExit(t, s, 0)
	if s.kernel.count("start") != 1 || s.kernel.count("complete") != 1 {
		t.Fatalf("the next command did not have its own single execution: %v", s.kernel.events())
	}
}

func TestNativeFish_CancellingABuiltinReaderDoesNotEndTheAttemptEarly(t *testing.T) {
	fish, home, opts := nativeFishFixture(t, `function fish_greeting; end
function fish_prompt; printf 'NATIVE_FISH> '; end
`)
	s := startLocalPane(t, fish, ShellFish, home, opts)
	defer s.close()
	waitFishOutput(t, s, "\x1b]133;B\x07")
	if _, err := s.ptmx.Write([]byte("read --prompt-str (builtin printf 'READ_%s> ' ACTIVE) value\n")); err != nil {
		t.Fatal(err)
	}
	waitFishOutput(t, s, "READ_ACTIVE> ")
	sendFishInterrupt(t, s)
	waittest.WaitForTimeoutDetail(t, "the native Fish reader completes after cancellation", 10*time.Second,
		func() string { return fmt.Sprintf("events=%v output=%q", s.kernel.events(), s.output()) },
		func() bool { return s.kernel.count("complete") == 1 })
	s.waitForPromptAfterComplete()
	s.waitForRenderedThroughPrompt()
	started := false
	exitCode := 0
	for _, event := range s.kernel.events() {
		switch event.Evt {
		case "start":
			started = true
		case "prompt_ready":
			if started {
				t.Fatal("a reader inside a running command claimed the top-level prompt")
			}
		case "complete":
			started = false
			code, ok := event.Body["exit_code"].(float64)
			if !ok {
				t.Fatal("the native reader completion omitted its numeric exit code")
			}
			exitCode = int(code)
		}
	}
	if exitCode == 0 {
		t.Fatal("the cancelled native reader reported success")
	}
	s.run("builtin printf 'READ_EXIT_%s\\n' \"$status\"")
	waitFishOutput(t, s, fmt.Sprintf("READ_EXIT_%d", exitCode))
}

func TestNativeFish_PreservesUniversalsAndProtectsStartupPipe(t *testing.T) {
	fish, home, opts := nativeFishFixture(t, `function fish_greeting; end
function fish_prompt; printf 'NATIVE_FISH> '; end
function changed --on-variable nocx_shared_value
    printf 'UNIVERSAL_CHANGED:%s\n' "$nocx_shared_value"
end
command dd if=/dev/fd/3 of="$HOME/startup-read" bs=4096 count=1 2>/dev/null
command env > "$HOME/startup-environment"
set -g nocx_startup_tty no
if test -t 0; set -g nocx_startup_tty yes; end
`)
	opts.AgentToolToken = strings.Repeat("ac", 32)
	prime := exec.Command(fish, "--command", "set --universal nocx_shared_value before; set --universal fish_trace 1") //nolint:gosec // required native Fish, fixed fixture-only program
	prime.Env = append(cleanEnv(), opts.EnvOverrides...)
	if out, err := prime.CombinedOutput(); err != nil {
		t.Fatalf("prime native universal state: %v\n%s", err, out)
	}
	s := startLocalPane(t, fish, ShellFish, home, opts)
	defer s.close()
	s.run("printf 'FISH_FIDELITY:%s:%s\\n' \"$nocx_shared_value\" \"$nocx_startup_tty\"")
	waitFishOutput(t, s, "FISH_FIDELITY:before:yes")
	read, err := os.ReadFile(filepath.Join(home, "startup-read")) //nolint:gosec // reading back this test's isolated startup fixture
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 0 {
		t.Fatal("a user-startup descendant obtained private bootstrap bytes")
	}
	startupEnv, err := os.ReadFile(filepath.Join(home, "startup-environment")) //nolint:gosec // reading back this test's isolated startup fixture
	if err != nil {
		t.Fatal(err)
	}
	output := s.output()
	for _, secret := range []string{testCap, opts.Recovery, opts.AgentToolToken} {
		if strings.Contains(string(startupEnv), secret) || strings.Contains(output, secret) {
			t.Fatal("a private startup credential reached a descendant environment or traced terminal")
		}
	}
	change := exec.Command(fish, "--command", "set --universal nocx_shared_value after") //nolint:gosec // fixed fixture-only universal update
	change.Env = append(cleanEnv(), opts.EnvOverrides...)
	if out, err := change.CombinedOutput(); err != nil {
		t.Fatalf("update shared native universal state: %v\n%s", err, out)
	}
	// Do not run a command in the integrated Fish: external execution can
	// accidentally force a reread and conceal a broken native notifier.
	waitFishOutput(t, s, "UNIVERSAL_CHANGED:after")
}

func TestNativeFish_NoAcceptRetainsAWorkingNativePrompt(t *testing.T) {
	for _, fault := range []string{"no-accept", "wrong-lane", "partial-header"} {
		t.Run(fault, func(t *testing.T) {
			fish, home, opts := nativeFishFixture(t, `function fish_greeting; end
function fish_prompt; printf 'VISIBLE_NATIVE_FISH> '; end
`)
			kernel := newFakeKernel(t, testCap)
			kernel.answerHello = false
			kernel.onEvent = func(conn net.Conn, event kernelEvent) {
				if event.Evt != "hello" {
					return
				}
				switch fault {
				case "wrong-lane":
					body, err := json.Marshal(map[string]any{
						"v": 1, "lane": "other-lane", "dom": testDom, "epoch": testEpoch, "seq": 0, "evt": "accept",
					})
					if err != nil {
						t.Error(err)
						return
					}
					if _, err := lifecyclecodec.WriteRawFrame(conn, body); err != nil {
						t.Error(err)
					}
				case "partial-header":
					_, _ = conn.Write([]byte{0, 0, 1})
					_ = conn.Close()
				}
			}
			s := startLocalPaneMode(t, fish, ShellFish, home, opts, localPaneOptions{kernel: kernel})
			defer s.close()
			waitFishOutput(t, s, "VISIBLE_NATIVE_FISH> ")
			if _, err := s.ptmx.Write([]byte("printf 'NATIVE_%s\\n' WORKS\n")); err != nil {
				t.Fatal(err)
			}
			waitFishOutput(t, s, "NATIVE_WORKS")
			if kernel.count("prompt_ready") != 0 || kernel.count("start") != 0 || kernel.count("complete") != 0 {
				t.Fatalf("a shell without acceptance claimed integration: %v", kernel.events())
			}
		})
	}
}

func TestNativeFish_RefreshRetainsRealStatusAndLossRestoresPrompt(t *testing.T) {
	fish, home, opts := nativeFishFixture(t, `function fish_greeting; end
function fish_prompt; printf 'RECOVERED_NATIVE_FISH> '; end
`)
	kernel := newFakeKernel(t, testCap)
	s := startLocalPaneMode(t, fish, ShellFish, home, opts, localPaneOptions{kernel: kernel, waitHandshake: true})
	defer s.close()
	s.run("false")
	const request = "req-1234567890abcdef"
	kernel.sendRefresh(request)
	if _, err := s.ptmx.Write([]byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	waittest.WaitForTimeoutDetail(t, "Fish refresh with its actual last completion", 10*time.Second,
		func() string { return fmt.Sprintf("events=%v output=%q", kernel.events(), s.output()) },
		func() bool {
			for _, event := range kernel.events() {
				if event.Evt != "snapshot" || event.Body["request"] != request {
					continue
				}
				last, ok := event.Body["last_completed"].(map[string]any)
				nextSeq, hasNextSeq := event.Body["next_seq"].(float64)
				if ok && hasNextSeq && last["exit_code"] == float64(1) && nextSeq > float64(event.Seq) {
					return true
				}
			}
			return false
		})
	kernel.mu.Lock()
	conn := kernel.conn
	kernel.mu.Unlock()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ptmx.Write([]byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	waitFishOutput(t, s, "RECOVERED_NATIVE_FISH> ")
	marker := "\x1b]1337;NOCX_RECOVERY;" + opts.Recovery + "\x07"
	waitFishOutput(t, s, marker)
	if _, err := s.ptmx.Write([]byte("printf 'AFTER_%s\\n' LOSS\n")); err != nil {
		t.Fatal(err)
	}
	waitFishOutput(t, s, "AFTER_LOSS")
	if strings.Count(s.output(), marker) != 1 {
		t.Fatal("Fish emitted more than one recovery acknowledgement for the lost epoch")
	}
}

func TestFishCommentStrippingPreservesQuotedProgramData(t *testing.T) {
	fish := requireShell(t, "fish")
	program := stripShellComments("printf '%s\\n' 'it\\'s # data' # remove this comment\n", ShellFish)
	out, err := exec.Command(fish, "--no-config", "--command", program).CombinedOutput() //nolint:gosec // actual shell consuming the lexical output
	if err != nil || string(out) != "it's # data\n" {
		t.Fatalf("quoted Fish program data changed: %v %q", err, out)
	}
}

func TestNativeFish_ConfiguredAgentKeepsInputEnvironmentAndExitStatus(t *testing.T) {
	fish, home, opts := nativeFishFixture(t, `function fish_greeting; end
function fish_prompt; printf 'NATIVE_FISH> '; end
`)
	opts.Agents = []string{"fish-agent", "math", "status", "fish_prompt", "prompt_pwd", "ssh", "__nocx_lc_send"}
	opts.AgentToolToken = strings.Repeat("ab", 32)
	// A deterministic fixture regression: inherited race options must neither
	// delay every adapter shutdown nor be overwritten in the real target.
	opts.EnvOverrides = append(opts.EnvOverrides, "GORACE=atexit_sleep_ms=20000")
	result := filepath.Join(home, "agent-result")
	program := `IFS= read -r value || exit 61
[ "$value" = AGENT_INPUT ] || exit 62
[ "$fish_trace" = target-only ] || exit 63
[ "$status" = reserved-name ] || exit 64
[ "$__nocx_agent_exec_argv" = configured-value ] || exit 65
[ "$1" = "arg's space" ] || exit 66
[ -z "${__nocx_cap+x}${__nocx_recovery+x}${__nocx_agent_token+x}${NOCX_TEST_FISH_ADAPTER+x}${NOCX_TEST_FISH_ADAPTER_GORACE+x}" ] || exit 67
[ "$GORACE" = atexit_sleep_ms=20000 ] || exit 68
printf 'REAL_AGENT:%s\n' "$value" > "$RESULT"
exit 17`
	payload, err := EncodeAgentLaunchPayload([]string{"/bin/sh", "-c", program, "configured-agent"}, []string{
		"fish_trace=target-only", "status=reserved-name", "__nocx_agent_exec_argv=configured-value", "RESULT=" + result,
	})
	if err != nil {
		t.Fatal(err)
	}
	k := newFakeKernel(t, testCap)
	k.onEvent = func(conn net.Conn, event kernelEvent) {
		reply := map[string]any{
			"v": 1, "lane": testLane, "dom": testDom, "epoch": testEpoch, "seq": 0,
			"request": event.Body["request"],
		}
		switch event.Evt {
		case "agent_launch_resolve":
			reply["evt"], reply["agent"] = "agent_launch_resolved", event.Body["agent"]
			reply["local"], reply["tool_surface"] = true, false
			reply["ticket"], reply["payload"] = strings.Repeat("A", 43), payload
		case "agent_enrol":
			reply["evt"], reply["agent"], reply["enrolled"] = "agent_enrolled", event.Body["agent"], true
		case "agent_withdraw":
			reply["evt"] = "agent_withdrawn"
		default:
			return
		}
		body, marshalErr := json.Marshal(reply)
		if marshalErr != nil {
			t.Error(marshalErr)
			return
		}
		if _, writeErr := lifecyclecodec.WriteRawFrame(conn, body); writeErr != nil {
			t.Error(writeErr)
		}
	}
	s := startLocalPaneMode(t, fish, ShellFish, home, opts, localPaneOptions{kernel: k, waitHandshake: true})
	defer s.close()
	s.run(`builtin printf 'AGENT_%s\n' INPUT | fish-agent "arg's space"`)
	assertFishExit(t, s, 17)
	data, err := os.ReadFile(result) //nolint:gosec // reading the configured fixture target's t.TempDir output
	if err != nil || string(data) != "REAL_AGENT:AGENT_INPUT\n" {
		t.Fatalf("configured target did not consume its original input: %q, %v", data, err)
	}
	if k.count("agent_enrol") != 1 || k.count("agent_withdraw") != 1 {
		t.Fatalf("agent lifetime was not enrolled and withdrawn: %v", k.events())
	}
	s.run("builtin printf 'NATIVE_MATH:%s\\n' (math 6 + 1)")
	waitFishOutput(t, s, "NATIVE_MATH:7")
	assertFishExit(t, s, 0)
	s.run("status is-interactive")
	assertFishExit(t, s, 0)
	if k.count("agent_enrol") != 1 {
		t.Fatalf("a native Fish builtin invoked the configured agent: %v", k.events())
	}
	s.run("builtin printf 'SHELL_AFTER_%s\\n' AGENT")
	waitFishOutput(t, s, "SHELL_AFTER_AGENT")
	assertFishExit(t, s, 0)
}

func TestNativeFish_NestedDomainReturnsToTheOriginalFish(t *testing.T) {
	fish, home, opts := nativeFishFixture(t, `function fish_greeting; end
function fish_prompt; printf 'NATIVE_FISH> '; end
`)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(preservingSudoFixture()), 0o700); err != nil { //nolint:gosec // executable sudo fixture owned by t.TempDir
		t.Fatal(err)
	}
	opts.EnvOverrides = append(opts.EnvOverrides, "PATH="+bin+":"+os.Getenv("PATH"))
	k := newNestedKernel(t)
	s := startLocalPaneMode(t, fish, ShellFish, home, opts, localPaneOptions{kernel: k, waitHandshake: true})
	defer s.close()
	driveNestedHappyInterval(t, s, k, "sudo -i", "")
	s.run("status is-interactive; and builtin printf 'FISH_RETURN%s\\n' ED")
	waitFishOutput(t, s, "FISH_RETURNED")
	assertFishExit(t, s, 0)
	if strings.Contains(s.output(), nestedChildCap) || strings.Contains(s.output(), testCap) {
		t.Fatal("nested credentials reached terminal output")
	}
}
