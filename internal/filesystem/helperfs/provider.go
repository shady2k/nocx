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

var _ filesystem.Provider = (*Provider)(nil)

func New(reg Registry, connectionID, rootPath string) *Provider {
	return &Provider{reg: reg, connectionID: connectionID, rootPath: rootPath}
}

func (p *Provider) unavailable() *filesystem.ErrUnavailable {
	status, known := p.reg.Axis(p.connectionID)
	if !known {
		return &filesystem.ErrUnavailable{Reason: string(farhelper.ReasonConnection), Action: string(farhelper.ActionNone)}
	}
	if status.Reason == "" {
		return &filesystem.ErrUnavailable{Reason: string(farhelper.ReasonHelperLost), Action: string(farhelper.ActionNone)}
	}
	return &filesystem.ErrUnavailable{Reason: string(status.Reason), Action: string(status.Action)}
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
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(lease.Ctx, cancel)
	defer stop()
	if lease.Ctx.Err() != nil {
		return p.unavailable()
	}
	err = lease.Client().Call(child, proto.ServiceFS, op, params, out)
	if lease.Ctx.Err() != nil || errors.Is(err, client.ErrLost) {
		return p.unavailable()
	}
	return classifyRefusal(name, params, err)
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

func (*Provider) Watch(context.Context, string) (filesystem.Watch, error) {
	return nil, &filesystem.ErrWatchUnavailable{}
}

// The registry owns the clients and their lifetimes.
func (*Provider) Close() error { return nil }
