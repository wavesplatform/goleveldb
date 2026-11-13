//go:build race

package table

// raceEnabled reports whether the race detector is on. It makes sync.Pool
// drop items at random, so allocation counts are not stable.
const raceEnabled = true
