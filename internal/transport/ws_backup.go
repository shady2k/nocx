package transport

import (
	"encoding/json"
	"errors"

	"github.com/shady2k/nocx/internal/backup"
)

// --- backup.* control-plane handlers (ADR-0015) -------------------------

// handleBackupMethod dispatches backup.create/preview/restore RPCs.
// Returns -32601 when backup service is not wired.
func (s *WSServer) handleBackupMethod(wconn *wsConn, req jsonrpcRequest) {
	if s.backupService == nil {
		resp := newJSONRPCError(req.ID, -32601, "Method not found")
		_ = wconn.writeJSON(resp)
		return
	}

	// Check poisoned config gate.
	s.configMu.RLock()
	poisoned := s.configErr != nil
	s.configMu.RUnlock()
	if poisoned {
		resp := newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx")
		_ = wconn.writeJSON(resp)
		return
	}

	switch req.Method {
	case "backup.create":
		s.handleBackupCreate(wconn, req)
	case "backup.preview":
		s.handleBackupPreview(wconn, req)
	case "backup.restore":
		s.handleBackupRestore(wconn, req)
	case "backup.saveToFile":
		s.handleBackupSaveToFile(wconn, req)
	}
}

// --- backup.create ------------------------------------------------------

func (s *WSServer) handleBackupCreate(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	poisoned := s.configErr != nil
	s.configMu.RUnlock()
	if poisoned {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	s.configMu.Lock()
	defer s.configMu.Unlock()
	// TOCTOU: re-check after acquiring exclusive lock.
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}
	result, err := s.backupService.Create()

	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "backup.create: internal error"))
		s.log.Warn("backup.create failed", "error", err)
		return
	}

	raw, err := json.Marshal(result)
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "backup.create: marshal error"))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, raw))
}

// --- backup.preview -----------------------------------------------------

type backupPreviewParams struct {
	Contents string `json:"contents"`
	Strategy string `json:"strategy"`
}

func (s *WSServer) handleBackupPreview(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	poisoned := s.configErr != nil
	s.configMu.RUnlock()
	if poisoned {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	var params backupPreviewParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}

	strategy := backup.RestoreStrategy(params.Strategy)

	s.configMu.Lock()
	defer s.configMu.Unlock()
	// TOCTOU: re-check after acquiring exclusive lock.
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}
	result, err := s.backupService.Preview(params.Contents, strategy)

	if err != nil {
		if errors.Is(err, backup.ErrInvalidDocument) {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "backup.preview: "+err.Error()))
		} else {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "backup.preview: internal error"))
		}
		s.log.Warn("backup.preview failed", "error", err)
		return
	}

	raw, err := json.Marshal(result)
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "backup.preview: marshal error"))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, raw))
}

// --- backup.restore -----------------------------------------------------

type backupRestoreParams struct {
	Contents     string `json:"contents"`
	Strategy     string `json:"strategy"`
	PreviewToken string `json:"previewToken"`
}

func (s *WSServer) handleBackupRestore(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	poisoned := s.configErr != nil
	s.configMu.RUnlock()
	if poisoned {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	var params backupRestoreParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}

	strategy := backup.RestoreStrategy(params.Strategy)

	s.configMu.Lock()
	defer s.configMu.Unlock()
	// TOCTOU: re-check after acquiring exclusive lock.
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}
	result, err := s.backupService.Restore(params.Contents, strategy, params.PreviewToken)

	if err != nil {
		if errors.Is(err, backup.ErrInvalidDocument) {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "backup.restore: "+err.Error()))
			return
		}
		if errors.Is(err, backup.ErrRecoveryRequired) {
			s.configErr = err
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
			return
		}
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "backup.restore: internal error"))
		s.log.Warn("backup.restore failed", "error", err)
		return
	}

	raw, err := json.Marshal(result)
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "backup.restore: marshal error"))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, raw))
}

// --- backup.saveToFile ---------------------------------------------------

type backupSaveToFileParams struct {
	FileName string `json:"fileName"`
	Contents string `json:"contents"`
}

func (s *WSServer) handleBackupSaveToFile(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	poisoned := s.configErr != nil
	s.configMu.RUnlock()
	if poisoned {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	var params backupSaveToFileParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}

	result, err := backup.SaveToFile(params.FileName, params.Contents)
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "backup.saveToFile: internal error"))
		s.log.Warn("backup.saveToFile failed", "error", err)
		return
	}
	// User cancelled — return null result.
	if result == nil {
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, json.RawMessage("null")))
		return
	}

	raw, err := json.Marshal(result)
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "backup.saveToFile: marshal error"))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, raw))
}
