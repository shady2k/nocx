package httppolicy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestMCPPolicyRefusesUnsafeResolvedAnswersBeforeDial(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		for _, ip := range []string{"0.0.0.0", "::", "169.254.169.254", "::ffff:169.254.169.254", "fe80::1", "fd00:ec2::254", "224.0.0.1", "ff02::1", "100.100.100.200", "168.63.129.16", "255.255.255.255"} {
			t.Run(scheme+ip, func(t *testing.T) {
				var dials atomic.Int32
				route := NewRoute(ResolverFunc(func(context.Context, string) ([]net.IP, error) {
					return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP(ip)}, nil
				}), DialerFunc(func(context.Context, string, string) (net.Conn, error) {
					dials.Add(1)
					return nil, errors.New("unexpected dial")
				}), nil)
				transport := NewMCPTransport(Params{Route: route}, MCPPolicy{Endpoint: scheme + "://fixture/mcp"})
				request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, scheme+"://fixture/mcp", nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = transport.RoundTrip(request)
				if !errors.Is(err, ErrMCPDestination) || dials.Load() != 0 {
					t.Fatalf("err=%v dials=%d", err, dials.Load())
				}
			})
		}
	}
}

func TestMCPPolicyPinsHTTPAndHTTPSAndNeverConsultsProxy(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "https"}[encrypted], func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pinned") }))
			if encrypted {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			u, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			actual := u.Host
			ip := net.ParseIP(u.Hostname())
			u.Host = net.JoinHostPort("fixture.example", u.Port())
			var lookups, proxies atomic.Int32
			dialed := make(chan string, 1)
			route := NewRoute(ResolverFunc(func(context.Context, string) ([]net.IP, error) {
				if lookups.Add(1) != 1 {
					return []net.IP{net.ParseIP("169.254.169.254")}, nil
				}
				return []net.IP{ip}, nil
			}), DialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialed <- addr
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}), func(*http.Request) (*url.URL, error) {
				proxies.Add(1)
				return url.Parse("http://credential:secret@proxy.invalid")
			})
			params := Params{Route: route}
			if encrypted {
				roots := x509.NewCertPool()
				roots.AddCert(server.Certificate())
				params.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: server.Certificate().DNSNames[0], MinVersion: tls.VersionTLS12}
			}
			transport := NewMCPTransport(params, MCPPolicy{Endpoint: u.String()})
			defer transport.Inner.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u.String(), nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if closeErr := resp.Body.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			}()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "pinned" || <-dialed != actual || lookups.Load() != 1 || proxies.Load() != 0 {
				t.Fatal("resolve/dial answer or direct-proxy ownership diverged")
			}
		})
	}
}

func TestMCPOAuthPolicyIsStricterThanServerPolicy(t *testing.T) {
	for _, test := range []struct {
		target, endpoint, ip string
		oauth, allowed       bool
	}{
		{"http://local/mcp", "http://local/mcp", "10.0.0.5", false, true},
		{"http://local/token", "http://local/mcp", "10.0.0.5", true, false},
		{"http://local/token", "http://local/mcp", "127.0.0.1", true, true},
		{"https://local/token", "https://local/mcp", "10.0.0.5", true, true},
		{"https://other/token", "https://local/mcp", "10.0.0.5", true, false},
		{"https://other/token", "https://local/mcp", "203.0.113.5", true, true},
		{"http://public/mcp", "http://public/mcp", "203.0.113.5", false, false},
	} {
		route := NewRoute(ResolverFunc(func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP(test.ip)}, nil }), &net.Dialer{}, nil)
		transport := NewMCPTransport(Params{Route: route}, MCPPolicy{OAuth: test.oauth, Endpoint: test.endpoint})
		u, _ := url.Parse(test.target)
		if err := transport.ValidateURL(t.Context(), u); (err == nil) != test.allowed {
			t.Fatalf("target=%s oauth=%v ip=%s: %v", test.target, test.oauth, test.ip, err)
		}
	}
}
