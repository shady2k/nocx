package assistant

import (
	"net/http"
	"strings"
	"testing"

	openai "github.com/meguminnnnnnnnn/go-openai"
)

func TestEndpointErrorSentence(t *testing.T) {
	const model = "tiel-coder-35b-a3b-mlx-oq4e"
	frameworkWords := []string{"NodeRunError", "node path", "ToolNode"}
	tests := []struct {
		name string
		err  error
		want []string
		not  []string
	}{
		{
			name: "unauthorized names credential",
			err:  &openai.APIError{HTTPStatusCode: http.StatusUnauthorized, Message: "invalid api key"},
			want: []string{"credential"},
		},
		{
			name: "not found names address and v1",
			err:  &openai.APIError{HTTPStatusCode: http.StatusNotFound, Message: "route missing"},
			want: []string{"404", "address", "/v1"},
		},
		{
			name: "model not found names model id",
			err:  &openai.APIError{HTTPStatusCode: http.StatusNotFound, Code: "model_not_found", Message: "[NodeRunError] model missing at node path ToolNode"},
			want: []string{model, "model id"},
			not:  frameworkWords,
		},
		{
			name: "other status names status",
			err: &openai.RequestError{
				HTTPStatusCode: http.StatusTooManyRequests,
				Body:           []byte("[NodeRunError] ToolNode node path"),
			},
			want: []string{"429", "Too Many Requests"},
			not:  frameworkWords,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sentence, ok := EndpointErrorSentence(tt.err, model)
			if !ok {
				t.Fatalf("EndpointErrorSentence(%T) not recognized", tt.err)
			}
			for _, want := range tt.want {
				if !strings.Contains(sentence, want) {
					t.Errorf("sentence %q does not contain %q", sentence, want)
				}
			}
			for _, not := range append(tt.not, frameworkWords...) {
				if strings.Contains(sentence, not) {
					t.Errorf("sentence %q contains framework text %q", sentence, not)
				}
			}
		})
	}
}

func TestEndpointErrorSentence_RecognizesWrappedResponse(t *testing.T) {
	wrapped := &openai.APIError{HTTPStatusCode: http.StatusForbidden, Message: "forbidden"}
	sentence, ok := EndpointErrorSentence(wrapEndpointError(wrapped), "model")
	if !ok || !strings.Contains(sentence, "credential") {
		t.Fatalf("wrapped endpoint error = (%q, %v), want credential sentence", sentence, ok)
	}
}

func wrapEndpointError(err error) error {
	return endpointErrorWrapper{err: err}
}

type endpointErrorWrapper struct{ err error }

func (e endpointErrorWrapper) Error() string { return "wrapped endpoint error" }
func (e endpointErrorWrapper) Unwrap() error { return e.err }
