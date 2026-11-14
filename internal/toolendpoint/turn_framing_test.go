package toolendpoint

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/toolendpoint/turnbind"
)

type turnCapableAuthorizer struct{ *testAuthorizer }

func (*turnCapableAuthorizer) AdmitsTurnBearer() bool { return true }

func TestTurnCapableEndpointAdmitsSilentLocalImmediately(t *testing.T) {
	admitted := make(chan struct{}, 1)
	auth := &testAuthorizer{invoked: admitted}
	cfg := endpointConfig(t, auth, &testDispatcher{})
	cfg.Auth = &turnCapableAuthorizer{auth}
	ep := startEndpoint(t, cfg)
	_ = dialEndpoint(t, ep)
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("silent local admission waited for bytes")
	}
}

func TestTurnCapableEndpointPreservesJSONFraming(t *testing.T) {
	for _, input := range []string{"false\n", "  {\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"workers.holdings\",\"params\":{}}\n"} {
		t.Run(input, func(t *testing.T) {
			auth := &testAuthorizer{}
			cfg := endpointConfig(t, auth, &testDispatcher{out: `{}`})
			cfg.Auth = &turnCapableAuthorizer{auth}
			ep := startEndpoint(t, cfg)
			conn := dialEndpoint(t, ep)
			if _, err := io.WriteString(conn, input); err != nil {
				t.Fatal(err)
			}
			got := readResponse(t, conn)
			if input[0] == 'f' && (got.Error == nil || got.Error.Code != rpcParseError) {
				t.Fatalf("false response = %+v", got)
			}
			if input[0] == ' ' && got.Error != nil {
				t.Fatalf("whitespace response = %+v", got)
			}
		})
	}
}

func TestEndpointUsesAdmittedDispatcher(t *testing.T) {
	selected := &testDispatcher{out: `{"selected":true}`}
	fallback := &testDispatcher{out: `{"fallback":true}`}
	auth := &testAuthorizer{inv: assistant.ToolInvocation{Dispatcher: selected}}
	ep := startEndpoint(t, endpointConfig(t, auth, fallback))
	conn := dialEndpoint(t, ep)
	callOver(t, conn, "selected")
	if selected.callCount() != 1 || fallback.callCount() != 0 {
		t.Fatalf("selected calls %d fallback %d", selected.callCount(), fallback.callCount())
	}
}

func TestTurnRetirementDoesNotRetainPerTurnFloors(t *testing.T) {
	ep := startEndpoint(t, endpointConfig(t, &testAuthorizer{}, &testDispatcher{}))
	ep.RetireAdmissions(TurnScope("ended"), TurnEpoch)
	ep.RetireAdmissions("pane", 3)
	ep.mu.Lock()
	defer ep.mu.Unlock()
	if len(ep.retired) != 1 || ep.retired["pane"] != 3 {
		t.Fatalf("retired floors = %v, want only pane:3", ep.retired)
	}
}

type selectedCatalogue struct{ testDispatcher }

func (*selectedCatalogue) Catalogue(content.Grant) []agenttools.Tool {
	return []agenttools.Tool{{Declaration: agenttools.Declaration{Name: "selected.tool"}}}
}

func TestEndpointUsesAdmittedCatalogue(t *testing.T) {
	selected := &selectedCatalogue{}
	auth := &testAuthorizer{inv: assistant.ToolInvocation{Dispatcher: selected}}
	ep := startEndpoint(t, endpointConfig(t, auth, &testDispatcher{}))
	conn := dialEndpoint(t, ep)
	_, _ = io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"tools.catalogue","params":{}}`+"\n")
	got := readResponse(t, conn)
	if got.Error != nil || !strings.Contains(string(got.Result), "selected.tool") {
		t.Fatalf("selected catalogue = %+v", got)
	}
}

func TestPartialBearerIsRefusedNotLocal(t *testing.T) {
	auth := &testAuthorizer{}
	dispatch := &testDispatcher{}
	cfg := endpointConfig(t, auth, dispatch)
	cfg.Auth = &turnCapableAuthorizer{auth}
	ep := startEndpoint(t, cfg)
	conn, ok := dialEndpoint(t, ep).(*net.UnixConn)
	if !ok {
		t.Fatal("endpoint did not return a Unix connection")
	}
	_, _ = io.WriteString(conn, turnbind.TokenMagic+"abcd")
	_ = conn.CloseWrite()
	got := readResponse(t, conn)
	if got.Error == nil || dispatch.callCount() != 0 {
		t.Fatalf("partial bearer was served: %+v", got)
	}
}
