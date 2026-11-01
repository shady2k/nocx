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
	entrypoint, _, err := fishNativeConfig(fish, nil)
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

// waitFishReaderPrompt ties a native reader B to a particular prompt's A.
// prompt_ready precedes Fish's keyboard-protocol setup; a cumulative B count
// can still belong to the previous prompt. readPump ingests terminal modes
// before appending output, so observing this B also establishes encoder state.
func waitFishReaderPrompt(t *testing.T, s *channelShell, after int) int {
	t.Helper()
	if after < 0 {
		t.Fatal("the completed Fish command has no prompt start")
	}
	const promptStart = "\x1b]133;A\a"
	const readerReady = "\x1b]133;B\x1b\\"
	var end int
	waittest.WaitForTimeoutDetail(t, "this Fish prompt's native reader boundary", 10*time.Second,
		func() string {
			return fmt.Sprintf("after=%d events=%v output=%q", after, s.kernel.events(), s.output())
		},
		func() bool {
			out := s.output()
			start := strings.Index(out[after:], promptStart)
			if start < 0 {
				return false
			}
			start += after + len(promptStart)
			boundary := strings.Index(out[start:], readerReady)
			if boundary < 0 {
				return false
			}
			end = start + boundary + len(readerReady)
			return true
		})
	return end
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
	t.Logf("native Fish interrupt encoding: %q", data)
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
	// Establish idle input before testing idle Ctrl+C, not the protocol-disabled
	// prompt-construction phase. Keep the following Ctrl+C-to-command gap empty.
	promptEnd := waitFishReaderPrompt(t, s, strings.LastIndex(s.output(), "\x1b]133;A\a"))
	ready := s.kernel.count("prompt_ready")
	if _, err := s.ptmx.Write([]byte{'\r'}); err != nil {
		t.Fatal(err)
	}
	waittest.WaitForTimeoutDetail(t, "blank Fish input returns ready", 10*time.Second,
		func() string { return fmt.Sprintf("events=%v output=%q", s.kernel.events(), s.output()) },
		func() bool { return s.kernel.count("prompt_ready") > ready })
	waitFishReaderPrompt(t, s, promptEnd)
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

// Only the executable's public discovery response is doubled. The launcher
// still decides admission and constructs (or refuses) every private descriptor.
func fishDiscoveryFixture(t *testing.T, response string) string {
	t.Helper()
	shell := filepath.Join(t.TempDir(), "fish")
	body := "#!/bin/sh\n[ \"$1\" = --no-config ] && [ \"$2\" = --command ] && [ \"$#\" = 3 ] || exit 91\n" +
		"printf '%s' " + ShellQuote(response) + "\n"
	if err := os.WriteFile(shell, []byte(body), 0o700); err != nil { //nolint:gosec // executable fixture owned by t.TempDir
		t.Fatal(err)
	}
	return shell
}

func TestFishStartupRefusalNamesCompatibilityWithoutPrivateDescriptors(t *testing.T) {
	startup := filepath.Join(t.TempDir(), "config.fish")
	if err := os.WriteFile(startup, []byte("# incompatible disk startup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, response, version string
	}{
		{"older", "4.8.0\ne# different native prelude\n", "4.8.0"},
		{"older disk startup", "4.0.0\np" + startup, "4.0.0"},
		{"modified audited version", "4.9.3\ne__fish_theme_migrate\n", "4.9.3"},
		{"unknown version", "\ne# unknown native prelude\n", ""},
		{"unsafe version", "4.8.0\x1b[31m\ne# unknown native prelude\n", ""},
		{"discovery unavailable", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shell := fishDiscoveryFixture(t, tc.response)
			opts := localTestOpts()
			opts.AgentHelperPath = "/public/helper"
			opts.AgentToolToken = "private-tool-token"
			launch, err := LocalEnhancedLaunchInMemory(shell, ShellFish, opts)
			if err != nil {
				t.Fatal(err)
			}
			if launch.Refusal != ReasonUnsupportedFishStartup || launch.ShellVersion != tc.version {
				t.Fatalf("refusal/version = %q/%q, want unsupported-fish-startup/%q", launch.Refusal, launch.ShellVersion, tc.version)
			}
			if launch.Command != shell || len(launch.Args) != 1 || launch.Args[0] != "-i" {
				t.Fatalf("fallback changed the native shell: %q %q", launch.Command, launch.Args)
			}
			if len(launch.ExtraFiles) != 0 || len(launch.Env) != 0 || launch.Cleanup != nil || launch.Abort != nil {
				t.Fatal("an unsupported startup layout received private launch resources")
			}
		})
	}
}

func TestNativeFish_DiscoverySkipsUserStartupAndVersionDoesNotAuthorizePrelude(t *testing.T) {
	fish, home, opts := nativeFishFixture(t, `builtin printf ran > "$HOME/user-startup-ran"
function fish_greeting; end
`)
	config, version, err := fishNativeConfig(fish, opts.EnvOverrides)
	if err != nil || version == "" || !fishPreludeSupported(config) {
		t.Fatalf("native discovery = version %q, error %v", version, err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "user-startup-ran")); !os.IsNotExist(statErr) {
		t.Fatalf("public discovery ran user startup: %v", statErr)
	}
	// The audited bytes remain admissible even when the executable reports
	// another version. Conversely, its real version cannot admit altered bytes.
	compatible := fishDiscoveryFixture(t, "99.0.0\ne"+config)
	launch, err := LocalEnhancedLaunchInMemory(compatible, ShellFish, opts)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Abort != nil {
		defer launch.Abort()
	}
	if launch.Refusal != ReasonNone || len(launch.ExtraFiles) != 1 {
		t.Fatal("a version number overrode the audited prelude gate")
	}
	incompatible := fishDiscoveryFixture(t, version+"\neecho changed\n"+config)
	launch, err = LocalEnhancedLaunchInMemory(incompatible, ShellFish, opts)
	if err != nil || launch.Refusal != ReasonUnsupportedFishStartup || len(launch.ExtraFiles) != 0 {
		t.Fatalf("changed prelude was admitted: refusal=%q, error=%v", launch.Refusal, err)
	}
	// Run the real Fish with exactly the conventional fallback's arguments.
	// It must retain normal user startup and accept an ordinary command.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, fish, append(launch.Args, "--command", "builtin printf 'CONVENTIONAL_FISH_WORKS\\n'")...) //nolint:gosec // real shell and fixed authored command
	cmd.Env = append(os.Environ(), opts.EnvOverrides...)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "CONVENTIONAL_FISH_WORKS") {
		t.Fatalf("conventional Fish was not usable: %v, %q", err, out)
	}
	if data, readErr := os.ReadFile(filepath.Join(home, "user-startup-ran")); readErr != nil || string(data) != "ran" { //nolint:gosec // fixed fixture marker under nativeFishFixture's t.TempDir
		t.Fatalf("conventional Fish lost native user startup: %q, %v", data, readErr)
	}
}

