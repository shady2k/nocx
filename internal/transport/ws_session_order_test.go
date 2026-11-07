package transport

// Stage 6 of ADR-8eej3e66, over the real socket: the session, content and API
// domains order per resource, not per domain.
//
// The row this file proves is the constraint table's "unrelated work completes
// beside a blocked domain": one tab's queue can be saturated with work that
// cannot finish, and a person's action in another tab still completes inside
// its declared deadline. Under the domain gate that was impossible — capacity
// one, and a queue of eight behind which every other request of the domain
// waited or was refused.

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

func TestPersonActionCompletesBesideSaturatedOtherSessions(t *testing.T) {
	// Session A's channel blocks every window-change for ever; session B's
	// applies. A stands for background work that cannot finish, B for the
	// person at the keyboard.
	blocked := newGatedResizeChannel()
	live := newLiveChannel()
	ws := stallServer(t, blocked, live)
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	saturated := openSSHOverSocket(t, ws, conn, 1)
	person := openSSHOverSocket(t, ws, conn, 2)

	// Saturate one session's own queue: every resize the connection may have
	// outstanding at A, each behind the one in flight. None of them can
	// complete, so nothing but the first is running — a queue of work that
	// cannot finish.
	//
	// THE WINDOW IS THE BOUND (ADR-8eej3e66 §7): a connection may have the
	// advertised number of requests in flight and the renderer queues beyond
	// it, so a client that kept one more would be the peer the backend
	// disconnects. One credit is left for the person's own resize below.
	const background = DefaultCreditWindow - 1
	for i := 0; i < background; i++ {
		sendResizeRaw(t, conn, saturated, 100, 30, 100+i)
	}
	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the background resize never started")
	}

	// The person resizes the other tab. It must complete while A's queue is
	// still exactly where it was: a different session is a different
	// resource, and the domain is not held.
	start := time.Now()
	sendResizeRaw(t, conn, person, 120, 40, 999)
	resp := collectResponses(t, conn, []int{999}, 5*time.Second)[999]
	elapsed := time.Since(start)
	assertResponseOK(t, resp, 999)
	if elapsed > time.Second {
		t.Fatalf("the person's resize took %v, past its declared turn and max: it waited behind another session's queue",
			elapsed)
	}
	select {
	case <-blocked.returned:
		t.Fatal("the saturated session's blocked resize returned; the test did not hold it")
	default:
	}
	if got := blocked.appliedResizes(); len(got) != 0 {
		t.Fatalf("the saturated session applied %d resizes; nothing should have completed there", len(got))
	}
	t.Logf("person's resize answered in %v beside %d queued resizes on another session", elapsed, background)
}

// TestTheCompositionRootOrdersTheThreeDomainsPerResource is the wiring half:
// the session, content and API domains are given per-resource FIFOs and no
// domain gate, the session gate survives only for the domains that still stage
// a session read, and the three key namespaces do not overlap on any key their
// methods produce — so no FIFO can hold a resource another owns.
func TestTheCompositionRootOrdersTheThreeDomainsPerResource(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))

	names := make(map[string]bool, len(ws.diagAdmits))
	for _, a := range ws.diagAdmits {
		names[a.Name()] = true
	}
	for _, want := range []string{"session-order", "content-order", "api-order"} {
		if !names[want] {
			t.Errorf("the composition root does not build %q; admissions are %v", want, names)
		}
	}
	for _, gone := range []string{"content", "api"} {
		if names[gone] {
			t.Errorf("the %q domain gate is still built; stage 6 replaced it", gone)
		}
	}
	if !names["session"] {
		t.Error("the session gate is gone, but shell, git and filesystem still stage a session read under it")
	}

	// The one textual overlap between two FIFOs is the layout collection:
	// content's key is "collection:workspace-layout" and the api prefix is
	// "collection:". No api invocation can produce that key — a handle must
	// be the 32-hex id the backend minted and a folder path must be absolute
	// — so the two FIFOs never hold one real collection. These two checks
	// are what make that a fact rather than a hope.
	if msg := validateAPIHandle("workspace-layout"); msg == "" {
		t.Error("a non-hex string passed the api handle validator; an api key could then name the layout collection")
	}
	if msg := validateAPIFolderPath("workspace-layout", "path"); msg == "" {
		t.Error("a relative string passed the api folder validator; an api key could then name the layout collection")
	}
}
