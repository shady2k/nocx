package pi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
)

func TestReadinessAndModelsShareCatalogue(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "launches")
	script := `#!/bin/sh
if [ "$1" = "auth" ]; then
 [ "$5" = "--no-refresh" ] || exit 2
 exit 0
fi
printf 'launch\n' >> "$LAUNCH_LOG"
while IFS= read -r request; do
 case "$request" in
 *get_available_models*) printf '%s\n' '{"type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"fixture","id":"one"}]}}' ;;
 *get_state*) printf '%s\n' '{"type":"response","command":"get_state","success":true,"data":{"model":{"provider":"fixture","id":"one"}}}' ;;
 esac
done
`
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700); err != nil { //nolint:gosec // executable fixture in private test directory
		t.Fatal(err)
	}
	c := newTestConnector(t.TempDir(), []string{"PATH=" + dir, "LAUNCH_LOG=" + log})
	if ready := c.Readiness(context.Background()); ready.State != harness.Ready {
		t.Fatalf("Readiness = %+v", ready)
	}
	models, err := c.Models(context.Background())
	if err != nil || len(models) != 1 {
		t.Fatalf("Models = %+v, %v", models, err)
	}
	models[0].ID = "caller mutation"
	models, err = c.Models(context.Background())
	if err != nil || models[0].ID != "fixture/one" {
		t.Fatalf("shared catalogue mutated: %+v %v", models, err)
	}
	raw, err := os.ReadFile(log) //nolint:gosec // private fixture launch log
	if err != nil || string(raw) != "launch\n" {
		t.Fatalf("catalogue process launches = %q, %v", raw, err)
	}
}

func TestCatalogueCacheWaitHonorsDeadline(t *testing.T) {
	c := newTestConnector(t.TempDir(), nil)
	c.catalogGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.catalog(ctx); err != context.Canceled {
		t.Fatalf("catalog = %v", err)
	}
	<-c.catalogGate
}

func TestCatalogueRejectsFailedAndIncompleteRPC(t *testing.T) {
	// The fixture consumes every request before it exits, because queryCatalog
	// writes both of its commands before it reads a reply. A fixture that stops
	// after the first command lets the second write race the closing pipe, and
	// the resulting EPIPE then replaces the refusal under assertion. The
	// get_state arm returns without answering, which is what ends the catalogue
	// early in the incomplete case instead of stalling the scan until the
	// readiness deadline.
	const fixture = `while IFS= read -r request; do
case "$request" in
*get_available_models*) printf '%s\n' '<answer>' ;;
*get_state*) exit 0 ;;
esac
done
`
	for _, tc := range []struct{ name, answer, want string }{
		{"failure", `{"type":"response","command":"get_available_models","success":false,"error":"provider unavailable"}`, "provider unavailable"},
		{"incomplete", `{"type":"response","command":"get_available_models","success":true,"data":{"models":[]}}`, "incomplete catalogue"},
		{"malformed", `not json`, "invalid character"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := strings.Replace(fixture, "<answer>", tc.answer, 1)
			if err := os.WriteFile(filepath.Join(dir, "pi"), []byte("#!/bin/sh\n"+script), 0o700); err != nil { //nolint:gosec // executable fixture in private test directory
				t.Fatal(err)
			}
			c := newTestConnector(t.TempDir(), []string{"PATH=" + dir})
			if _, err := c.Models(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Models error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCatalogueCancellationReturnsContextError(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
read request
while :; do :; done
`
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700); err != nil { //nolint:gosec // executable fixture in private test directory
		t.Fatal(err)
	}
	c := newTestConnector(t.TempDir(), []string{"PATH=" + dir})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Models(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Models = %v, want deadline exceeded", err)
	}
}
