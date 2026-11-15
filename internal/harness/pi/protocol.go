package pi

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/harness"
)

//go:embed extension.ts
var extension []byte

// NewWithBase allows composition to supply the session endpoint and bounds.
func NewWithBase(dataDir string, env []string, base harness.Base) *Connector {
	sweepOrphanConfigs(context.Background(), dataDir)
	c := &Connector{dataDir: dataDir, env: append([]string(nil), env...), catalogGate: make(chan struct{}, 1)}
	c.manager = harness.NewSessionManager(base, c)
	return c
}

// Start describes one incarnation. The actor alone owns its lifetime.
func (c *Connector) Start(ctx context.Context, spec harness.TurnSpec) (harness.Launch, error) {
	root, err := filepath.Abs(filepath.Join(c.dataDir, "pi"))
	if err != nil {
		return harness.Launch{}, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(spec.ConversationID)))
	dir := filepath.Join(root, "workspaces", key)
	sessions := filepath.Join(root, "sessions")
	configs := filepath.Join(root, "configs")
	for _, path := range []string{dir, sessions, configs} {
		if err = os.MkdirAll(path, 0o700); err != nil {
			return harness.Launch{}, err
		}
	}
	// A private file belongs to this incarnation, never to a turn. A delayed
	// starting child must not read another incarnation's bridge credentials.
	config, err := os.CreateTemp(configs, sessionConfigPattern())
	if err != nil {
		return harness.Launch{}, err
	}
	configPath := config.Name()
	privateDir := configPath + ".d"
	if err = os.Mkdir(privateDir, 0o700); err != nil {
		_ = config.Close()
		_ = os.Remove(configPath)
		return harness.Launch{}, err
	}
	promptPath := filepath.Join(privateDir, "prompt.txt")
	ready := false
	defer func() {
		if !ready {
			_ = os.Remove(configPath)
			_ = os.RemoveAll(privateDir)
		}
	}()
	env := make(map[string]string, len(spec.Tools.Env))
	for _, entry := range spec.Tools.Env {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			env[k] = v
		}
	}
	raw, err := json.Marshal(struct {
		Command    []string          `json:"command"`
		Env        map[string]string `json:"env"`
		PromptFile string            `json:"promptFile"`
	}{spec.Tools.Command, env, promptPath})
	if err != nil {
		return harness.Launch{}, err
	}
	_, configErr := config.Write(raw)
	if err = errors.Join(configErr, config.Close()); err != nil {
		return harness.Launch{}, err
	}
	if err = os.WriteFile(promptPath, []byte(spec.SystemPrompt), 0o600); err != nil {
		return harness.Launch{}, err
	}
	// Atomic replacement keeps concurrent conversation starts from seeing a
	// partially written copy of the common embedded extension.
	temp, err := os.CreateTemp(root, "extension-*.mjs")
	if err != nil {
		return harness.Launch{}, err
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()
	_, writeErr := temp.Write(extension)
	if err := errors.Join(writeErr, temp.Close()); err != nil {
		return harness.Launch{}, err
	}
	extensionPath := filepath.Join(root, "extension.mjs")
	if err := os.Rename(name, extensionPath); err != nil {
		return harness.Launch{}, err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(configPath)
		return harness.Launch{}, err
	}
	argv := []string{"pi", "--mode", "rpc", "--session-dir", sessions, "--session-id", spec.ConversationID, "--no-builtin-tools", "--tools", "mcp__nocx__*"}
	if spec.Model != "" {
		argv = append(argv, "--model", spec.Model)
	}
	argv = append(argv, "-e", extensionPath)
	ready = true
	return harness.Launch{PromptPath: promptPath, Argv: argv, Dir: dir, Env: append(append([]string(nil), c.env...), "NOCX_PI_SESSION_FILE="+configPath), Cleanup: func() error {
		var errs []error
		if err := os.Remove(configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		if err := os.RemoveAll(privateDir); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}}, nil
}

func (c *Connector) NewCodec(info harness.SessionInfo, launch harness.Launch) harness.Codec {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(info.ConversationID)))
	codec := &codec{markerPath: filepath.Join(c.dataDir, "pi", "resume", key+".json"), promptPath: launch.PromptPath}
	raw, err := os.ReadFile(codec.markerPath) //nolint:gosec // connector-owned conversation metadata
	if err == nil {
		codec.markerErr = json.Unmarshal(raw, &codec.expected)
	} else if !errors.Is(err, os.ErrNotExist) {
		codec.markerErr = err
	}
	return codec
}

