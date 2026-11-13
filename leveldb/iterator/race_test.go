//go:build race

package iterator_test

// raceEnabled reports whether the race detector is on, under which
// allocation counts are not stable.
const raceEnabled = true
