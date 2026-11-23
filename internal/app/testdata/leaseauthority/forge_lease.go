package app

import (
	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/helper/client"
)

func forgeLease(shellClient *client.Client) {
	_ = farhelper.Lease{Client: shellClient}
	var lease farhelper.Lease
	lease.Client = shellClient
}
