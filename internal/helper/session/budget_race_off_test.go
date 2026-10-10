//go:build !race

package session

// raceDetector: see budget_race_on_test.go.
const raceDetector = false
