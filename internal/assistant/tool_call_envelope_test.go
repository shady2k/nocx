package assistant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestIsUnexecutedToolCallEnvelope_RecognizesWholeKnownDialects(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "xml envelope",
			text: "  <tool_call><function=run><parameter=command>df -h</parameter><parameter=sessionId>abc</parameter></function></tool_call>  ",
			want: true,
		},
		{
			name: "json envelope",
			text: `<tool_call>{"name":"run","arguments":{"command":"df -h"}}</tool_call>`,
			want: true,
		},
		{
			name: "quoted in an answer",
			text: `I cannot run that here; the model might emit <tool_call>{"name":"run","arguments":{}}</tool_call>.`,
			want: false,
		},
		{
			name: "envelope with answer around it",
			text: `I will check that. <tool_call>{"name":"run","arguments":{}}</tool_call>`,
			want: false,
		},
		{
			name: "xml missing function",
			text: `<tool_call><parameter=command>df -h</parameter></tool_call>`,
			want: false,
		},
		{
			name: "json missing arguments object",
			text: `<tool_call>{"name":"run"}</tool_call>`,
			want: false,
		},
		{
			name: "two xml envelopes",
			text: "<tool_call><function=run><parameter=command>df -h</parameter></function></tool_call>\n\n<tool_call><function=run><parameter=command>du -sh .</parameter></function></tool_call>",
			want: true,
		},
		{
			name: "two json envelopes",
			text: `<tool_call>{"name":"run","arguments":{"command":"df -h"}}</tool_call>

<tool_call>{"name":"run","arguments":{"command":"du -sh ."}}</tool_call>`,
			want: true,
		},
		{
			name: "mixed dialect envelopes",
			text: `<tool_call>{"name":"run","arguments":{"command":"df -h"}}</tool_call>

<tool_call><function=run><parameter=command>du -sh .</parameter></function></tool_call>`,
			want: true,
		},
		{
			name: "trailing prose after sequence",
			text: "<tool_call><function=run><parameter=command>df -h</parameter></function></tool_call>\n\n<tool_call><function=run><parameter=command>du -sh .</parameter></function></tool_call>\nI also checked the logs.",
			want: false,
		},
		{
			name: "nested envelope",
			text: "<tool_call><function=run><parameter=command><tool_call>{\"name\":\"run\",\"arguments\":{}}</tool_call></parameter></function></tool_call>",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUnexecutedToolCallEnvelope(tt.text); got != tt.want {
				t.Fatalf("IsUnexecutedToolCallEnvelope(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestAsk_OfferedToolsAndWholeEnvelopeReturnTypedFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		streamAnswer(w, `<tool_call>{"name":"run","arguments":{"command":"df -h"}}</tool_call>`)
	}))
	defer srv.Close()

	grant, _ := testDirGrant(t, autonomousMatrix())
	cl, err := newClient(nil, os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	askErr := cl.Ask(context.Background(), askParams(srv.URL, &grant, realLedger(t), NewApprovalStore()), func(AskEvent) error {
		return nil
	})
	var envelopeErr *UnexecutedToolCallError
	if !errors.As(askErr, &envelopeErr) {
		t.Fatalf("Ask error = %v, want UnexecutedToolCallError", askErr)
	}
}

func TestAsk_OrdinaryAnswerWithToolMarkupQuotedStillCompletes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		streamAnswer(w, "The answer mentions <tool_call> as an example, but it is ordinary prose.")
	}))
	defer srv.Close()

	grant, _ := testDirGrant(t, autonomousMatrix())
	cl, err := newClient(nil, os.DirFS(realToolsFS))
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	if err := cl.Ask(context.Background(), askParams(srv.URL, &grant, realLedger(t), NewApprovalStore()), func(AskEvent) error {
		return nil
	}); err != nil {
		t.Fatalf("ordinary answer returned error: %v", err)
	}
}

func TestUnexecutedToolCallErrorHasStableNonProseText(t *testing.T) {
	if strings.TrimSpace((&UnexecutedToolCallError{}).Error()) == "" {
		t.Fatal("UnexecutedToolCallError.Error() is empty")
	}
}
