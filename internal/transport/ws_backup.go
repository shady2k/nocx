package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/operation/catalog"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ── backup.* ingress bounds and validators (the per-field sweep) ─────────

// validateBackupStrategy checks the closed restore-strategy enum the backup
// service itself rules on (an unknown strategy is ErrInvalidDocument →
// -32602 at the handler): the same set, refused before the handler.
func validateBackupStrategy(s string) string {
	switch backup.RestoreStrategy(s) {
	case backup.RestoreMerge, backup.RestoreReplace:
		return ""
	default:
		return "strategy must be one of merge, replace"
	}
}

// validateBackupPreviewRaw checks backup.preview. The contents are bounded by
// the same ceiling the service applies when parsing (backup.MaxDocumentBytes
// → ErrInvalidDocument); the document's INTERNAL validity is deliberately
// left to the service — parsing needs the settings seam parseAndValidate
// holds, and duplicating it here would be a second owner.
func validateBackupPreviewRaw(raw json.RawMessage) string {
	var p backupPreviewParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if p.Contents == "" {
		return "contents is required"
	}
	if len(p.Contents) > backup.MaxDocumentBytes {
		return fmt.Sprintf("contents exceeds %d bytes", backup.MaxDocumentBytes)
	}
	if msg := validateBackupStrategy(p.Strategy); msg != "" {
		return msg
	}
	return ""
}

// validateBackupRestoreRaw checks backup.restore: the same contents and
// strategy rules as preview, plus the preview token — the hex sha-256 the
// service computed (computePreviewToken), which the service compares for
// equality, so a token of the wrong shape can never match.
func validateBackupRestoreRaw(raw json.RawMessage) string {
	var p backupRestoreParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if p.Contents == "" {
		return "contents is required"
	}
	if len(p.Contents) > backup.MaxDocumentBytes {
		return fmt.Sprintf("contents exceeds %d bytes", backup.MaxDocumentBytes)
	}
	if msg := validateBackupStrategy(p.Strategy); msg != "" {
		return msg
	}
	if !isLowerHex(p.PreviewToken, 64) {
		return "previewToken must be the 64-hex token the preview returned"
	}
	return ""
}

// validateBackupSaveRaw checks backup.saveToFile: the dialog's suggested
// file name (bounded like an OS file-name component; a control character in
// a suggested name is never legitimate) and the document-class contents.
func validateBackupSaveRaw(raw json.RawMessage) string {
	var p backupSaveParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if strings.TrimSpace(p.FileName) == "" {
		return "fileName is required"
	}
	if utf8.RuneCountInString(p.FileName) > maxFileNameRunes {
		return fmt.Sprintf("fileName exceeds %d characters", maxFileNameRunes)
	}
	if hasControlChars(p.FileName) {
		return "fileName must not contain control characters"
	}
	if p.Contents == "" {
		return "contents is required"
	}
	if len(p.Contents) > backup.MaxDocumentBytes {
		return fmt.Sprintf("contents exceeds %d bytes", backup.MaxDocumentBytes)
	}
	return ""
}

type backupSpecs struct {
	operation capability.BackupOperation
	// restoreOp is the exclusive operation: create and preview read the stores
	// (shared), restore rewrites them and excludes every reader while it does.
	restoreOp capability.BackupOperation
	saver     func(string, string) (*backup.SaveResult, error)
}

func (s *WSServer) backupSpecs(configGate control.Admission, stores *capability.StoreGuard) []methodSpec {
	var op capability.BackupOperation
	var restoreOp capability.BackupOperation
	if s.backupService != nil {
		op = capability.NewBackupOperation(configGate, s.backupService)
		restoreOp = capability.NewRestoreOperation(stores, catalog.OpBackupRestore.Declaration(), s.backupService)
	}
	h := backupSpecs{operation: op, restoreOp: restoreOp, saver: s.backupFileSaver}
	return []methodSpec{
		regResponder(s.work, catalog.OpBackupCreate, noParams(), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) { h.create(ctx, r, req) }
		}),
		regResponder(s.work, catalog.OpBackupPreview, params(validateBackupPreviewRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) { h.preview(ctx, r, req) }
		}),
		regResponder(s.work, catalog.OpBackupRestore, params(validateBackupRestoreRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) { h.restore(ctx, r, req) }
		}),
		regResponder(s.dialogSub, catalog.OpBackupSaveToFile, params(validateBackupSaveRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) { h.saveToFile(ctx, r, req) }
		}),
	}
}

