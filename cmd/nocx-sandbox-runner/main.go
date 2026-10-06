package main

import (
	"os"

	"github.com/shady2k/nocx/internal/sandbox"
)

func main() {
	os.Exit(sandbox.RunRunner())
}
