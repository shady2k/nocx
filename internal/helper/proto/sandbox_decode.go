package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	maxSandboxPrepareBytes = 1 << 20
	maxSandboxLaunchBytes  = 128 << 10
)

var errSandboxJSON = errors.New("sandbox: invalid bounded request")

// These decoders also run during host schema validation, before the service
// handler can allocate unbounded root or environment collections.
func (params *SandboxPrepareParams) UnmarshalJSON(data []byte) error {
	if len(data) > maxSandboxPrepareBytes {
		return errSandboxJSON
	}
	type plain SandboxPrepareParams
	var decoded plain
	if err := decodeSandboxJSON(data, &decoded); err != nil {
		return err
	}
	*params = SandboxPrepareParams(decoded)
	return nil
}

func (params *SandboxLaunchParams) UnmarshalJSON(data []byte) error {
	if len(data) > maxSandboxLaunchBytes {
		return errSandboxJSON
	}
	type plain SandboxLaunchParams
	var decoded plain
	if err := decodeSandboxJSON(data, &decoded); err != nil {
		return err
	}
	*params = SandboxLaunchParams(decoded)
	return nil
}

func (params *SandboxDiscardParams) UnmarshalJSON(data []byte) error {
	if len(data) > 1024 {
		return errSandboxJSON
	}
	type plain SandboxDiscardParams
	var decoded plain
	if err := decodeSandboxJSON(data, &decoded); err != nil {
		return err
	}
	ticket := decoded.Ticket != "" && len(decoded.Ticket) <= 128 && decoded.OperationID == "" && decoded.LaunchID == ""
	correlation := decoded.Ticket == "" && decoded.OperationID != "" && decoded.LaunchID != "" && len(decoded.OperationID) <= 128 && len(decoded.LaunchID) <= 128
	if !ticket && !correlation {
		return errSandboxJSON
	}
	*params = SandboxDiscardParams(decoded)
	return nil
}

func decodeSandboxJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errSandboxJSON
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errSandboxJSON
	}
	return nil
}
