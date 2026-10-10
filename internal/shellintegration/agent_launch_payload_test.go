package shellintegration

import (
	"strings"
	"testing"
)

func TestEncodeAgentLaunchPayloadFramesArgvAndEnvironmentAsHexData(t *testing.T) {
	argv := []string{"/tmp/my agent", "", "--prompt=quote\" $()", "line\nbreak", "✓"}
	env := []string{"EMPTY=", "MESSAGE=space tab\t and = signs"}

	got, err := EncodeAgentLaunchPayload(argv, env)
	if err != nil {
		t.Fatalf("encode launch payload: %v", err)
	}
	lines := strings.Split(got, "\n")
	wantArgv := []string{
		"2f746d702f6d79206167656e74",
		"",
		"2d2d70726f6d70743d71756f74652220242829",
		"6c696e650a627265616b",
		"e29c93",
	}
	want := []string{"v1", "5"}
	want = append(want, wantArgv...)
	want = append(want, "2", "454d505459\t", "4d455353414745\t7370616365207461620920616e64203d207369676e73", "")
	if !equalPayloadLines(lines, want) {
		t.Fatalf("payload lines = %#v, want %#v", lines, want)
	}
	for _, forbidden := range []string{"/tmp/my agent", "quote", "space tab", "$(", "\""} {
		if strings.Contains(got, forbidden) {
			t.Errorf("payload exposes raw data %q: %q", forbidden, got)
		}
	}
}

func equalPayloadLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
