package apifetch_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/httppolicy"
)

func directTextRoutes() apisend.Routes {
	return func(_ context.Context, routeID string) (httppolicy.Route, error) {
		if routeID != "" {
			return nil, fmt.Errorf("unexpected route %q", routeID)
		}
		return httppolicy.Local(), nil
	}
}

func routedTextRoutes(resolve httppolicy.ResolverFunc) apisend.Routes {
	return func(_ context.Context, routeID string) (httppolicy.Route, error) {
		if routeID != "" {
			return nil, fmt.Errorf("unexpected route %q", routeID)
		}
		return httppolicy.NewRoute(resolve, httppolicy.DialerFunc((&net.Dialer{}).DialContext), nil), nil
	}
}

func TestFetchTextReturnsUTF8TextAndMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>Hello from the page</body></html>"))
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err != nil {
		t.Fatalf("FetchText: %v", err)
	}
	if got.URL != srv.URL || got.ContentType != "text/html; charset=utf-8" {
		t.Fatalf("metadata = %+v, want URL and content type", got)
	}
	if got.Text != "<html><body>Hello from the page</body></html>" || got.Lossy {
		t.Fatalf("result = %+v, want complete lossless text", got)
	}
}

// httppolicy deliberately permits private and loopback HTTP for local services.
// The refusal half of the fetch acceptance therefore targets the policy's real
// unsafe-HTTP sentence rather than adding a second SSRF rule in apifetch.
func TestFetchTextRefusesUnsafeHTTPAddressAndOversize(t *testing.T) {
	unsafeRoutes := routedTextRoutes(func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.7")}, nil
	})
	_, err := apifetch.New(unsafeRoutes, nil).FetchText(context.Background(), apifetch.TextRequest{URL: "http://public.example/page", MaxBytes: 64 << 10})
	if err == nil || !strings.Contains(err.Error(), "not a loopback or private address") {
		t.Fatalf("unsafe HTTP error = %v, want the shared policy's stated refusal", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 129)))
	}))
	defer srv.Close()
	_, err = apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 128})
	if !errors.Is(err, apifetch.ErrTooLarge) || !strings.Contains(err.Error(), "128") {
		t.Fatalf("oversize error = %v, want ErrTooLarge and limit", err)
	}
}

func TestFetchTextPrivateHTTPUsesTheSharedPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("local service"))
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err != nil || got.Text != "local service" {
		t.Fatalf("private HTTP result = %+v, err = %v; want policy-permitted local text", got, err)
	}
}

func TestFetchTextClassifiesBodyRatherThanContentType(t *testing.T) {
	binaryPNG := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x01, 0x02, 0x03}
	cases := []struct {
		name        string
		body        []byte
		contentType string
		wantErr     bool
	}{
		{"binary PNG body", binaryPNG, "image/png", true},
		{"three UTF-8 replacements", []byte{0xff, 0xfd, 0xfc}, "text/plain; charset=utf-8", true},
		{"text body with stray NUL", []byte("plain\x00response"), "text/plain", true},
		{"missing content type", []byte("plain response"), "", false},
		{"text/xml", []byte("<rss/>"), "text/xml", false},
		{"RSS XML", []byte("<feed/>"), "application/rss+xml", false},
		{"CSV", []byte("name,value\n"), "text/csv", false},
		{"unknown type", []byte("plain response"), "application/x-made-up", false},
		{"empty body", nil, "application/x-made-up", false},
		{"one-byte body", []byte("x"), "application/x-made-up", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				_, _ = w.Write(tc.body)
			}))
			defer srv.Close()

			got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
			if tc.wantErr {
				if !errors.Is(err, apifetch.ErrNotText) {
					t.Fatalf("result = %+v, error = %v; want ErrNotText", got, err)
				}
				return
			}
			if err != nil || got.Text != string(tc.body) {
				t.Fatalf("result = %+v, err = %v; want body text", got, err)
			}
		})
	}
}

func TestFetchTextDecodesDeclaredWindows1251(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=windows-1251")
		_, _ = w.Write([]byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2})
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err != nil {
		t.Fatalf("FetchText: %v", err)
	}
	if got.Text != "Привет" || got.Lossy {
		t.Fatalf("result = %+v, want UTF-8 Привет without loss", got)
	}
}

