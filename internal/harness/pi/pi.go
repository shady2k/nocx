// Package pi connects the harness to pi's documented RPC mode.
package pi

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/agentdetect"
	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/proc"
	"github.com/shady2k/nocx/internal/shellenv"
)

//go:embed extension.ts
var extension []byte

// Connector runs one pi process per turn; dataDir owns extensions and sessions.
type Connector struct {
	dataDir string
	env     []string
	base    harness.Base
}

// New keeps pi's own provider configuration. env supplies process overrides,
// including PI_CODING_AGENT_DIR for isolated installations and tests.
func New(dataDir string, env []string) *Connector {
	return &Connector{dataDir: dataDir, env: append([]string(nil), env...)}
}

func (*Connector) Facts() harness.Facts {
	return harness.Facts{ToolBoundary: harness.NocxToolsOnly, ContinuesConversationByID: true, ReportsAnsweringModel: true}
}

type environment struct{ overrides []string }

func (e environment) Environment(ctx context.Context) ([]string, string, error) {
	env, dir, err := (agentdetect.LoginShell{}).Environment(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, v := range e.overrides {
		key, _, _ := strings.Cut(v, "=")
		for i := len(env) - 1; i >= 0; i-- {
			if strings.HasPrefix(env[i], key+"=") {
				env = append(env[:i], env[i+1:]...)
			}
		}
		env = append(env, v)
	}
	return env, dir, nil
}

// Readiness reports whether pi's selected/default model can authenticate.
func (c *Connector) Readiness(ctx context.Context) harness.Readiness {
	return c.ReadinessForModel(ctx, "")
}

// ReadinessForModel checks a chosen model, or pi's reported default when empty.
// Settings can check the same selection that a turn will use without changing
// pi's saved configuration.
func (c *Connector) ReadinessForModel(ctx context.Context, chosen string) harness.Readiness {
	env, dir, err := (environment{overrides: c.env}).Environment(ctx)
	if err != nil {
		return harness.Readiness{State: harness.NotConfigured, Message: fmt.Sprint("pi could not be checked: ", err)}
	}
	path, _ := shellenv.Path(env)
	executable, err := agentapproval.LookupExecutableInPath("pi", path, dir)
	if err != nil {
		return harness.Readiness{State: harness.NotInstalled, Message: "Install pi to use this agent."}
	}
	models, defaultModel, err := c.catalog(ctx)
	if err != nil {
		return harness.Readiness{State: harness.NotConfigured, Message: fmt.Sprint("pi's models could not be checked: ", err)}
	}
	if len(models) == 0 {
		return harness.Readiness{State: harness.NotConfigured, Message: "Configure a provider in pi, then check again."}
	}
	if chosen == "" {
		chosen = defaultModel
		if chosen == "" {
			chosen = models[0].ID
		}
	}
	// The command uses the same login-shell executable and environment as turns.
	cmd := exec.CommandContext(ctx, executable, "auth", "check", "--model", chosen) //nolint:gosec // resolved pi executable, direct argv
	cmd.Env = env
	cmd.Dir = dir
	proc.InOwnGroup(cmd)
	cmd.Cancel = func() error { return proc.KillGroupImmediately(cmd) }
	cmd.WaitDelay = time.Second
	var output authOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	sentence := strings.TrimSpace(output.String())
	if err == nil {
		return harness.Readiness{State: harness.Ready, Message: "pi is ready to answer."}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.ExitCode() == 2 {
			return harness.Readiness{State: harness.NotConfigured, Message: "pi's credentials are invalid. " + sentence}
		}
		if exit.ExitCode() == 1 && sentence != "" {
			return harness.Readiness{State: harness.NotConfigured, Message: sentence}
		}
	}
	return harness.Readiness{State: harness.NotConfigured, Message: fmt.Sprint("pi could not authenticate: ", err)}
}

// authOutput bounds the CLI's human-readable diagnostic without blocking its pipes.
type authOutput struct{ bytes.Buffer }

func (w *authOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 64<<10 - w.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = w.Buffer.Write(p)
	}
	return n, nil
}

