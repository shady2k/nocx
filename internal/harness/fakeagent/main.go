// The fake agent serves sequential prompts over a persistent JSON-lines session.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-child" {
		block(func() { _, _ = fmt.Fprintln(os.Stdout, "ready") })
		return
	}
	var script protocol.Script
	if err := json.Unmarshal([]byte(os.Getenv("FAKEAGENT_SCRIPT")), &script); err != nil {
		os.Exit(2)
	}
	if script.CleanupGate != "" {
		go func() {
			gate, err := os.Open(script.CleanupGate)
			if err == nil {
				var b [1]byte
				_, _ = gate.Read(b[:])
				os.Exit(1)
			}
		}()
	}
	if script.PIDFile != "" {
		_ = os.WriteFile(script.PIDFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	if script.EnvironmentFile != "" {
		raw, _ := json.Marshal(os.Environ())
		_ = os.WriteFile(script.EnvironmentFile, raw, 0o600)
	}
	commands := make(chan protocol.Command)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 65536), 1<<20)
		for scanner.Scan() {
			var command protocol.Command
			if json.Unmarshal(scanner.Bytes(), &command) != nil {
				os.Exit(2)
			}
			commands <- command
		}
		if !script.IgnoreStdinEOF {
			os.Exit(0)
		}
	}()
	var mu sync.Mutex
	out := json.NewEncoder(os.Stdout)
	emit := func(line protocol.Line) { mu.Lock(); defer mu.Unlock(); _ = out.Encode(line) }
	var cancel context.CancelFunc
	var done chan struct{}
	turn := 0
	for command := range commands {
		switch command.Name {
		case "initialize":
			if command.Session == nil {
				emit(protocol.Line{Kind: "response", ID: command.ID, Reason: "missing session"})
				continue
			}
			emit(protocol.Line{Kind: "response", ID: command.ID, Payload: command.Session})
		case "get_state":
			// The previous run must finish writing before this response fences it.
			if done != nil {
				<-done
			}
			model := "fake-default"
			for _, step := range script.Steps {
				if step.Model != "" {
					model = step.Model
					break
				}
			}
			if script.DefaultModelFile != "" {
				raw, err := os.ReadFile(script.DefaultModelFile)
				if err != nil {
					os.Exit(2)
				}
				model = string(raw)
			}
			emit(protocol.Line{Kind: "response", ID: command.ID, Model: model, Payload: model})
		case "prompt":
			if done != nil {
				<-done
				cancel()
			}
			if command.Prompt == nil {
				os.Exit(2)
			}
			prompt := *command.Prompt
			if prompt.Model == "" && script.DefaultModelFile != "" {
				raw, err := os.ReadFile(script.DefaultModelFile)
				if err != nil {
					os.Exit(2)
				}
				prompt.Model = string(raw)
			}
			if script.PromptFile != "" {
				prompt.Dir, _ = os.Getwd()
				raw, _ := json.Marshal(prompt)
				_ = os.WriteFile(script.PromptFile, raw, 0o600)
			}
			steps := script.Steps
			if len(script.Turns) > 0 {
				index := turn
				if index >= len(script.Turns) {
					index = len(script.Turns) - 1
				}
				steps = script.Turns[index]
			}
			turn++
			ctx, stop := context.WithCancel(context.Background())
			cancel = stop
			done = make(chan struct{})
			emit(protocol.Line{Kind: "response", ID: command.ID})
			go func(done chan struct{}) { defer close(done); run(ctx, script, prompt, steps, emit) }(done)
		case "steer":
			if command.Steer == nil {
				emit(protocol.Line{Kind: "response", ID: command.ID, Reason: "missing steer"})
				continue
			}
			if script.SteerFile != "" {
				file, openErr := os.OpenFile(script.SteerFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
				if openErr != nil {
					os.Exit(2)
				}
				_, _ = file.WriteString(command.Steer.Text + "\n")
				_ = file.Close()
			}
			emit(protocol.Line{Kind: "response", ID: command.ID})
		case "abort":
			if script.IgnoreAbort {
				continue
			}
			if cancel != nil {
				cancel()
				<-done
				cancel = nil
				done = nil
				emit(protocol.Line{Kind: "failure", Reason: "cancelled"})
			}
			emit(protocol.Line{Kind: "response", ID: command.ID})
		default:
			emit(protocol.Line{Kind: "response", ID: command.ID, Reason: "unknown command"})
		}
	}
}

