package capability

import (
	"context"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/transport/control"
)

// BackupService is the guarded domain surface for structured backup reads and
// restore writes. The service is usable only during BackupOperation.Run.
type BackupService interface {
	Create() (*backup.CreateResult, error)
	Preview(contents string, strategy backup.RestoreStrategy) (*backup.RestorePreview, error)
	Restore(contents string, strategy backup.RestoreStrategy, previewToken string) (*backup.RestoreResult, error)
}

// BackupOperation serializes backup reads and restore writes with the config
// domain and the bounded control lane.
type BackupOperation interface {
	AssistantOperation
	Run(context.Context, func(context.Context, BackupService) error) error
}

type backupService struct {
	guard   *guard
	service *backup.Service
}

func (s *backupService) Create() (*backup.CreateResult, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.service.Create()
}

func (s *backupService) Preview(contents string, strategy backup.RestoreStrategy) (*backup.RestorePreview, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.service.Preview(contents, strategy)
}

func (s *backupService) Restore(contents string, strategy backup.RestoreStrategy, previewToken string) (*backup.RestoreResult, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.service.Restore(contents, strategy, previewToken)
}

// NewBackupOperation constructs the sole guarded access path for the backup
// service. The config gate is acquired before the execution lane, matching the
// canonical order used by every config operation.
func NewBackupOperation(configGate, lane control.Admission, service *backup.Service) BackupOperation {
	g := &guard{}
	return newOperation[BackupService](Direct("BackupOperation"), control.NewComposite(configGate, lane), g, &backupService{guard: g, service: service})
}
