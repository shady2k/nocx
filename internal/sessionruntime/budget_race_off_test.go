//go:build !race

package sessionruntime

// raceDetector: see budget_race_on_test.go.
const raceDetector = false
