package main

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/shady2k/nocx/internal/downloadsave"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

type wailsDownloadPicker struct{ app *WailsApp }

func (p wailsDownloadPicker) SaveFile(ctx context.Context, suggestedName string) (string, error) {
	permit, err := p.app.acquirePrompt(ctx)
	if err != nil {
		return "", err
	}
	defer permit.Release()
	app := application.Get()
	if app == nil {
		return "", errors.New("save dialog unavailable")
	}
	return app.Dialog.SaveFile().
		SetFilename(suggestedName).
		CanCreateDirectories(true).
		PromptForSingleSelection()
}

func (w *WailsApp) initializeDownloadSave() {
	w.downloadOnce.Do(func() {
		svc, err := downloadsave.New(downloadsave.Config{
			Picker: wailsDownloadPicker{app: w},
			PrepareDestination: func(path string) (downloadsave.Destination, error) {
				return local.PrepareDownload(path)
			},
			Address: func() (string, error) {
				w.downloadMu.RLock()
				defer w.downloadMu.RUnlock()
				if w.downloadAddress == "" {
					return "", errors.New("backend unavailable")
				}
				return w.downloadAddress, nil
			},
			Logger: w.logger,
			Now:    time.Now,
			Random: rand.Reader,
		})
		if err != nil {
			if w.logger != nil {
				w.logger.Error("native download receiver unavailable")
			}
			return
		}
		w.downloadMu.Lock()
		w.downloadSave = svc
		w.downloadMu.Unlock()
	})
}

func (w *WailsApp) downloadService() *downloadsave.Service {
	w.downloadMu.RLock()
	defer w.downloadMu.RUnlock()
	return w.downloadSave
}

// HostPrepareDownload presents the native destination picker before a backend transfer exists.
func (w *WailsApp) HostPrepareDownload(ctx context.Context, suggestedName string) (string, error) {
	svc := w.downloadService()
	if svc == nil {
		return "", errors.New("native download is unavailable")
	}
	handle, err := svc.Prepare(ctx, suggestedName)
	if err != nil {
		return "", errors.New("native download destination could not be prepared")
	}
	return handle, nil
}

// HostSaveDownload consumes a prepared destination and reports only its outcome.
func (w *WailsApp) HostSaveDownload(ctx context.Context, handle, ticket string, size int64) (downloadsave.Result, error) {
	svc := w.downloadService()
	if svc == nil {
		return downloadsave.Result{Outcome: "source-failed"}, errors.New("native download is unavailable")
	}
	return svc.Save(ctx, handle, ticket, size), nil
}

// HostDiscardDownload releases or cancels one opaque prepared destination.
func (w *WailsApp) HostDiscardDownload(handle string) error {
	if svc := w.downloadService(); svc != nil {
		svc.Discard(handle)
	}
	return nil
}
