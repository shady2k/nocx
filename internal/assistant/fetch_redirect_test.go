package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/httppolicy"
)

func directFetchRoutes() apisend.Routes {
	return func(_ context.Context, routeID string) (httppolicy.Route, error) {
		if routeID != "" {
			return nil, fmt.Errorf("unexpected route %q", routeID)
		}
		return httppolicy.Local(), nil
	}
}

func fetchThrough(t *testing.T, scope *agenttools.URLScope, target string) (string, error) {
	t.Helper()
	ctx := withToolBound(context.Background(), agenttools.ResultBound{
		MaxBytes: 64 << 10, Truncation: agenttools.TruncationDropTail,
	})
	seams := toolSeams{
		fetcher:   apifetch.New(directFetchRoutes(), nil),
		snapshots: newRunSnapshots(),
		runID:     "run-redirect",
	}
	args, err := json.Marshal(map[string]string{"url": target})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return executeFetchURL(ctx, scope, args, seams)
}

// A redirect lands on a URL nobody resolved, and the grant is asked again
// there. Without the per-hop check the chain walks off the endpoint the
// person granted and the document comes back as though it had been fetched
// from inside it (design §5.4, and the redirect rule httpguard.go states).
func TestFetchURLRefusesARedirectOffTheGrantedEndpoint(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("the document at the other endpoint"))
	}))
	defer elsewhere.Close()

	granted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/moved", http.StatusFound)
	}))
	defer granted.Close()

	scope := &agenttools.URLScope{Endpoints: []content.GrantScope{
		{Kind: content.ResourceDestination, ID: granted.URL},
	}}

	out, err := fetchThrough(t, scope, granted.URL+"/start")
	if err == nil {
		t.Fatalf("the redirect off the granted endpoint was followed: %s", out)
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("err = %v, want a refusal naming the grant", err)
	}
	if strings.Contains(out, "the other endpoint") {
		t.Errorf("the body from the other endpoint was returned: %s", out)
	}
}

// A redirect that stays on the granted endpoint is ordinary and is followed.
// Without this row, "refuse every redirect" would pass the test above.
func TestFetchURLFollowsARedirectInsideTheGrantedEndpoint(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			_, _ = w.Write([]byte("the document at the granted endpoint"))
			return
		}
		http.Redirect(w, r, srv.URL+"/moved", http.StatusFound)
	}))
	defer srv.Close()

	scope := &agenttools.URLScope{Endpoints: []content.GrantScope{
		{Kind: content.ResourceDestination, ID: srv.URL},
	}}

	out, err := fetchThrough(t, scope, srv.URL+"/start")
	if err != nil {
		t.Fatalf("a redirect inside the granted endpoint was refused: %v", err)
	}
	if !strings.Contains(out, "the document at the granted endpoint") {
		t.Fatalf("result = %s, want the redirected document", out)
	}
}

// The subdomain marker is honoured on the hop too: what the grant covers is
// one question, asked in one place, whichever hop asks it.
func TestFetchURLRedirectAsksTheSameContainmentPredicate(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("off-endpoint"))
	}))
	defer elsewhere.Close()

	granted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/moved", http.StatusFound)
	}))
	defer granted.Close()

	scope := &agenttools.URLScope{Endpoints: []content.GrantScope{
		{Kind: content.ResourceDestination, ID: granted.URL},
	}}
	// The capability and the hop agree: neither covers the other server.
	if scope.Allows(elsewhere.URL + "/moved") {
		t.Fatal("the capability covers the other endpoint; this test proves nothing")
	}
	if _, err := fetchThrough(t, scope, granted.URL+"/start"); err == nil {
		t.Fatal("the hop allowed what the capability refuses")
	}
}