type backupPreviewParams struct {
	Contents string `json:"contents"`
	Strategy string `json:"strategy"`
}

type backupRestoreParams struct {
	Contents     string `json:"contents"`
	Strategy     string `json:"strategy"`
	PreviewToken string `json:"previewToken"`
}

type backupSaveParams struct {
	FileName string `json:"fileName"`
	Contents string `json:"contents"`
}

func (h backupSpecs) create(ctx context.Context, r Responder, req jsonrpcRequest) {
	if h.operation == nil {
		_ = r.TryError(req.ID, RPCError{Code: -32601, Message: "backup not available"})
		return
	}
	var result *backup.CreateResult
	err := h.operation.Run(ctx, func(ctx context.Context, svc capability.BackupService) error {
		var err error
		result, err = svc.Create()
		return err
	})
	if err != nil {
		h.error(ctx, r, req, err)
		return
	}
	_ = r.TryResult(req.ID, mustMarshal(result))
}

func (h backupSpecs) preview(ctx context.Context, r Responder, req jsonrpcRequest) {
	if h.operation == nil {
		_ = r.TryError(req.ID, RPCError{Code: -32601, Message: "backup not available"})
		return
	}
	var params backupPreviewParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Contents == "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: contents required"})
		return
	}
	var result *backup.RestorePreview
	err := h.operation.Run(ctx, func(ctx context.Context, svc capability.BackupService) error {
		var err error
		result, err = svc.Preview(params.Contents, backup.RestoreStrategy(params.Strategy))
		return err
	})
	if err != nil {
		h.error(ctx, r, req, err)
		return
	}
	_ = r.TryResult(req.ID, mustMarshal(result))
}

func (h backupSpecs) restore(ctx context.Context, r Responder, req jsonrpcRequest) {
	if h.restoreOp == nil {
		_ = r.TryError(req.ID, RPCError{Code: -32601, Message: "backup not available"})
		return
	}
	var params backupRestoreParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Contents == "" || params.PreviewToken == "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: contents and previewToken required"})
		return
	}
	var result *backup.RestoreResult
	err := h.restoreOp.Run(ctx, func(ctx context.Context, svc capability.BackupService) error {
		var err error
		result, err = svc.Restore(params.Contents, backup.RestoreStrategy(params.Strategy), params.PreviewToken)
		return err
	})
	if err != nil {
		h.error(ctx, r, req, err)
		return
	}
	_ = r.TryResult(req.ID, mustMarshal(result))
}

func (h backupSpecs) saveToFile(_ context.Context, r Responder, req jsonrpcRequest) {
	if h.saver == nil {
		_ = r.TryError(req.ID, RPCError{Code: -32601, Message: "backup.saveToFile not available"})
		return
	}
	var params backupSaveParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.FileName == "" || params.Contents == "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: fileName and contents required"})
		return
	}
	result, err := h.saver(params.FileName, params.Contents)
	if err != nil {
		_ = r.TryError(req.ID, rpcErrorFor(-32603, "backup.saveToFile: ", err))
		return
	}
	_ = r.TryResult(req.ID, mustMarshal(result))
}

func (h backupSpecs) error(ctx context.Context, r Responder, req jsonrpcRequest, err error) {
	var refused *capability.RefusedError
	if errors.As(err, &refused) {
		answerSubmissionRejection(ctx, r, req, &refused.Rejection)
		return
	}
	id := req.ID
	method := req.Method
	code := -32603
	if errors.Is(err, backup.ErrInvalidDocument) {
		code = -32602
	}
	_ = r.TryError(id, rpcErrorFor(code, method+": ", err))
}
