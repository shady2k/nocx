// The host is the parent process the conformance suite kills. It starts one
// agent through the shared base and then holds the pipes until it is killed.
// The suite asserts that the agent and its bridge die from that alone, which is
// ADR-anntnxdj decision 5: a dead backend takes its agent with it, through the
// pipes.
package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

func main() {
	ctx := context.Background()
	process, err := (harness.Base{}).Start(ctx, harness.Launch{
		Argv: []string{os.Getenv("HOST_AGENT")},
		Env: []string{
			"FAKEAGENT_SCRIPT=" + os.Getenv("HOST_SCRIPT"),
			"FAKEAGENT_TOOLS=" + os.Getenv("HOST_TOOLS"),
		},
		Dir:    os.Getenv("HOST_DIR"),
		Decode: func([]byte) ([]harness.Event, error) { return nil, nil },
	})
	if err != nil {
		os.Exit(2)
	}
	if err := json.NewEncoder(process.Stdin()).Encode(protocol.Prompt{Text: "host"}); err != nil {
		os.Exit(2)
	}
	select {}
}
