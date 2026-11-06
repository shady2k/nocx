package capability

import (
	"context"
	"errors"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/operation/catalog"
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

// NewBackupOperation constructs the guarded access path for the backup reads
// (create and preview). restore takes its own operation below: it rewrites the
// store set and must exclude every reader, not share the config gate with them.
// The config gate is acquired before the execution lane, matching the canonical
// order used by every config operation.
func NewBackupOperation(configGate, lane control.Admission, service *backup.Service) BackupOperation {
	g := &guard{}
	return newOperation[BackupService](Direct("BackupOperation"), control.NewComposite(configGate, lane), g, &backupService{guard: g, service: service})
}

// NewRestoreOperation constructs the operation that rewrites the store set.
// Its admission is the store guard's EXCLUSIVE plan for backup.restore — the
// declared barrier and every store the declaration names — so it waits for
// every shared holder and admits no new one while it waits (ADR-8eej3e66 §5).
//
// It also owns the other half of the trade. A rollback that fails or outlives
// its recovery budget leaves the stores holding two generations, and the only
// safe answer is to make them unreadable until restart: the operation quarantines
// the declaration's stores and returns an unavailable failure, which the transport
// answers with a named sentence rather than control.saturated.
func NewRestoreOperation(stores *StoreGuard, decl catalog.Declaration, lane control.Admission, service *backup.Service) BackupOperation {
	g := &guard{}
	inner := newOperation[BackupService](Direct("RestoreOperation"),
		control.NewComposite(stores.Exclusive(decl), lane), g, &backupService{guard: g, service: service})
	return &restoreOperation{inner: inner, guard: stores, stores: stores.StoresOf(decl)}
}

// restoreOperation is a BackupOperation that reacts to a failed rollback. It
// wraps rather than extends the generic operation because the reaction is not
// a property of the admission: it is what the restore path does when its own
// recovery signal arrives.
type restoreOperation struct {
	inner  *operation[BackupService]
	guard  *StoreGuard
	stores []string
}

func (r *restoreOperation) Disposition() Disposition { return r.inner.Disposition() }

func (r *restoreOperation) Run(ctx context.Context, fn func(context.Context, BackupService) error) error {
	err := r.inner.Run(ctx, fn)
	var recovery *backup.RecoveryError
	if errors.As(err, &recovery) {
		store := r.stores[0]
		r.guard.Quarantine(r.stores, recoveryCause(recovery))
		return &UnavailableError{
			Scope:  "store:" + store,
			Reason: unavailableSentence(store, recoveryCause(recovery)),
			Err:    err,
		}
	}
	return err
}

// recoveryCause is the short clause the person's sentence carries. The full
// error stays underneath as the cause.
func recoveryCause(err *backup.RecoveryError) string {
	if err.TimedOut {
		return "the rollback did not finish within " + err.Budget.String()
	}
	return "the rollback failed"
}