type resumeMarker struct {
	SessionFile string `json:"sessionFile"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
}

type codec struct {
	promptPath  string
	prompt      string
	model       string
	outcome     harness.Outcome
	decode      func([]byte) ([]harness.Event, error)
	markerPath  string
	markerErr   error
	expected    resumeMarker
	sessionFile string
}

func (*codec) Setup() []harness.Step { return nil }
func (c *codec) BeginTurn(spec harness.TurnSpec) []harness.Step {
	c.outcome = harness.Outcome{}
	c.decode = newDecoder(&c.outcome)
	c.prompt = spec.SystemPrompt
	selectModel := spec.Model != "" && spec.Model != c.model
	steps := []harness.Step{{Command: harness.Command{Name: "get_state"}, Barrier: true, Check: func(payload any) error {
		if err := c.readState(payload); err != nil {
			return err
		}
		if spec.Model != "" && !selectModel && c.model != spec.Model {
			return fmt.Errorf("pi changed the pinned model: wanted %q, selected %q", spec.Model, c.model)
		}
		return nil
	}, Model: func() string {
		if spec.Model == "" || c.model == spec.Model {
			return c.model
		}
		return ""
	}}}
	if selectModel {
		provider, model, _ := strings.Cut(spec.Model, "/")
		steps = append(steps,
			harness.Step{Command: harness.Command{Name: "set_model", Payload: map[string]any{"provider": provider, "modelId": model}}},
			harness.Step{Command: harness.Command{Name: "get_state"}, Check: func(payload any) error {
				if err := c.readState(payload); err != nil {
					return err
				}
				if c.model != spec.Model {
					return fmt.Errorf("pi did not select the exact model %q (selected %q)", spec.Model, c.model)
				}
				return nil
			}, Model: func() string { return c.model }})
	}
	return append(steps, harness.Step{Command: harness.Command{Name: "prompt", Payload: map[string]any{"message": spec.Text}}, Prompt: true})
}

func (c *codec) readState(payload any) error {
	state, ok := payload.(map[string]any)
	if !ok {
		return errors.New("pi returned invalid state")
	}
	if c.markerErr != nil {
		return fmt.Errorf("pi conversation session was lost: invalid resume record: %w", c.markerErr)
	}
	sessionFile, _ := state["sessionFile"].(string)
	count, _ := state["messageCount"].(float64)
	if c.expected.SessionFile != "" {
		if sessionFile != c.expected.SessionFile {
			return errors.New("pi conversation session was lost: persisted history could not be resumed")
		}
		if err := c.expected.validate(); err != nil {
			return fmt.Errorf("pi conversation session was lost: %w", err)
		}
	} else if count > 0 {
		return errors.New("pi conversation session was lost: persisted history has no resume record")
	}
	c.sessionFile = sessionFile
	c.model = ""
	if model, ok := state["model"].(map[string]any); ok {
		provider, _ := model["provider"].(string)
		id, _ := model["id"].(string)
		if provider != "" && id != "" {
			c.model = provider + "/" + id
		}
	}
	return nil
}

func (*codec) CancelSequence() []harness.Command {
	return []harness.Command{{Name: "clear_queue"}, {Name: "abort"}}
}

func (c *codec) Encode(id string, command harness.Command) ([]byte, error) {
	if command.Name == "prompt" {
		if err := os.WriteFile(c.promptPath, []byte(c.prompt), 0o600); err != nil {
			return nil, fmt.Errorf("pi prompt file: %w", err)
		}
	}
	wire := map[string]any{"id": id, "type": command.Name}
	if payload, ok := command.Payload.(map[string]any); ok {
		for key, value := range payload {
			wire[key] = value
		}
	}
	raw, err := json.Marshal(wire)
	return append(raw, '\n'), err
}

func (c *codec) Decode(line []byte) (harness.Decoded, error) {
	var wire struct {
		ID      string         `json:"id"`
		Type    string         `json:"type"`
		Command string         `json:"command"`
		Success bool           `json:"success"`
		Error   string         `json:"error"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(line, &wire); err != nil {
		return harness.Decoded{}, err
	}
	if wire.Type == "response" {
		reason := wire.Error
		if !wire.Success && reason == "" {
			reason = "pi rejected " + wire.Command
		}
		if wire.Command == "prompt" && wire.Data["disposition"] == "handled" {
			reason = "pi extension handled the prompt without an answer"
		}
		return harness.Decoded{Response: &harness.Response{ID: wire.ID, Error: reason, Payload: wire.Data}}, nil
	}
	if c.decode == nil {
		c.outcome = harness.Outcome{}
		c.decode = newDecoder(&c.outcome)
	}
	events, err := c.decode(line)
	// turn_end follows persistence of a completed model call and its tool
	// results. agent_settled also covers first-turn failures and cancellation.
	if err == nil && (wire.Type == "turn_end" || wire.Type == "agent_settled") {
		err = c.checkpoint()
	}
	return harness.Decoded{Events: events, Outcome: &c.outcome}, err
}
