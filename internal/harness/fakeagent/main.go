// The fake agent is one program speaking a trivial JSON-lines protocol on
// stdio. The conformance suite scripts it: stream text, emit reasoning, call a
// tool through the turn's bridge, hang, fail, or exit early leaving a child.
//
// It is test support and is built by the suite with `go build`. It deliberately
// does not import the harness contract: it is an agent, not a connector, and
// what it knows is its own wire.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-child" {
		// A child the turn left behind: acknowledge readiness before blocking.
		block(func() { _, _ = fmt.Fprintln(os.Stdout, "ready") })
	}

	stdin := bufio.NewReader(os.Stdin)
	line, err := stdin.ReadBytes('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeagent: prompt: %v\n", err)
		os.Exit(2)
	}
	var prompt protocol.Prompt
	if err := json.Unmarshal(line, &prompt); err != nil {
		fmt.Fprintf(os.Stderr, "fakeagent: prompt: %v\n", err)
		os.Exit(2)
	}

	var script protocol.Script
	if err := json.Unmarshal([]byte(os.Getenv("FAKEAGENT_SCRIPT")), &script); err != nil {
		fmt.Fprintf(os.Stderr, "fakeagent: script: %v\n", err)
		os.Exit(2)
	}

	// The parent's death closes this process's stdin, and that is how the
	// conformance suite proves no agent outlives the backend. A program
	// blocked in a tool call cannot notice the pipe by reading its own
	// protocol, so the watchdog owns the fact. A script may turn it off so a
	// check can prove Cancel stops the group itself.
	if !script.IgnoreStdinEOF {
		go func() {
			_, _ = io.Copy(io.Discard, stdin)
			if script.CompleteOnEOF {
				os.Exit(0)
			}
			os.Exit(1)
		}()
	}
	if script.PIDFile != "" {
		_ = os.WriteFile(script.PIDFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}

	bridge := prompt.Tools
	if script.PromptFile != "" {
		prompt.Dir, _ = os.Getwd()
		raw, _ := json.Marshal(prompt)
		_ = os.WriteFile(script.PromptFile, raw, 0o600)
	}

	out := json.NewEncoder(os.Stdout)
	emit := func(line protocol.Line) {
		_ = out.Encode(line)
	}

	calls := 0
	for _, step := range script.Steps {
		switch step.Kind {
		case protocol.StepAnswer:
			emit(protocol.Line{Kind: "answer", Text: step.Text})
		case protocol.StepReasoning:
			emit(protocol.Line{Kind: "reasoning", Text: step.Text})
		case protocol.StepBridge:
			cmd := exec.Command(bridge.Command[0], bridge.Command[1:]...) //nolint:gosec // fixture bridge argv
			cmd.Env = append(os.Environ(), bridge.Env...)
			input, err := cmd.StdinPipe()
			if err != nil {
				os.Exit(2)
			}
			if err := cmd.Start(); err != nil {
				os.Exit(2)
			}
			defer func() { _ = input.Close(); _ = cmd.Wait() }()
		case protocol.StepTool:
			calls++
			id := "call-" + strconv.Itoa(calls)
			emit(protocol.Line{Kind: "tool_started", ID: id, Name: step.Name})
			result, err := callTool(bridge, step.Name, step.Arguments)
			if err != nil {
				emit(protocol.Line{Kind: "failure", Reason: fmt.Sprintf("tool call failed: %v", err)})
				os.Exit(1)
			}
			if script.ResultFile != "" {
				_ = os.WriteFile(script.ResultFile, []byte(result), 0o600)
			}
			emit(protocol.Line{Kind: "tool_finished", ID: id, Name: step.Name})
		case protocol.StepEnd:
			model := step.Model
			if model == "" {
				model = prompt.Model
			}
			if model == "" {
				model = "fake-default"
			}
			emit(protocol.Line{Kind: "end", Model: model, Usage: step.Usage})
		case protocol.StepFail:
			emit(protocol.Line{Kind: "failure", Reason: step.Reason})
		case protocol.StepGate:
			gate, err := os.Open(script.Gate)
			if err != nil {
				os.Exit(2)
			}
			var b [1]byte
			_, _ = gate.Read(b[:])
			_ = gate.Close()
		case protocol.StepHang:
			block(func() {
				if script.HangFile != "" {
					_ = os.WriteFile(script.HangFile, []byte("blocked"), 0o600)
				}
			})
		case protocol.StepExit:
			os.Exit(step.Code)
		case protocol.StepChild:
			self, err := os.Executable()
			if err != nil {
				emit(protocol.Line{Kind: "failure", Reason: fmt.Sprintf("cannot find my own program: %v", err)})
				os.Exit(1)
			}
			child := exec.Command(self, "-child") //nolint:gosec // this program's own path
			childOut, err := child.StdoutPipe()
			if err != nil {
				os.Exit(2)
			}
			child.Stderr = nil
			if err := child.Start(); err != nil {
				emit(protocol.Line{Kind: "failure", Reason: fmt.Sprintf("cannot start the child: %v", err)})
				os.Exit(1)
			}
			if _, err := bufio.NewReader(childOut).ReadString('\n'); err != nil {
				os.Exit(2)
			}
			_ = childOut.Close()
			if script.ChildPIDFile != "" {
				_ = os.WriteFile(script.ChildPIDFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
			}
		}
	}
}

// callTool launches the turn's bridge as an MCP stdio client and makes one
// tools/call. The bridge's environment is added to this child alone; this
// process's own environment is what it is.
func callTool(bridge harness.ToolBridge, name string, arguments map[string]any) (string, error) {
	if len(bridge.Command) == 0 {
		return "", fmt.Errorf("no tool bridge was declared")
	}
	cmd := exec.Command(bridge.Command[0], bridge.Command[1:]...) //nolint:gosec // the turn states the bridge's argv
	cmd.Env = append(os.Environ(), bridge.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	encoder := json.NewEncoder(stdin)
	decoder := json.NewDecoder(bufio.NewReader(stdout))
	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "fakeagent", "version": "1"},
		},
	}); err != nil {
		return "", err
	}
	var initialized rpcResponse
	if err := decoder.Decode(&initialized); err != nil {
		return "", err
	}
	if initialized.Error != nil {
		return "", fmt.Errorf("initialize: %s", initialized.Error.Message)
	}

	args := make(map[string]any, len(arguments)+1)
	for key, value := range arguments {
		args[key] = value
	}
	// The server reports this value next to its own view of the same
	// variable, which is how the suite proves the bridge's environment
	// reached the server and not the agent.
	args["agent_secret"] = os.Getenv("NOCX_TEST_SECRET")
	args["agent_environment"] = os.Environ()

	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}); err != nil {
		return "", err
	}
	var response rpcResponse
	if err := decoder.Decode(&response); err != nil {
		return "", err
	}
	if response.Error != nil {
		return "", fmt.Errorf("tools/call: %s", response.Error.Message)
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return "", err
	}
	if len(result.Content) == 0 {
		return "", nil
	}
	return result.Content[0].Text, nil
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// A pollable pipe with its writer held open needs a signal to stop it.
func block(ready func()) {
	r, w, err := os.Pipe()
	if err != nil {
		os.Exit(2)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	if ready != nil {
		ready()
	}
	var b [1]byte
	_, _ = r.Read(b[:])
}
