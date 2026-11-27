package app

import (
	helpergit "github.com/shady2k/nocx/internal/git/helper"
	"github.com/shady2k/nocx/internal/helper/client"
)

func openWithShell(shellClient *client.Client) {
	_ = helpergit.NewFactory(shellClient)
}