// A SUBCOMMAND THE RECORD MARKS AS NOT A LAUNCH REACHES THE CLI UNCHANGED,
// in Fish too (nocx-3an1s.1.3, GitHub #541). Fish's wrapper is built by
// fish_agents.go rather than authored in the shell script, so this is the
// third implementation that must read the same record answer: `fish-agent
// update` gets the person's argv and no --mcp-config, and a plain
// `fish-agent` still gets the staged tool surface.
func TestNativeFish_NonLaunchSubcommandKeepsTheTypedArgv(t *testing.T) {
	fish, home, opts := nativeFishFixture(t, `function fish_greeting; end
function fish_prompt; printf 'NATIVE_FISH> '; end
`)
	opts.Agents = []string{"fish-agent"}
	opts.AgentToolSocketPath = filepath.Join(t.TempDir(), "tool.sock")
	result := filepath.Join(t.TempDir(), "args")
	// The record's program writes the argv it received, to a file per kind of
	// invocation: the first argument is the person's only one here, so it is
	// also what tells the two runs apart.
	program := `out="$RESULT"
case "$1" in
  update) out="$RESULT.maintenance" ;;
  *) out="$RESULT.surface" ;;
esac
printf '%s\n' "$@" > "$out"`
	payload, err := EncodeAgentLaunchPayload([]string{"/bin/sh", "-c", program, "fish-agent"}, []string{"RESULT=" + result})
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
			reply["local"], reply["tool_surface"] = true, true
			// What the shipped Claude record's nonLaunchSubcommands arrive as.
			reply["non_launch_subcommands"] = "update"
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

	s.run("fish-agent update")
	assertFishExit(t, s, 0)
	maintenance, err := os.ReadFile(result + ".maintenance") //nolint:gosec // this test's own temp directory
	if err != nil {
		t.Fatalf("the maintenance subcommand did not run: %v (events=%v output=%q)", err, k.events(), s.output())
	}
	if got := strings.Split(strings.TrimSpace(string(maintenance)), "\n"); len(got) != 1 || got[0] != "update" {
		t.Fatalf("maintenance argv = %q, want exactly the typed word", got)
	}
	if k.count("agent_enrol") != 0 {
		t.Fatalf("a maintenance subcommand was enrolled as an agent launch: %v", k.events())
	}
	if _, statErr := os.Stat(result + ".surface"); !os.IsNotExist(statErr) {
		t.Fatal("the maintenance subcommand was reported as a tool-surface launch")
	}

	s.run("fish-agent")
	assertFishExit(t, s, 0)
	surface, err := os.ReadFile(result + ".surface") //nolint:gosec // this test's own temp directory
	if err != nil {
		t.Fatalf("the plain launch did not run: %v (events=%v output=%q)", err, k.events(), s.output())
	}
	argv := strings.Split(strings.TrimSpace(string(surface)), "\n")
	if len(argv) != 2 || argv[0] != "--mcp-config" || !strings.HasSuffix(argv[1], "/mcp.json") {
		t.Fatalf("plain launch argv = %q, want --mcp-config and the staged file", argv)
	}
	if k.count("agent_enrol") != 1 {
		t.Fatalf("the plain launch was not enrolled exactly once: %v", k.events())
	}
}
