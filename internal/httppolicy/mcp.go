package httppolicy

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
)

// MCPPolicy opts into the stricter outbound MCP rules without changing the
// existing API/provider policy. OAuth endpoints permit cleartext only at
// loopback, and private HTTPS only at the configured MCP origin or loopback.
type MCPPolicy struct {
	OAuth    bool
	Endpoint string
}

var ErrMCPDestination = errors.New("MCP destination refused")

// NewMCPTransport shares this package's resolve-and-dial owner and route seams.
// Neither HTTP nor HTTPS may use environment proxies; both pin the validated IPs.
func NewMCPTransport(p Params, policy MCPPolicy) *Transport {
	t := NewTransport(p)
	t.mcp = &policy
	return t
}

// ValidateURL checks an explicit browser destination. Network requests still
// validate again in RoundTrip, pinning that request's answer through dial.
func (t *Transport) ValidateURL(ctx context.Context, u *url.URL) error {
	_, err := t.check(ctx, u)
	return err
}

func (t *Transport) checkMCP(ctx context.Context, u *url.URL) (dialSpec, error) {
	if u == nil || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Hostname() == "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return dialSpec{}, ErrMCPDestination
	}
	host := u.Hostname()
	ips, err := t.route.LookupIP(ctx, host)
	if err != nil {
		if ctx.Err() != nil {
			return dialSpec{}, ctx.Err()
		}
		return dialSpec{}, ErrMCPDestination
	}
	if len(ips) == 0 {
		return dialSpec{}, ErrMCPDestination
	}
	endpoint, err := url.Parse(t.mcp.Endpoint)
	if err != nil {
		return dialSpec{}, ErrMCPDestination
	}
	for _, ip := range ips {
		if forbiddenMCPAddress(ip) {
			return dialSpec{}, ErrMCPDestination
		}
		if u.Scheme == "http" && (!ip.IsLoopback() && (t.mcp.OAuth || !ip.IsPrivate())) {
			return dialSpec{}, ErrMCPDestination
		}
		if t.mcp.OAuth && !ip.IsLoopback() && ip.IsPrivate() && !sameMCPEndpoint(u, endpoint) {
			return dialSpec{}, ErrMCPDestination
		}
	}
	return dialSpec{scheme: u.Scheme, host: host, port: portFor(u), ips: ips}, nil
}

var mcpIPv6Metadata = net.ParseIP("fd00:ec2::254")

func forbiddenMCPAddress(ip net.IP) bool {
	return ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.Equal(net.IPv4(100, 100, 100, 200)) || ip.Equal(net.IPv4(168, 63, 129, 16)) || ip.Equal(mcpIPv6Metadata) ||
		(!ip.IsLoopback() && !ip.IsGlobalUnicast())
}

func sameMCPEndpoint(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) && portFor(a) == portFor(b)
}
