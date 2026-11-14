// Package pi connects the harness to pi's documented RPC mode.
package pi

import (
	"bufio"
	"bytes"
	"context"
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

// Connector exposes pi catalogue and readiness checks.
type Connector struct {
	dataDir       string
	env           []string
	catalogGate   chan struct{}
	cachedModels  []harness.Model
	cachedDefault string
	catalogUntil  time.Time
	manager       *harness.SessionManager
}

// New keeps pi's own provider configuration. env supplies process overrides,
// including PI_CODING_AGENT_DIR for isolated installations and tests.
func New(dataDir string, env []string) *Connector {
	return NewWithBase(dataDir, env, harness.Base{})
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

// Run submits a turn to its conversation's session actor.
func (c *Connector) Run(ctx context.Context, spec harness.TurnSpec) (harness.Turn, error) {
	return c.manager.Run(ctx, spec)
}
func (c *Connector) Close(ctx context.Context) error { return c.manager.Close(ctx) }
func (c *Connector) CloseConversation(ctx context.Context, id string) error {
	return c.manager.CloseConversation(ctx, id)
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
	decode := func(line []byte) error {
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
			return err
		}
		if r.Type != "response" || (r.Command != "get_available_models" && r.Command != "get_state") {
			return nil
		}
		if !r.Success {
			return fmt.Errorf("pi catalogue: %s", r.Error)
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
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()
	env, dir, err := (environment{overrides: c.env}).Environment(ctx)
	if err != nil {
		return nil, "", err
	}
	path, _ := shellenv.Path(env)
	executable, err := agentapproval.LookupExecutableInPath("pi", path, dir)
	if err != nil {
		return nil, "", err
	}
	cmd := exec.CommandContext(ctx, executable, "--mode", "rpc", "--no-session") //nolint:gosec // resolved pi executable, direct argv
	cmd.Env, cmd.Dir = env, dir
	proc.InOwnGroup(cmd)
	cmd.Cancel = func() error { return proc.KillGroupImmediately(cmd) }
	cmd.WaitDelay = time.Second
	var stderr authOutput
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if err = cmd.Start(); err != nil {
		return nil, "", err
	}
	encoder := json.NewEncoder(stdin)
	for _, command := range []string{"get_available_models", "get_state"} {
		if err = encoder.Encode(map[string]string{"type": command}); err != nil {
			break
		}
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for err == nil && !(gotModels && gotState) && scanner.Scan() {
		err = decode(scanner.Bytes())
	}
	if err == nil {
		err = scanner.Err()
	}
	complete := gotModels && gotState && err == nil
	// Discovery owns no conversation. End its process group even if an extension
	// keeps a pipe open or pi does not exit on stdin EOF.
	_ = stdin.Close()
	_ = proc.KillGroupImmediately(cmd)
	waitErr := cmd.Wait()
	if err != nil {
		return nil, "", err
	}
	if !complete {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", fmt.Errorf("pi catalogue ended before both responses: %w (%s)", errors.Join(waitErr, errors.New("incomplete catalogue")), strings.TrimSpace(stderr.String()))
	}
	return models, selected, nil
}