func run(ctx context.Context, script protocol.Script, prompt protocol.Prompt, steps []protocol.Step, emit func(protocol.Line)) {
	calls := 0
	var lastResult string
	for _, step := range steps {
		if ctx.Err() != nil {
			return
		}
		switch step.Kind {
		case protocol.StepAnswer:
			emit(protocol.Line{Kind: "answer", Text: step.Text})
		case protocol.StepReasoning:
			emit(protocol.Line{Kind: "reasoning", Text: step.Text})
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
			emit(protocol.Line{Kind: "failure", Model: prompt.Model, Usage: step.Usage, Reason: step.Reason})
		case protocol.StepExit:
			os.Exit(step.Code)
		case protocol.StepHang:
			if script.HangFile != "" {
				_ = os.WriteFile(script.HangFile, []byte("blocked"), 0o600)
			}
			<-ctx.Done()
			return
		case protocol.StepGate:
			ready := make(chan struct{})
			go func() {
				for ctx.Err() == nil {
					gate, err := os.Open(script.Gate)
					if err != nil {
						return
					}
					var b [1]byte
					n, _ := gate.Read(b[:])
					_ = gate.Close()
					if n == 1 {
						close(ready)
						return
					}
				}
			}()
			select {
			case <-ctx.Done():
				return
			case <-ready:
			}
		case protocol.StepWaitRun:
			for strings.Contains(lastResult, "STILL RUNNING") {
				match := regexp.MustCompile(`run id ([0-9a-f]+)`).FindStringSubmatch(lastResult)
				if len(match) != 2 {
					emit(protocol.Line{Kind: "failure", Reason: "quiet result has no run id"})
					return
				}
				calls++
				id := "call-" + strconv.Itoa(calls)
				emit(protocol.Line{Kind: "tool_started", ID: id, Name: "session.wait"})
				result, _, err := callTool(ctx, prompt.Tools, "session.wait", map[string]any{"runId": match[1], "decision": step.Decision}, script.ProbeEnvironment, false, false)
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					emit(protocol.Line{Kind: "failure", Reason: fmt.Sprintf("tool call failed: %v", err)})
					return
				}
				lastResult = result
				if script.ResultFile != "" {
					_ = os.WriteFile(script.ResultFile, []byte(result), 0o600)
				}
				emit(protocol.Line{Kind: "tool_finished", ID: id, Name: "session.wait"})
			}
		case protocol.StepTool:
			calls++
			id := "call-" + strconv.Itoa(calls)
			emit(protocol.Line{Kind: "tool_started", ID: id, Name: step.Name})
			arguments := make(map[string]any, len(step.Arguments)+len(step.ArgumentsFromResult))
			for key, value := range step.Arguments {
				arguments[key] = value
			}
			for key, fact := range step.ArgumentsFromPrompt {
				if fact != "attachedItemID" {
					emit(protocol.Line{Kind: "failure", Reason: "unknown prompt fact: " + fact})
					return
				}
				found := false
				for _, line := range strings.Split(prompt.Text+"\n"+prompt.SystemPrompt, "\n") {
					if rest, ok := strings.CutPrefix(line, "- id: "); ok {
						attachedID, _, _ := strings.Cut(rest, ";")
						arguments[key] = attachedID
						found = true
						break
					}
				}
				if !found {
					emit(protocol.Line{Kind: "failure", Reason: "no attached item in prompt"})
					return
				}
			}
			if len(step.ArgumentsFromResult) > 0 {
				var decoded any
				if json.Unmarshal([]byte(lastResult), &decoded) != nil {
					emit(protocol.Line{Kind: "failure", Reason: "invalid preceding tool result JSON"})
					return
				}
				for key, path := range step.ArgumentsFromResult {
					value := decoded
					for _, part := range strings.Split(path, ".") {
						var ok bool
						switch object := value.(type) {
						case map[string]any:
							value, ok = object[part]
						case []any:
							index, err := strconv.Atoi(part)
							if err == nil && index >= 0 && index < len(object) {
								value, ok = object[index], true
							}
						}
						if !ok {
							emit(protocol.Line{Kind: "failure", Reason: "missing result field: " + path})
							return
						}
					}
					if offset, ok := step.ArgumentOffsets[key]; ok {
						number, ok := value.(float64)
						if !ok {
							emit(protocol.Line{Kind: "failure", Reason: "result argument is not numeric: " + key})
							return
						}
						value = number + offset
					}
					arguments[key] = value
				}
			}
			result, isError, err := callTool(ctx, prompt.Tools, step.Name, arguments, script.ProbeEnvironment, step.FailOnError, step.ContinueOnRPCError)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				if result != "" {
					if step.ResultFile != "" {
						_ = os.WriteFile(step.ResultFile, []byte(result), 0o600)
					}
					if script.ResultFile != "" && !isError {
						_ = os.WriteFile(script.ResultFile, []byte(result), 0o600)
					}
				}
				emit(protocol.Line{Kind: "failure", Reason: fmt.Sprintf("tool call failed: %v", err)})
				return
			}
			lastResult = result
			if step.ResultFile != "" {
				_ = os.WriteFile(step.ResultFile, []byte(result), 0o600)
			}
			if script.ResultFile != "" && !isError {
				_ = os.WriteFile(script.ResultFile, []byte(result), 0o600)
			}
			emit(protocol.Line{Kind: "tool_finished", ID: id, Name: step.Name})
		case protocol.StepBridge:
			bridge := prompt.Tools
			cmd := exec.CommandContext(ctx, bridge.Command[0], bridge.Command[1:]...) //nolint:gosec // fixture bridge
			cmd.Env = append(os.Environ(), bridge.Env...)
			input, err := cmd.StdinPipe()
			if err != nil {
				os.Exit(2)
			}
			if cmd.Start() != nil {
				os.Exit(2)
			}
			defer func() { _ = input.Close(); _ = cmd.Wait() }()
		case protocol.StepChild:
			self, err := os.Executable()
			if err != nil {
				os.Exit(2)
			}
			child := exec.Command(self, "-child") //nolint:gosec // this fixture
			out, err := child.StdoutPipe()
			if err != nil {
				os.Exit(2)
			}
			if child.Start() != nil {
				os.Exit(2)
			}
			_, _ = bufio.NewReader(out).ReadString('\n')
			_ = out.Close()
			if script.ChildPIDFile != "" {
				_ = os.WriteFile(script.ChildPIDFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
			}
		}
	}
}

