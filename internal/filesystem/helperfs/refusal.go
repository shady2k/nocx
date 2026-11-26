package helperfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"syscall"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/transfer"
)

// Classify the host's closed vocabulary without dropping the filesystem or
// transfer contracts. The underlying refusal still identifies protocol errors
// that do not have a filesystem equivalent (such as bad_params).
func classifyRefusal(name string, params any, err error) error {
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		return err
	}
	var detail struct {
		Kind     string `json:"kind"`
		Reason   string `json:"reason"`
		Observed int64  `json:"observed"`
		Limit    int64  `json:"limit"`
		Expected int64  `json:"expected"`
		Size     int64  `json:"size"`
		Offset   int64  `json:"offset"`
	}
	_ = json.Unmarshal(refusal.Details, &detail)
	switch refusal.Code {
	case proto.FSCodeNotFound:
		return &filesystem.ErrNotFound{Path: name, Err: errors.Join(fs.ErrNotExist, refusal)}
	case proto.FSCodePermissionDenied:
		return &filesystem.ErrPermission{Path: name, Err: errors.Join(fs.ErrPermission, refusal)}
	case proto.FSCodeNotDir:
		return &filesystem.ErrNotDir{Path: name, Err: refusal}
	case proto.FSCodeNotRegular:
		kind := filesystem.Kind(detail.Kind)
		switch kind {
		case filesystem.KindDir, filesystem.KindSymlink:
		default:
			kind = filesystem.KindOther
		}
		return &filesystem.ErrNotRegular{Path: name, Kind: kind, Err: errors.Join(transfer.ErrNotRegular, refusal)}
	case proto.FSCodeInvalidPath:
		return &filesystem.ErrInvalidPath{Path: name, Reason: detail.Reason}
	case proto.FSCodeInvalidPage:
		switch req := params.(type) {
		case proto.FSListParams:
			return &filesystem.ErrInvalidPage{Offset: req.Offset, Limit: req.Limit}
		case proto.FSReadParams:
			return &filesystem.ErrInvalidPage{Offset: int(req.Offset), Limit: int(req.Length)}
		}
	case proto.FSCodeTooLarge:
		if _, listing := params.(proto.FSListParams); listing {
			return &filesystem.ErrTooLarge{ObservedCount: int(detail.Observed), Limit: int(detail.Limit)}
		}
		return &filesystem.ErrByteLimit{Path: name, Observed: detail.Observed, Limit: detail.Limit}
	case proto.FSCodeTooLargeSize:
		return &filesystem.ErrTooLargeSize{ObservedBytes: detail.Observed, Limit: detail.Limit}
	case proto.FSCodeTimedOut:
		return &filesystem.ErrTimedOut{}
	case proto.FSCodeChangedDuringRead:
		return &filesystem.ErrChangedDuringRead{Path: name}
	case proto.FSCodeChangedSinceRead:
		return &filesystem.ErrChangedSinceRead{Path: name, Reason: detail.Reason}
	case proto.FSCodeExists:
		return &fs.PathError{Op: "write", Path: name, Err: fs.ErrExist}
	case proto.FSCodeNoSpace:
		return &fs.PathError{Op: "write", Path: name, Err: syscall.ENOSPC}
	case proto.FSCodeSizeMismatch:
		return &transfer.SizeMismatchError{Declared: detail.Size, Got: detail.Expected}
	case proto.FSCodeBadOffset:
		return fmt.Errorf("filesystem: helper expected write offset %d, got %d: %w", detail.Expected, detail.Offset, refusal)
	}
	return err
}
