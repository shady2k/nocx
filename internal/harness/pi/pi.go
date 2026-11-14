// Package pi connects the harness to pi's documented RPC mode.
package pi

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
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
	dataDir       string
	env           []string
	base          harness.Base
	catalogGate   chan struct{}
	cachedModels  []harness.Model
	cachedDefault string
	catalogUntil  time.Time
	stateErr      error
}

// New keeps pi's own provider configuration. env supplies process overrides,
// including PI_CODING_AGENT_DIR for isolated installations and tests.
func New(dataDir string, env []string) *Connector {
	c := &Connector{dataDir: dataDir, env: append([]string(nil), env...), catalogGate: make(chan struct{}, 1)}
	if dataDir != "" {
		c.stateErr = c.sweepTurnFiles()
	}
	return c
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
	ctx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()
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
	if !slices.ContainsFunc(models, func(m harness.Model) bool { return m.ID == chosen }) {
		return harness.Readiness{State: harness.NotConfigured, Message: "The selected model is not in pi's catalogue: " + chosen}
	}
	// The command uses the same login-shell executable and environment as turns.
	cmd := exec.CommandContext(ctx, executable, "auth", "check", "--model", chosen, "--no-refresh") //nolint:gosec // resolved pi executable, direct argv
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.stateErr != nil {
		return nil, c.stateErr
	}
	if spec.Model != "" {
		models, _, err := c.queryCatalog(ctx)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(models, func(m harness.Model) bool { return m.ID == spec.Model }) {
			return nil, fmt.Errorf("model %q is not in pi's catalogue", spec.Model)
		}
	}
	files, err := c.prepareTurn(spec)
	if err != nil {
		return nil, err
	}
	cleanup := files.cleanup
	argv := []string{"pi", "--mode", "rpc", "--session-dir", files.sessionDir, "--session-id", spec.ConversationID, "--no-builtin-tools", "--tools", "mcp__nocx__*"}
	if spec.Model != "" {
		argv = append(argv, "--model", spec.Model)
	}
	argv = append(argv, "-e", files.extensionPath)
	state := &cancelState{}
	p, err := c.base.Start(context.WithoutCancel(ctx), harness.Launch{Argv: argv, Env: append(append([]string(nil), c.env...), "NOCX_PI_TURN_FILE="+files.configPath), Dir: files.dir, Decode: newDecoder(state), CompleteOnEnd: true, FailureReason: failureReason})
	if err != nil {
		cleanup()
		return nil, err
	}
	if err = json.NewEncoder(p.Stdin()).Encode(map[string]string{"type": "prompt", "message": spec.Text}); err != nil {
		_ = p.Cancel(context.Background())
		_, _ = p.Wait(context.Background())
		cleanup()
		return nil, err
	}
	return newTurn(ctx, p, cleanup, state), nil
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

const (
	readinessTimeout   = 10 * time.Second
	catalogCacheWindow = 2 * time.Second
)

// catalog shares successful discovery between Settings readiness and Models.
func (c *Connector) catalog(ctx context.Context) ([]harness.Model, string, error) {
	select {
	case c.catalogGate <- struct{}{}:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	defer func() { <-c.catalogGate }()
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if time.Now().Before(c.catalogUntil) {
		return slices.Clone(c.cachedModels), c.cachedDefault, nil
	}
	models, selected, err := c.queryCatalog(ctx)
	if err == nil {
		c.cachedModels = slices.Clone(models)
		c.cachedDefault = selected
		c.catalogUntil = time.Now().Add(catalogCacheWindow)
	}
	return models, selected, err
}

func (c *Connector) queryCatalog(ctx context.Context) ([]harness.Model, string, error) {
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