func TestFetchTextSniffsHTMLMetaCharset(t *testing.T) {
	body := append([]byte(`<meta charset="windows-1251"><p>`), []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err != nil || got.Text != `<meta charset="windows-1251"><p>Привет` || got.Lossy {
		t.Fatalf("result = %+v, err = %v; want meta-decoded UTF-8", got, err)
	}
}

func TestFetchTextMarksReplacementFromDeclaredCharsetLossy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte{0xff})
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err != nil {
		t.Fatalf("FetchText: %v", err)
	}
	if got.Text != "\ufffd" || !got.Lossy {
		t.Fatalf("result = %+v, want replacement text with lossy=true", got)
	}
}

func TestFetchTextRefusesUnknownDeclaredCharset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=x-made-up-charset")
		_, _ = w.Write([]byte("plain response"))
	}))
	defer srv.Close()

	_, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err == nil || !strings.Contains(err.Error(), "x-made-up-charset") {
		t.Fatalf("error = %v, want refusal naming unsupported charset", err)
	}
}

func TestFetchTextWithoutCharsetKeepsUTF8(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Привет"))
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err != nil || got.Text != "Привет" || got.Lossy {
		t.Fatalf("result = %+v, err = %v; want lossless UTF-8", got, err)
	}
}

func TestFetchTextHonorsDeclaredCharsetOverWireUTF8(t *testing.T) {
	const wireText = "Привет"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=windows-1251")
		_, _ = w.Write([]byte(wireText))
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err != nil {
		t.Fatalf("FetchText: %v", err)
	}
	if got.Text != "РџСЂРёРІРµС‚" || got.Lossy {
		t.Fatalf("result = %+v, want header-decoded mojibake without loss", got)
	}
}

func TestFetchTextRejectsTranscodedBodyOverCeiling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=windows-1251")
		_, _ = w.Write([]byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2})
	}))
	defer srv.Close()

	_, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 6})
	if !errors.Is(err, apifetch.ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge after UTF-8 expansion", err)
	}
}

func TestFetchTextDNSFailureHasPairedSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("resolved"))
	}))
	defer srv.Close()

	routes := routedTextRoutes(func(ctx context.Context, host string) ([]net.IP, error) {
		if host == "dns-failure.test" {
			return nil, errors.New("resolver unavailable")
		}
		return httppolicy.SystemResolver().LookupIP(ctx, host)
	})
	_, err := apifetch.New(routes, nil).FetchText(context.Background(), apifetch.TextRequest{URL: "http://dns-failure.test/page", MaxBytes: 64 << 10})
	if err == nil || !strings.Contains(err.Error(), "resolver unavailable") {
		t.Fatalf("DNS error = %v, want resolver failure", err)
	}
	got, err := apifetch.New(routes, nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err != nil || got.Text != "resolved" {
		t.Fatalf("ordinary URL = %+v, err = %v; want success after DNS failure case", got, err)
	}
}

func TestFetchTextConnectionRefusedHasPairedSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("connected"))
	}))
	closedURL := srv.URL
	srv.Close()

	_, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: closedURL, MaxBytes: 64 << 10})
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("connection error = %v, want refused local address", err)
	}

	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("connected"))
	}))
	defer open.Close()
	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: open.URL, MaxBytes: 64 << 10})
	if err != nil || got.Text != "connected" {
		t.Fatalf("ordinary URL = %+v, err = %v; want connection success", got, err)
	}
}

func TestFetchTextTLSFailureHasPairedSuccess(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("secure"))
	}))
	defer tlsSrv.Close()

	_, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: tlsSrv.URL, MaxBytes: 64 << 10})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "certificate") {
		t.Fatalf("TLS error = %v, want certificate failure", err)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ordinary"))
	}))
	defer plain.Close()
	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: plain.URL, MaxBytes: 64 << 10})
	if err != nil || got.Text != "ordinary" {
		t.Fatalf("ordinary URL = %+v, err = %v; want success after TLS failure case", got, err)
	}
}

func TestFetchTextRedirectChainPastBoundHasPairedSuccess(t *testing.T) {
	var redirector *httptest.Server
	redirector = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirector.URL+"/again", http.StatusFound)
	}))
	defer redirector.Close()

	_, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: redirector.URL, MaxBytes: 64 << 10})
	if err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("redirect error = %v, want the chain bound", err)
	}

	ordinary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("one hop"))
	}))
	defer ordinary.Close()
	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: ordinary.URL, MaxBytes: 64 << 10})
	if err != nil || got.Text != "one hop" {
		t.Fatalf("ordinary URL = %+v, err = %v; want success", got, err)
	}
}

