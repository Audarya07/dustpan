package scan

import "time"

// SetBirthTime replaces the birth-time source for a test and returns a func
// restoring the real one.
func SetBirthTime(f func(path string) (time.Time, bool)) (restore func()) {
	old := birthTime
	birthTime = f
	return func() { birthTime = old }
}

// RealBirthTime is the platform birth-time source.
var RealBirthTime = fileBirthTime
