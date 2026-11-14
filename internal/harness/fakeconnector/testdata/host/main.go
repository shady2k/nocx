// The connector-specific backend fixture exercises its actual Run launch path.
package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/harness/fakeconnector"
)

func main() {
	var script protocol.Script
	var tools harness.ToolBridge
	if json.Unmarshal([]byte(os.Getenv("HOST_SCRIPT")), &script) != nil || json.Unmarshal([]byte(os.Getenv("HOST_TOOLS")), &tools) != nil {
		os.Exit(2)
	}
	connector := fakeconnector.New(fakeconnector.Config{AgentPath: os.Getenv("HOST_AGENT"), Script: script})
	turn, err := connector.Run(context.Background(), harness.TurnSpec{Text: "host", Dir: os.Getenv("HOST_DIR"), Tools: tools})
	if err != nil {
		os.Exit(2)
	}
	for range turn.Events() {
	}
	_, _ = turn.Wait(context.Background())
}
