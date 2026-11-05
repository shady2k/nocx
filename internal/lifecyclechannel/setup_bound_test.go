package lifecyclechannel

import (
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

// Fake time makes the setup/hello ordering deterministic. These checks drive
// the public stream and inspect the domain a pane uses, not the timer fields.
func TestSlowSetupStillIntegratesAfterTheHelloBudgetHasElapsed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, shell := setupBoundedStream(t)
		time.Sleep(2 * time.Second) // deliberately longer than the hello budget
		a.ObserveShellOutput()
		establishSetupStream(t, a, shell)
		time.Sleep(5 * time.Second)
		assertSetupDomain(t, a, lifecycle.DomainEstablished)
	})
}

func TestAPeerThatNeverStartsStillDegrades(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _ := setupBoundedStream(t)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		assertSetupDomain(t, a, lifecycle.DomainLost)
	})
}

func TestABannerWithoutAHelloDegradesAfterTheHelloBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _ := setupBoundedStream(t)
		a.ObserveShellOutput()
		time.Sleep(2 * time.Second) // setup budget has not expired
		synctest.Wait()
		assertSetupDomain(t, a, lifecycle.DomainLost)
	})
}

func TestAnEarlyHelloCancelsBothSetupAndHelloBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, shell := setupBoundedStream(t)
		establishSetupStream(t, a, shell) // lifecycle may precede all PTY bytes
		a.ObserveShellOutput()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		assertSetupDomain(t, a, lifecycle.DomainEstablished)
	})
}

func setupBoundedStream(t *testing.T) (*Adapter, net.Conn) {
	t.Helper()
	_, logger := logtest.New(t)
	coordinator, shell := net.Pipe()
	a, err := NewStream(logger, newTestKernel(), coordinator,
		WithHelloTimeout(time.Second), WithSetupTimeout(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = shell.Close() })
	a.BeginSetup()
	return a, shell
}

func establishSetupStream(t *testing.T, a *Adapter, shell net.Conn) {
	t.Helper()
	if _, err := lifecyclecodec.Encode(shell, shellEnv(a, 1, lifecycle.Event{
		Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "zsh"},
	})); err != nil {
		t.Fatal(err)
	}
	accept, err := lifecyclecodec.NewDecoder(shell, lifecyclecodec.Config{}, nil).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if accept.Event.Kind != lifecycle.KindAccept {
		t.Fatalf("expected accept, got %s", accept.Event.Kind)
	}
	synctest.Wait()
}

func assertSetupDomain(t *testing.T, a *Adapter, want lifecycle.DomainState) {
	t.Helper()
	d, ok := a.kernel.Domain(a.Launch().Domain)
	if !ok || d.State != want {
		t.Fatalf("pane domain = %+v (found %v), want %v", d, ok, want)
	}
}

// The log call deliberately holds Send after its accept reached the shell.
// Deadline cancellation must already be committed, not deferred until logging.
func TestADeliveredAcceptCannotLoseItsDomainWhileLogging(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, logger := logtest.New(t)
		gate := &acceptLogGate{Logger: logger, entered: make(chan struct{}), release: make(chan struct{})}
		coordinator, shell := net.Pipe()
		a, err := NewStream(gate, newTestKernel(), coordinator,
			WithHelloTimeout(time.Second), WithSetupTimeout(4*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = a.Close(); _ = shell.Close() }()
		a.BeginSetup()
		if _, err := lifecyclecodec.Encode(shell, shellEnv(a, 1, lifecycle.Event{
			Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "zsh"},
		})); err != nil {
			t.Fatal(err)
		}
		if _, err := lifecyclecodec.NewDecoder(shell, lifecyclecodec.Config{}, nil).ReadFrame(); err != nil {
			t.Fatal(err)
		}
		<-gate.entered
		time.Sleep(5 * time.Second)
		synctest.Wait()
		d, found := a.kernel.Domain(a.Launch().Domain)
		close(gate.release)
		synctest.Wait()
		if !found || d.State != lifecycle.DomainEstablished {
			t.Fatalf("delivered accept lost its pane while logging: %+v", d)
		}
	})
}

type acceptLogGate struct {
	log.Logger
	entered chan struct{}
	release chan struct{}
}

func (g *acceptLogGate) Info(msg string, args ...any) {
	if msg == "lifecycle channel established" {
		close(g.entered)
		<-g.release
	}
	g.Logger.Info(msg, args...)
}

func TestAnAdoptedPaneDoesNotStartANewHelloWaitOnOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, logger := logtest.New(t)
		coordinator, shell := net.Pipe()
		a, err := NewAdoptedStream(logger, newTestKernel(), coordinator, adoptedLaunch(), WithHelloTimeout(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = a.Close(); _ = shell.Close() }()
		a.ObserveShellOutput()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		assertSetupDomain(t, a, lifecycle.DomainEstablished)
	})
}

func TestPreparationBeforeSpawnDoesNotSpendTheShellSetupBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, logger := logtest.New(t)
		coordinator, shell := net.Pipe()
		a, err := NewStream(logger, newTestKernel(), coordinator,
			WithHelloTimeout(time.Second), WithSetupTimeout(4*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = a.Close(); _ = shell.Close() }()
		time.Sleep(5 * time.Second) // tool preparation, before spawn is asked for
		a.BeginSetup()
		establishSetupStream(t, a, shell)
		assertSetupDomain(t, a, lifecycle.DomainEstablished)
	})
}

func TestAnExpiredHandshakeCannotDeliverAcceptWhileReportingLoss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, logger := logtest.New(t)
		coordinator, shell := net.Pipe()
		reporting, release := make(chan struct{}), make(chan struct{})
		a, err := NewStream(logger, newTestKernel(), coordinator,
			WithHelloTimeout(time.Second), WithSetupTimeout(4*time.Second),
			WithLossReporter(func(lifecycle.LaneID, Loss) {
				close(reporting)
				<-release
			}))
		if err != nil {
			t.Fatal(err)
		}
		a.BeginSetup()
		time.Sleep(5 * time.Second)
		<-reporting
		// If expiry left a sending window open, this reader lets the buggy
		// Send finish, so the assertion reports an accept rather than hangs.
		received := make(chan bool, 1)
		go func() {
			frame, err := lifecyclecodec.NewDecoder(shell, lifecyclecodec.Config{}, nil).ReadFrame()
			received <- err == nil && frame.Event.Kind == lifecycle.KindAccept
		}()
		sendErr := a.Send(lifecycle.Envelope{Event: lifecycle.Event{Kind: lifecycle.KindAccept}})
		close(release)
		synctest.Wait()
		_ = shell.Close()
		_ = a.Close()
		delivered := <-received
		if sendErr != ErrClosed || delivered {
			t.Fatalf("expired pane delivered accept=%v, send error=%v; want refusal", delivered, sendErr)
		}
		assertSetupDomain(t, a, lifecycle.DomainLost)
	})
}