func (c *Connector) Run(ctx context.Context, spec harness.TurnSpec) (harness.Turn, error) {
	if c.dataDir == "" {
		return nil, errors.New("pi needs nocx's data directory")
	}
	dir := filepath.Join(c.dataDir, "pi")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	config, err := os.CreateTemp(dir, "turn-*.json")
	if err != nil {
		return nil, err
	}
	configPath := config.Name()
	bridgeEnv := map[string]string{}
	for _, v := range spec.Tools.Env {
		key, value, ok := strings.Cut(v, "=")
		if !ok {
			_ = config.Close()
			_ = os.Remove(configPath)
			return nil, fmt.Errorf("invalid bridge environment variable %q", key)
		}
		bridgeEnv[key] = value
	}
	err = json.NewEncoder(config).Encode(struct {
		Command      []string          `json:"command"`
		Env          map[string]string `json:"env"`
		SystemPrompt string            `json:"systemPrompt"`
	}{spec.Tools.Command, bridgeEnv, spec.SystemPrompt})
	err = errors.Join(err, config.Close())
	if err != nil {
		_ = os.Remove(configPath)
		return nil, err
	}
	ext, err := os.CreateTemp(dir, "extension-*.mjs")
	if err != nil {
		_ = os.Remove(configPath)
		return nil, err
	}
	extPath := ext.Name()
	_, err = ext.Write(extension)
	err = errors.Join(err, ext.Close())
	cleanup := func() { _ = os.Remove(configPath); _ = os.Remove(extPath) }
	if err != nil {
		cleanup()
		return nil, err
	}
	argv := []string{"pi", "--mode", "rpc", "--session-dir", filepath.Join(dir, "sessions"), "--session-id", spec.ConversationID, "--no-builtin-tools", "--tools", "mcp__nocx__*"}
	if spec.Model != "" {
		argv = append(argv, "--model", spec.Model)
	}
	argv = append(argv, "-e", extPath)
	p, err := c.base.Start(ctx, harness.Launch{Argv: argv, Env: append(append([]string(nil), c.env...), "NOCX_PI_TURN_FILE="+configPath), Dir: spec.Dir, Decode: newDecoder(), CompleteOnEnd: true, FailureReason: failureReason})
	if err != nil {
		cleanup()
		return nil, err
	}
	turn := &turn{Process: p, cleanup: cleanup}
	if err = json.NewEncoder(p.Stdin()).Encode(map[string]string{"type": "prompt", "message": spec.Text}); err != nil {
		_ = p.Cancel(context.Background())
		_, _ = turn.Wait(context.Background())
		return nil, err
	}
	return turn, nil
}

type turn struct {
	*harness.Process
	cleanup func()
	once    sync.Once
}

func (t *turn) Wait(ctx context.Context) (harness.Outcome, error) {
	outcome, err := t.Process.Wait(ctx)
	if ctx.Err() == nil {
		t.once.Do(t.cleanup)
	}
	return outcome, err
}

func failureReason(stderr string, exitErr error) string {
	if text := strings.TrimSpace(stderr); text != "" {
		return "pi failed: " + text
	}
	return fmt.Sprint("pi failed: ", exitErr)
}

// Models asks pi for the models its configured providers can run.
func (c *Connector) Models(ctx context.Context) ([]harness.Model, error) {
	models, _, err := c.catalog(ctx)
	return models, err
}

func (c *Connector) catalog(ctx context.Context) ([]harness.Model, string, error) {
	var models []harness.Model
	var selected string
	var gotModels, gotState bool
	type model struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
	}
	decode := func(line []byte) ([]harness.Event, error) {
		var r struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Success bool   `json:"success"`
			Error   string `json:"error"`
			Data    struct {
				Models []model `json:"models"`
				Model  *model  `json:"model"`
			} `json:"data"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, err
		}
		if r.Type != "response" || (r.Command != "get_available_models" && r.Command != "get_state") {
			return nil, nil
		}
		if !r.Success {
			return []harness.Event{{Kind: harness.EventFailure, Failure: &harness.Failure{Reason: r.Error}}}, nil
		}
		if r.Command == "get_available_models" {
			gotModels = true
			for _, m := range r.Data.Models {
				models = append(models, harness.Model{ID: m.Provider + "/" + m.ID, Name: m.Name})
			}
		} else {
			gotState = true
			if m := r.Data.Model; m != nil {
				selected = m.Provider + "/" + m.ID
			}
		}
		if gotModels && gotState {
			return []harness.Event{{Kind: harness.EventEnd, End: &harness.End{}}}, nil
		}
		return nil, nil
	}
	p, err := c.base.Start(ctx, harness.Launch{Argv: []string{"pi", "--mode", "rpc", "--no-session"}, Env: c.env, Decode: decode, CompleteOnEnd: true, FailureReason: failureReason})
	if err != nil {
		return nil, "", err
	}
	encoder := json.NewEncoder(p.Stdin())
	for _, command := range []string{"get_available_models", "get_state"} {
		if err = encoder.Encode(map[string]string{"type": command}); err != nil {
			_ = p.Cancel(context.Background())
			break
		}
	}
	for range p.Events() {
	}
	_, waitErr := p.Wait(context.Background())
	if err = errors.Join(err, waitErr); err != nil {
		return nil, "", err
	}
	return models, selected, nil
}
