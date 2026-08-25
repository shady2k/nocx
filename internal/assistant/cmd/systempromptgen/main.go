package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/assistant"
)

func main() {
	artifact, err := json.Marshal(assistant.SettingsSystemPrompt())
	if err != nil {
		log.Fatalf("marshal settings prompt: %v", err)
	}
	path := filepath.Join("..", "..", "frontend", "src", "systemprompt.json")
	// With the trailing newline prettier wants: the artifact is checked in
	// under frontend/src, so the repo's formatter walks it like any other
	// source file. Emitting it already-formatted is what keeps the generator
	// and the formatter from overwriting each other's output forever.
	artifact = append(artifact, '\n')
	if err := os.WriteFile(path, artifact, 0o644); err != nil { //nolint:gosec // this is a checked-in public prompt artifact
		log.Fatalf("write %s: %v", path, err)
	}
}
