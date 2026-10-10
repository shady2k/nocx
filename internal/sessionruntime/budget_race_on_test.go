//go:build race

package sessionruntime

// raceDetector says this test binary was built with -race. The allocation
// budgets beside it are skipped there: the detector instruments every
// allocation and adds its own (measured 2026-09-30: the pump's window reads
// 8.3-8.7 MB/MiB under -race against 5.56 MB/MiB without it), so a -race
// run measures the detector, not the shipped path. CI runs the budgets in a
// pass of their own without it (make test-alloc-budgets).
const raceDetector = true
