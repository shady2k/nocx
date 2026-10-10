package app

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/log"
	coresession "github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
)

// scrollbackSetting is the composition root's hold on the person's
// scrollback setting (nocx-zg3k3.10.1): what a pane opened now is BORN with,
// read at spawn the way the row buffers' helper half is; a change to the
// setting fans out to every live session through watchScrollback below.
type scrollbackSetting struct {
	lines atomic.Uint64
}

// newScrollbackSetting starts at the DECLARED default, read from the
// declaration's own type rather than restated (Number.DefaultValue exists
// so a package that must hold a fallback reads the declared one), and the
// watch below replaces it with the person's actual value the moment the
// registry is wired. A spawn that raced the wiring would therefore carry
// the default — never a zero, which is a VALUE in this setting and would
// erase a pane's history nobody asked to erase.
func newScrollbackSetting(reg *settings.Registry) *scrollbackSetting {
	s := &scrollbackSetting{}
	if v, err := reg.GetNumber(settings.TerminalScrollbackLines); err == nil {
		s.lines.Store(uint64(v))
	} else {
		s.lines.Store(uint64(settings.TerminalScrollbackLines.DefaultValue()))
	}
	return s
}

// linesPtr is what a spawn carries: a pointer, because zero is a VALUE in
// this setting (a pane that keeps no history) where the row buffer's zero
// means the helper's default.
func (s *scrollbackSetting) linesPtr() *uint64 {
	// Nil is a legitimate state, the way a nil rowBuffers holder is: a test
	// stand builds an opener literal without the composition root, and a
	// spawn from it carries NO budget — the helper answers with its own
	// default, which is exactly what "this coordinator carries no setting"
	// means on the wire.
	if s == nil {
		return nil
	}
	v := s.lines.Load()
	return &v
}

// scrollbackCarrier is the one call a budget change makes on a helper
// connection. *helperclient.Client is the real one; a test double stands in
// at this boundary and nowhere lower — the wire itself is the session
// service's own tests.
type scrollbackCarrier interface {
	SetScrollback(ctx context.Context, id client.HostSessionID, lines uint64) error
}

// scrollbackTarget is one live session and the connection its op rides.
type scrollbackTarget struct {
	carrier scrollbackCarrier
	id      client.HostSessionID
}

// scrollbackFanout carries one budget change to every live session: this
// machine's helper's panes and the far registry's. The two walks arrive as
// functions because they are the composition root's WIRING — the concrete
// walkers below are bound where the opener and the registry exist — and
// because a test of the fan-out's own logic (every session reached, one
// carrier's refusal not starving the rest) needs no daemon to stand up.
type scrollbackFanout struct {
	local  func(context.Context) ([]scrollbackTarget, error)
	remote func(context.Context) ([]scrollbackTarget, error)
}

// apply pushes the budget to every target it can reach. Best-effort by
// design: one pane whose connection is mid-teardown, or one helper a
// generation behind (unknown_op), is logged and skipped — a pane keeping
// the budget it was born with is the degradation the op's own contract
// names, and it must not starve the panes that can take the change.
func (f *scrollbackFanout) apply(ctx context.Context, lines uint64) {
	lg := log.From(ctx)
	for _, walk := range []func(context.Context) ([]scrollbackTarget, error){f.local, f.remote} {
		targets, err := walk(ctx)
		if err != nil {
			lg.Warn("the scrollback budget could not reach one helper route", "error", err)
			continue
		}
		for _, t := range targets {
			if err := t.carrier.SetScrollback(ctx, t.id, lines); err != nil {
				lg.Warn("the pane's scrollback budget could not be updated", "session", t.id.Session, "error", err)
			}
		}
	}
}

// scrollbackTargets walks THIS machine's helper's held panes: each pane's
// own connection, because a re-adopted pane rides the daemon that holds it,
// not whichever connection is newest. A pane whose connection could not be
// resolved is skipped — it is detaching or lost, and there is nothing left
// on it to configure.
func (o *localHelperOpener) scrollbackTargets(ctx context.Context) ([]scrollbackTarget, error) {
	o.mu.Lock()
	ids := make([]coresession.ID, 0, len(o.held))
	for sid := range o.held {
		ids = append(ids, sid)
	}
	o.mu.Unlock()

	out := make([]scrollbackTarget, 0, len(ids))
	for _, sid := range ids {
		c, id, err := o.screenClient(ctx, string(sid))
		if err != nil {
			continue
		}
		out = append(out, scrollbackTarget{carrier: c, id: id})
	}
	return out, nil
}

// scrollbackTargets walks the far registry: every live helper generation,
// and every session each one reports — a helper only ever reports its own
// sessions, so pushing to what it reports cannot miss one.
func (r *helperRegistry) scrollbackTargets(ctx context.Context) ([]scrollbackTarget, error) {
	r.mu.Lock()
	hosts := make([]*hostHelper, 0, len(r.hosts))
	for _, h := range r.hosts {
		hosts = append(hosts, h)
	}
	r.mu.Unlock()

	out := make([]scrollbackTarget, 0, len(hosts))
	for _, h := range hosts {
		h.mu.Lock()
		c := h.client
		h.mu.Unlock()
		if c == nil {
			continue
		}
		entries, err := c.Sessions(ctx)
		if err != nil {
			// One generation that cannot answer is skipped; the rest are
			// still reached. The fan-out's contract is best-effort.
			continue
		}
		for _, e := range entries {
			out = append(out, scrollbackTarget{carrier: c, id: e.HostSessionID})
		}
	}
	return out, nil
}

// watchScrollback stores what a new spawn is born with and fans every
// change out to the sessions that are already running — the half the row
// buffers never had, because their helper half could only ride a spawn.
//
// The apply runs under a timeout because a notifier callback that parks
// holds every listener behind it: a wedged helper connection must cost the
// push, not the settings pipeline.
func watchScrollback(reg *settings.Registry, s *scrollbackSetting, f *scrollbackFanout) {
	reg.AddNotifier(func(_ int, keys []string) {
		for _, k := range keys {
			if k != settings.TerminalScrollbackLines.Key() {
				continue
			}
			v, err := reg.GetNumber(settings.TerminalScrollbackLines)
			if err != nil {
				continue
			}
			s.lines.Store(uint64(v))
			ctx, cancel := context.WithTimeout(context.Background(), pushScrollbackTimeout)
			defer cancel()
			f.apply(ctx, uint64(v))
			return
		}
	})
}

// pushScrollbackTimeout bounds one fan-out across every live helper.
const pushScrollbackTimeout = 30 * time.Second