// callTool launches the turn's bridge as an MCP stdio client and makes one
// tools/call. The bridge's environment is added to this child alone; this
// process's own environment is what it is.
func callTool(ctx context.Context, bridge harness.ToolBridge, name string, arguments map[string]any, probeEnvironment, failOnError, continueOnRPCError bool) (string, bool, error) {
	if len(bridge.Command) == 0 {
		return "", false, fmt.Errorf("no tool bridge was declared")
	}
	cmd := exec.CommandContext(ctx, bridge.Command[0], bridge.Command[1:]...) //nolint:gosec // the turn states the bridge's argv
	cmd.Env = append(os.Environ(), bridge.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return "", false, err
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
		return "", false, err
	}
	var initialized rpcResponse
	if err := decoder.Decode(&initialized); err != nil {
		return "", false, err
	}
	if initialized.Error != nil {
		return "", false, fmt.Errorf("initialize: %s", initialized.Error.Message)
	}

	args := make(map[string]any, len(arguments)+1)
	for key, value := range arguments {
		args[key] = value
	}
	// The server reports this value next to its own view of the same
	// variable, which is how the suite proves the bridge's environment
	// reached the server and not the agent.
	if probeEnvironment {
		args["agent_secret"] = os.Getenv("NOCX_TEST_SECRET")
		args["agent_environment"] = os.Environ()
	}

	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}); err != nil {
		return "", false, err
	}
	var response rpcResponse
	if err := decoder.Decode(&response); err != nil {
		return "", false, err
	}
	if response.Error != nil {
		text := "tools/call: " + response.Error.Message
		if !continueOnRPCError {
			return text, true, errors.New(text)
		}
		return text, true, nil
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return "", false, err
	}
	if len(result.Content) == 0 {
		return "", false, nil
	}
	if failOnError && result.IsError {
		return result.Content[0].Text, true, errors.New(result.Content[0].Text)
	}
	return result.Content[0].Text, result.IsError, nil
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