func TestFetchTextRedirectToUnsafeHTTPIsRefusedAtTheHop(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://public.example/hop", http.StatusFound)
	}))
	defer redirector.Close()

	routes := routedTextRoutes(func(ctx context.Context, host string) ([]net.IP, error) {
		if host == "public.example" {
			return []net.IP{net.ParseIP("203.0.113.7")}, nil
		}
		return httppolicy.SystemResolver().LookupIP(ctx, host)
	})
	_, err := apifetch.New(routes, nil).FetchText(context.Background(), apifetch.TextRequest{URL: redirector.URL, MaxBytes: 64 << 10})
	if err == nil || !strings.Contains(err.Error(), "not a loopback or private address") {
		t.Fatalf("redirect error = %v, want policy refusal at the redirect hop", err)
	}
}

func TestFetchTextBodyThatStopsMidStreamHasPairedSuccess(t *testing.T) {
	truncated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h, ok := w.(http.Hijacker)
		if !ok {
			t.Error("server does not support hijacking")
			return
		}
		conn, rw, err := h.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_, _ = fmt.Fprint(rw, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 100\r\n\r\nshort")
		_ = rw.Flush()
		_ = conn.Close()
	}))
	truncatedURL := truncated.URL
	defer truncated.Close()

	_, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: truncatedURL, MaxBytes: 64 << 10})
	if err == nil {
		t.Fatal("mid-stream body was accepted as complete text")
	}

	ordinary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("complete"))
	}))
	defer ordinary.Close()
	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: ordinary.URL, MaxBytes: 64 << 10})
	if err != nil || got.Text != "complete" {
		t.Fatalf("ordinary URL = %+v, err = %v; want complete text", got, err)
	}
}

func TestFetchTextKeepsOriginalURLAcrossRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("redirected"))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{
		URL: redirector.URL, MaxBytes: 64 << 10,
	})
	if err != nil {
		t.Fatalf("FetchText: %v", err)
	}
	if got.URL != redirector.URL || got.Text != "redirected" {
		t.Fatalf("result = %+v, want original URL identity and redirected body", got)
	}
}

// TestFetchTextSameOriginOnlyRefusesACrossOriginRedirect and its paired
// success below are the two halves of one rule: a caller that resolves
// relative paths against an address it was given (internal/skill's bundle)
// must be answered by that origin and no other, at every hop and not only on
// the first request.
func TestFetchTextSameOriginOnlyRefusesACrossOriginRedirect(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("bytes from an origin the caller never named"))
	}))
	defer elsewhere.Close()
	// TWO hops, deliberately: the first stays home and the second leaves. A
	// same-host check on the initial request passes this chain.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			http.Redirect(w, r, srv.URL+"/second", http.StatusFound)
			return
		}
		http.Redirect(w, r, elsewhere.URL+"/third", http.StatusFound)
	}))
	defer srv.Close()

	_, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{
		URL: srv.URL + "/first", MaxBytes: 64 << 10, SameOriginOnly: true,
	})
	if err == nil {
		t.Fatal("FetchText followed a redirect off the origin it was told not to leave")
	}
	if !strings.Contains(err.Error(), "may only be answered by the origin it named") {
		t.Errorf("error = %v, want the refusal to name the rule", err)
	}
}

func TestFetchTextSameOriginOnlyFollowsARedirectThatStaysHome(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			http.Redirect(w, r, srv.URL+"/second", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("the document, one hop along"))
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{
		URL: srv.URL + "/first", MaxBytes: 64 << 10, SameOriginOnly: true,
	})
	if err != nil {
		t.Fatalf("FetchText: %v", err)
	}
	if got.Text != "the document, one hop along" {
		t.Errorf("text = %q, want the document the redirect pointed at", got.Text)
	}
}

// And the same chain WITHOUT the flag is still followed, so the refusal above
// is the flag's doing and not a redirect rule everything now pays for.
func TestFetchTextWithoutSameOriginOnlyStillCrossesOrigins(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("the document, at its real home"))
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/doc", http.StatusFound)
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{
		URL: srv.URL + "/vanity", MaxBytes: 64 << 10,
	})
	if err != nil {
		t.Fatalf("FetchText: %v", err)
	}
	if got.Text != "the document, at its real home" {
		t.Errorf("text = %q, want the redirect target's document", got.Text)
	}
}
