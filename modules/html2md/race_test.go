//go:build race

package html2md_test

// The race detector slows the host side of a module call about 2.5×
// (1.8 MB of stray end tags: 0.43 s → 1.05 s on a fast machine, 2.01 s on
// a CI runner), so wall-clock budgets are checked only without it; the
// html2md CI job runs these tests without -race.
const raceEnabled = true
