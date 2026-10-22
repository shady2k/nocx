//go:build !darwin

package fishchannel

import "github.com/shady2k/nocx/internal/lifecyclecodec"

func writeSocketFrame(op *operation, fd int, body []byte) error {
	_, err := lifecyclecodec.WriteRawFrame(&socketIO{op: op, fd: fd}, body)
	return err
}
