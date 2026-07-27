package ssh

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/shady2k/nocx/internal/profile"
)

// CanonicalEndpoint represents a resolved SSH endpoint with canonical host and port.
// ADR-0013: used for TrustedEndpoints grants and authorization checks.
type CanonicalEndpoint struct {
	Host string // Canonical hostname (after SSH config resolution)
	Port uint16 // Effective port (1-65535)
}

// String returns the endpoint in host:port format.
func (e CanonicalEndpoint) String() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(int(e.Port)))
}

// Equals checks if two endpoints are identical.
func (e CanonicalEndpoint) Equals(other CanonicalEndpoint) bool {
	return e.Host == other.Host && e.Port == other.Port
}

// EndpointResolver resolves a profile to its canonical endpoint.
type EndpointResolver struct {
	client *RealClient
}

// NewEndpointResolver creates a new endpoint resolver.
func NewEndpointResolver(client *RealClient) *EndpointResolver {
	return &EndpointResolver{client: client}
}

// ResolveEndpoint resolves a profile to its canonical endpoint.
// Precedence: explicit profile values > ~/.ssh/config > defaults (port 22).
// ADR-0013 §4: endpoint identity has one owner.
func (r *EndpointResolver) ResolveEndpoint(p profile.SSHProfile) (CanonicalEndpoint, error) {
	// Use existing resolveConfig to get merged configuration
	cfg := &ConnectConfig{
		User: p.Options.User,
		Port: p.Options.Port,
	}

	resolved, err := r.client.resolveConfig(p.Options.Host, cfg)
	if err != nil {
		return CanonicalEndpoint{}, fmt.Errorf("resolve config: %w", err)
	}

	// Validate port range
	if resolved.port < 1 || resolved.port > 65535 {
		return CanonicalEndpoint{}, fmt.Errorf("invalid port %d: must be 1-65535", resolved.port)
	}

	// Canonicalize host (lowercase, trim whitespace)
	host := strings.ToLower(strings.TrimSpace(resolved.hostName))
	if host == "" {
		return CanonicalEndpoint{}, fmt.Errorf("empty hostname after resolution")
	}

	return CanonicalEndpoint{
		Host: host,
		Port: uint16(resolved.port),
	}, nil
}

// CheckGrant verifies that a credential has a grant for the given profile and endpoint.
// ADR-0013 §3: connection attempts never expand trust.
func CheckGrant(cred profile.Credential, profileID string, endpoint CanonicalEndpoint) error {
	for _, grant := range cred.TrustedEndpoints {
		if grant.ProfileID == profileID &&
			strings.ToLower(strings.TrimSpace(grant.Host)) == strings.ToLower(strings.TrimSpace(endpoint.Host)) &&
			grant.Port == endpoint.Port {
			return nil
		}
	}
	return fmt.Errorf("credential %s has no grant for profile %s at %s", cred.ID, profileID, endpoint)
}
