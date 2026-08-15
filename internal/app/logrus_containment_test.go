package app

// The containment test, asserted by receipt and not by silence (design
// §4.1): a logrus write arrives at the injected slog-backed sink with its
// level and fields intact, AND nothing is written to stderr. Silence alone
// would also be produced by discarding the records.

import (
	"bytes"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/shady2k/nocx/internal/log"
)

// restoreLogrusState undoes the global redirect every containment test
// installs: logrus's output, level, hooks and the install-once flag are
// process-global, and a test that leaves them installed would leak its
// sink (and a stale logrusHooked) into every later test in this binary.
func restoreLogrusState() {
	logrus.SetOutput(os.Stderr)
	logrus.SetLevel(logrus.InfoLevel)
	logrus.StandardLogger().ReplaceHooks(make(logrus.LevelHooks))
	logrusMu.Lock()
	logrusHooked = false
	logrusTarget = nil
	logrusMu.Unlock()
}

func TestLogrusContainment_ReceiptIntoInjectedSink(t *testing.T) {
	defer restoreLogrusState()
	var buf bytes.Buffer
	logger := log.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// Capture stderr for the "nothing reaches stderr" half. The app's own
	// stderr writes are not under test here — only what logrus emits.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	restore := func() {
		_ = w.Close()
		os.Stderr = orig
		_ = r.Close()
	}
	defer restore()

	installLogrusContainment(logger)

	// The write under test. Fields exercise the mapping; the level proves
	// the translation table.
	logrus.WithFields(logrus.Fields{
		"component": "gonja",
		"template":  "render",
	}).Warn("template execution degraded")

	// Receipt: the message, the level, and the fields arrive at OUR sink.
	out := buf.String()
	for _, want := range []string{"template execution degraded", "level=WARN", "component=gonja", "template=render"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("sink output missing %q:\n%s", want, out)
		}
	}

	// And nothing reaches stderr around it.
	_ = w.Close()
	os.Stderr = orig
	stderrBuf := make([]byte, 4096)
	n, _ := r.Read(stderrBuf)
	if n > 0 {
		t.Fatalf("logrus wrote to stderr: %q", stderrBuf[:n])
	}
}

// TestLogrusContainment_LevelMapping pins the level vocabulary translation:
// every logrus level lands on the slog-backed interface at the mapped
// severity. Fatal/Panic map to Error (their os.Exit/panic behaviour is
// logrus's own and outside the containment).
func TestLogrusContainment_LevelMapping(t *testing.T) {
	defer restoreLogrusState()
	levels := []struct {
		in   logrus.Level
		want string
	}{
		{logrus.TraceLevel, "level=DEBUG"},
		{logrus.DebugLevel, "level=DEBUG"},
		{logrus.InfoLevel, "level=INFO"},
		{logrus.WarnLevel, "level=WARN"},
		{logrus.ErrorLevel, "level=ERROR"},
	}
	for _, lv := range levels {
		t.Run(lv.in.String(), func(t *testing.T) {
			var buf bytes.Buffer
			logger := log.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			installLogrusContainment(logger)
			logrus.NewEntry(logrus.StandardLogger()).Log(lv.in, "level-probe")
			out := buf.String()
			if !bytes.Contains([]byte(out), []byte("level-probe")) || !bytes.Contains([]byte(out), []byte(lv.want)) {
				t.Fatalf("sink output = %q, want %q with %q", out, "level-probe", lv.want)
			}
		})
	}
}

// TestLogrusContainment_ConcurrentInstallKeepsOneHook: installing twice (two
// Apps in one process, as tests do) re-targets the sink instead of stacking
func TestLogrusContainment_ConcurrentInstallKeepsOneHook(t *testing.T) {
	defer restoreLogrusState()
	var buf1, buf2 bytes.Buffer
	l1 := log.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf1, nil)))
	l2 := log.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf2, nil)))

	installLogrusContainment(l1)
	installLogrusContainment(l2)
	logrus.Info("one-write")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		logrus.Warn("from-goroutine")
	}()
	wg.Wait()

	if buf1.Len() != 0 {
		t.Fatalf("first sink received writes after re-target: %q", buf1.String())
	}
	if !bytes.Contains(buf2.Bytes(), []byte("one-write")) || !bytes.Contains(buf2.Bytes(), []byte("from-goroutine")) {
		t.Fatalf("second sink missing writes:\n%s", buf2.String())
	}
}
