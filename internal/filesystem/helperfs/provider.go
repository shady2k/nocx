// Package helperfs reaches a pane's remote filesystem through its far helper.
package helperfs

import (
	"context"
	"errors"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// Registry is the connection's helper axis, not a way to start a helper.
type Registry interface {
	Acquire(context.Context, string) (*farhelper.Lease, error)
	Axis(string) (farhelper.Status, bool)
	Watch(func(string, farhelper.Status)) func()
}

// Provider holds a connection identity, not a client or an SSH connection.
type Provider struct {
	reg          Registry
	connectionID string
	rootPath     string
}

var (
	_ filesystem.Provider     = (*Provider)(nil)
	_ filesystem.AxisProvider = (*Provider)(nil)
)

func New(reg Registry, connectionID, rootPath string) *Provider {
	return &Provider{reg: reg, connectionID: connectionID, rootPath: rootPath}
}

// HelperAxis reports the connection's helper axis as the registry holds it
// now, or the axis a binding reports when the connection is no longer known to
// it. A helper-backed binding always has an axis — that is what makes it a
// helper-backed binding — so the bool is always true here; the false case
// belongs to providers with no axis at all, and this one is not among them.
//
// New performs no I/O, so a provider and its binding exist even while the
// axis is unavailable (C28): the panel must be able to draw the reason on a
// host where nothing is serving, which is exactly the moment a dial would
// fail.
func (p *Provider) HelperAxis() (filesystem.HelperAxis, bool) {
	return p.axis(), true
}

// axis is the one place the binding's axis is derived from the registry, so
// the reason a panel draws and the reason a failed call carries are the same
// answer rather than two that agree until they do not (C28).
func (p *Provider) axis() filesystem.HelperAxis {
	status, known := p.reg.Axis(p.connectionID)
	if !known {
		// The connection was unbound, or never reached the registry: the
		// panel's reason is the connection itself, not a helper that this
		// host may well be serving.
		return filesystem.HelperAxis{
			State:  string(farhelper.StateUnavailable),
			Reason: string(farhelper.ReasonConnection),
			Action: string(farhelper.ActionNone),
		}
	}
	axis := filesystem.HelperAxis{State: string(status.State), Reason: string(status.Reason), Action: string(status.Action)}
	if axis.State == string(farhelper.StateUnavailable) && axis.Reason == "" {
		// An unavailable axis always carries a reason. One that does not is a
		// publisher this code cannot read, and the helper being gone is the
		// conservative reading — the alternative is a panel drawing an empty
		// reason beside an error that names one.
		axis.Reason = string(farhelper.ReasonHelperLost)
		axis.Action = string(farhelper.ActionNone)
	}
	return axis
}

func (p *Provider) unavailable() *filesystem.ErrUnavailable {
	axis := p.axis()
	if axis.Reason == "" {
		// The lease was lost while the axis still reads serving, or a state
		// that publishes no reason could not be used. The helper is what is
		// missing, and this is the one place the refusal adds that reading to
		// the axis rather than reporting it beside it as if it were a state.
		axis.Reason = string(farhelper.ReasonHelperLost)
		axis.Action = string(farhelper.ActionNone)
	}
	return &filesystem.ErrUnavailable{Reason: axis.Reason, Action: axis.Action}
}

func (p *Provider) acquire(ctx context.Context) (*farhelper.Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lease, err := p.reg.Acquire(ctx, p.connectionID)
	var unavailable *farhelper.Unavailable
	if errors.As(err, &unavailable) || (err == nil && (lease == nil || lease.Client() == nil || lease.Ctx == nil || lease.Ctx.Err() != nil)) {
		return nil, p.unavailable()
	}
	return lease, err
}

func (p *Provider) call(ctx context.Context, op, name string, params, out any) error {
	lease, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	return p.callLease(ctx, lease, op, name, params, out)
}

// callLease runs one request on a lease already in hand, so a caller that must
// refuse a peer can check it first and still use the one request path every
// other call takes: cancellation follows the lease, and a lost lane becomes the
// axis's own reason rather than the transport's.
func (p *Provider) callLease(ctx context.Context, lease *farhelper.Lease, op, name string, params, out any) error {
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(lease.Ctx, cancel)
	defer stop()
	if lease.Ctx.Err() != nil {
		return p.unavailable()
	}
	err := lease.Client().Call(child, proto.ServiceFS, op, params, out)
	if lease.Ctx.Err() != nil || errors.Is(err, client.ErrLost) {
		return p.unavailable()
	}
	return classifyRefusal(name, params, err)
}

// openingLease acquires the axis and refuses every peer but the one that opened
// the write session. A helper that never saw the session answers the next chunk
// or the replay with bad-session, which is not an answer about this upload, and
// the bytes must not be offered to it in the first place.
func (p *Provider) openingLease(ctx context.Context, opening *client.Client) (*farhelper.Lease, error) {
	lease, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if lease.Ctx.Err() != nil || lease.Client() != opening {
		return nil, p.unavailable()
	}
	return lease, nil
}

func (p *Provider) Root(ctx context.Context) (filesystem.Root, error) {
	var out proto.FSRootResult
	err := p.call(ctx, proto.OpRoot, p.rootPath, proto.FSRootParams{Path: p.rootPath}, &out)
	return filesystem.Root(out), err
}

func (p *Provider) Stat(ctx context.Context, name string) (filesystem.Stat, error) {
	out, err := p.stat(ctx, name)
	return filesystem.Stat{Kind: out.Kind}, err
}

func (p *Provider) stat(ctx context.Context, name string) (proto.FSStatResult, error) {
	var out proto.FSStatResult
	err := p.call(ctx, proto.OpStat, name, proto.FSPathParams{Path: name}, &out)
	return out, err
}

func (p *Provider) Canonical(ctx context.Context, name string) (string, error) {
	var out proto.FSCanonicalResult
	err := p.call(ctx, proto.OpCanonical, name, proto.FSPathParams{Path: name}, &out)
	return out.Canonical, err
}

func (p *Provider) List(ctx context.Context, name string, page filesystem.Page) (filesystem.Listing, error) {
	var out proto.FSListResult
	if err := p.call(ctx, proto.OpList, name, proto.FSListParams{Path: name, Offset: page.Offset, Limit: page.Limit}, &out); err != nil {
		return filesystem.Listing{}, err
	}
	entries := make([]filesystem.Entry, 0, len(out.Entries))
	for _, e := range out.Entries {
		// Unreadable rows have no metadata, as on the local provider.
		var mtime time.Time
		if e.Kind != filesystem.KindUnreadable {
			mtime = time.Unix(0, e.MtimeNs)
		}
		entries = append(entries, filesystem.Entry{Name: e.Name, Path: e.Path, Kind: e.Kind, LinkTarget: e.LinkTarget, LinkKind: e.LinkKind, Size: e.Size, ModTime: mtime, Mode: e.Mode})
	}
	return filesystem.Listing{Path: out.Path, Canonical: out.Canonical, Entries: entries, Offset: out.Offset, Total: out.Total, HasMore: out.HasMore, Rev: out.Rev}, nil
}

func (p *Provider) Read(ctx context.Context, name string, maxBytes int64) (filesystem.Content, error) {
	limit := filesystem.ReadLimit(maxBytes)
	out, err := p.readRange(ctx, name, 0, limit+1)
	if err != nil {
		return filesystem.Content{}, err
	}
	truncated := int64(len(out.Data)) > limit
	if truncated {
		out.Data = out.Data[:limit]
	}
	return filesystem.PreviewContent(name, out.Canonical, out.Data, time.Unix(0, out.MtimeNs), truncated, out.Changed), nil
}

func (p *Provider) readRange(ctx context.Context, name string, offset, length int64) (proto.FSReadResult, error) {
	var out proto.FSReadResult
	err := p.call(ctx, proto.OpRead, name, proto.FSReadParams{Path: name, Offset: offset, Length: length}, &out)
	return out, err
}

// readWhole asks for one whole-file read. It is separate from readRange
// because the two questions differ: a range reports the object's identity, a
// whole read reports the version of exactly the bytes returned, and the host
// refuses a whole read that did not start at zero.
func (p *Provider) readWhole(ctx context.Context, name string, limit int64) (proto.FSReadResult, error) {
	var out proto.FSReadResult
	err := p.call(ctx, proto.OpRead, name, proto.FSReadParams{Path: name, Offset: 0, Length: limit, Whole: true}, &out)
	return out, err
}

func (*Provider) Watch(context.Context, string) (filesystem.Watch, error) {
	return nil, &filesystem.ErrWatchUnavailable{}
}

// The registry owns the clients and their lifetimes.
func (*Provider) Close() error { return nil }
