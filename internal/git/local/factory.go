package local

import (
	"context"
	"errors"
	"time"

	"github.com/shady2k/nocx/internal/git"
)

// Factory is the local implementation of git.RepoFactory: probe capability,
// resolve the environment, run rev-parse, and build a Repo on the answer —
// all before a Repo exists, all inside the factory so the composition layer
// never has to run git of its own (spec §5.1).
type Factory struct {
	env      *envCache
	probe    *capability
	ceilings ceilings
	fixedEnv []string // pinned environment (WithEnv); nil resolves from the shell
	// prewarm cancels the background environment resolution and is non-nil
	// exactly when fixedEnv is nil. The composition root calls Stop at
	// shutdown; without it a hung rc file could outlive the process.
	prewarm context.CancelFunc
}

// Option configures a Factory. Zero values select the package defaults.
type Option func(*options)

type options struct {
	shell         string
	envTimeout    time.Duration
	envMaxOutput  int64
	fixedEnv      []string
	statusBytes   int64
	statusWall    time.Duration
	statusEntries int
	logBytes      int64
	logWall       time.Duration
}

// NewFactory builds a local factory. The environment is resolved once, in
// the background, from construction; the git probe resolves on the first
// Open; both are cached from then on. The environment is deliberately NOT on
// Open's critical path (nocx-6pz0): resolving it lazily — at the first
// commit — would leave the panel's one-shot open outcome unable to tell "not
// resolved yet" from "resolution failed", so every healthy machine would
// show the degraded warning before its first commit. Resolving from
// construction settles the state before the panel can open; a hung rc file
// costs the background attempt's deadline once, never an open.
func NewFactory(opts ...Option) *Factory {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	c := ceilings{
		statusBytes:   git.MaxStatusBytes,
		statusWall:    git.MaxStatusWallClock,
		statusEntries: git.MaxStatusEntries,
		logBytes:      git.MaxLogBytes,
		logWall:       git.MaxLogWallClock,
	}
	if o.statusBytes > 0 {
		c.statusBytes = o.statusBytes
	}
	if o.statusWall > 0 {
		c.statusWall = o.statusWall
	}
	if o.statusEntries > 0 {
		c.statusEntries = o.statusEntries
	}
	if o.logBytes > 0 {
		c.logBytes = o.logBytes
	}
	if o.logWall > 0 {
		c.logWall = o.logWall
	}
	f := &Factory{
		env:      newEnvCache(o.shell, o.envTimeout, o.envMaxOutput),
		probe:    &capability{},
		ceilings: c,
		fixedEnv: o.fixedEnv,
	}
	if f.fixedEnv == nil {
		ctx, cancel := context.WithCancel(context.Background())
		f.prewarm = cancel
		go f.env.resolve(ctx)
	}
	return f
}

// Stop cancels the background environment resolution and waits for its
// attempt to settle, so no resolution child can outlive the process. The
// composition root calls it at shutdown; a no-op when the environment is
// pinned (WithEnv) or the resolution already settled.
func (f *Factory) Stop() {
	if f.prewarm == nil {
		return
	}
	f.prewarm()
	f.env.waitSettled()
}

// Open resolves the repository cwd stands in. Every outcome other than ok is
// a state in the result, not an error — with two exceptions: an empty cwd
// (which the factory refuses rather than letting rev-parse run in the
// backend's own directory) and a cancelled context both surface as errors.
// The ownership-transfer rules (§5.1) are enforced by the caller: a Repo is
// returned only with an ok outcome, and the caller must register it or close
// it — there is no third option.
func (f *Factory) Open(ctx context.Context, cwd string) (git.Repo, git.OpenOutcome, error) {
	if cwd == "" {
		return nil, git.OpenOutcome{State: git.OpenNoCwd}, nil
	}
	env, envState, envReason := f.fixedEnv, git.EnvResolved, ""
	if f.fixedEnv == nil {
		// nocx-6pz0: the resolved environment lives off the open path. Open
		// takes the settled answer — resolved, a remembered failure, or, in
		// the window before the background attempt settles, a conservative
		// degraded — and never waits: status, diff and log need a PATH that
		// finds git, which the probe establishes, not the shell environment,
		// which only the commit path needs (D6).
		env, envState, envReason = f.env.known()
	}
	if ctx.Err() != nil {
		return nil, git.OpenOutcome{}, ctx.Err()
	}

	gitPath, version, err := f.probe.probe(ctx, env)
	outcome := git.OpenOutcome{EnvState: envState, EnvReason: envReason, GitVersion: version}
	switch {
	case err != nil:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, git.OpenOutcome{}, err
		}
		outcome.State = git.OpenGitUnavailable
		return nil, outcome, nil
	case belowFloor(version):
		outcome.State = git.OpenGitTooOld
		return nil, outcome, nil
	}

	toplevel, gitDir, err := revParse(ctx, gitPath, env, cwd)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, git.OpenOutcome{}, err
		}
		outcome.State = git.OpenNotARepository
		return nil, outcome, nil
	}

	outcome.State = git.OpenOK
	outcome.Toplevel = toplevel
	outcome.GitDir = gitDir
	var resolver *envCache
	if f.fixedEnv == nil {
		resolver = f.env
	}
	repo := &Repo{
		gitPath:   gitPath,
		pinnedEnv: f.fixedEnv,
		resolver:  resolver,
		toplevel:  toplevel,
		gitDir:    gitDir,
		ceilings:  f.ceilings,
		// Which worktree-list encoding this git can answer (2.36 added the
		// NUL form; the floor is 2.25). Decided once, here, where the version
		// is known — the same place the floor itself is decided.
		worktreeNUL: worktreeListHasNUL(version),
	}
	return repo, outcome, nil
}
