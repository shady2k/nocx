package transport

import (
	"encoding/json"
	"testing"
	"time"
)

// assertDirectoryAnswerFrom takes the answer the waiting dialog.openDirectory is
// owed, from the goroutine that is already reading that connection (gorilla
// allows one reader per connection), and asserts it is the adapter's path.
func assertDirectoryAnswerFrom(t *testing.T, answered <-chan []byte, wantPath string) {
	t.Helper()
	var resp []byte
	select {
	case resp = <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting dialog.openDirectory was never answered")
	}
	var env struct {
		Error  *jsonrpcErrorObj `json:"error"`
		Result *struct {
			Path string `json:"path"`
		} `json:"result"`
	}
	if json.Unmarshal(resp, &env) != nil {
		t.Fatalf("unmarshal: %s", resp)
	}
	if env.Error != nil {
		t.Fatalf("the waiting dialog.openDirectory answered an error: %+v", env.Error)
	}
	if env.Result == nil || env.Result.Path != wantPath {
		t.Fatalf("path = %s, want %q", resp, wantPath)
	}
}
