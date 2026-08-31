package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/shady2k/nocx/internal/coordinator"
)

func TestResolveBackendReportsSuccessAndFailure(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		w := &WailsApp{
			logger: slog.Default(),
			launchCoordinatorFn: func(context.Context, string) (coordinator.Launch, error) {
				return coordinator.Launch{Hello: coordinator.Hello{
					WSAddress: "127.0.0.1:4321",
					WSToken:   "capability-token",
				}}, nil
			},
		}

		got := w.ResolveBackend()
		want := BackendResolution{
			OK:    true,
			Host:  "127.0.0.1",
			Port:  4321,
			Token: "capability-token",
		}
		if got != want {
			t.Fatalf("ResolveBackend() = %#v, want %#v", got, want)
		}
	})

	t.Run("failure", func(t *testing.T) {
		cause := errors.New("server could not start")
		failure := coordinator.NewLaunchFailure(
			coordinator.FailureNotReady,
			"The backend is not ready.",
			"Retry the launch.",
			cause,
		)
		w := &WailsApp{
			logger: slog.Default(),
			launchCoordinatorFn: func(context.Context, string) (coordinator.Launch, error) {
				return coordinator.Launch{}, failure
			},
		}

		got := w.ResolveBackend()
		want := BackendResolution{
			Kind:    string(coordinator.FailureNotReady),
			Message: failure.Message,
			Remedy:  failure.Remedy,
		}
		if got != want {
			t.Fatalf("ResolveBackend() = %#v, want %#v", got, want)
		}
	})
}

func TestResolveBackendReturnsHeldStartupFailureBeforeRetry(t *testing.T) {
	failure := coordinator.NewLaunchFailure(
		coordinator.FailureProfileUnusable,
		"The profile directories could not be used.",
		"Check the profile directories, then retry.",
		nil,
	)
	attempts := 0
	w := &WailsApp{
		logger:        slog.Default(),
		launchFailure: failure,
		launchCoordinatorFn: func(context.Context, string) (coordinator.Launch, error) {
			attempts++
			return coordinator.Launch{Hello: coordinator.Hello{
				WSAddress: "127.0.0.1:4322",
				WSToken:   "new-token",
			}}, nil
		},
	}

	if got := w.ResolveBackend(); got.Kind != string(coordinator.FailureProfileUnusable) {
		t.Fatalf("first ResolveBackend() = %#v, want held failure", got)
	}
	if attempts != 0 {
		t.Fatalf("first ResolveBackend launched %d times, want 0", attempts)
	}
	if got := w.ResolveBackend(); !got.OK || got.Port != 4322 || got.Token != "new-token" {
		t.Fatalf("retry ResolveBackend() = %#v, want successful retry", got)
	}
	if attempts != 1 {
		t.Fatalf("retry launched %d times, want 1", attempts)
	}
}
