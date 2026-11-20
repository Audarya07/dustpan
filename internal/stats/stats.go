// Package stats keeps the local, never-transmitted usage numbers.
package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Stats are the local counters behind `stats`.
type Stats struct {
	FirstRun, LastReport time.Time
	ReportsRun           int
	ItemsOffered         int
	ItemsCleaned         int
	ItemsKept            int
	BytesTrashed         int64
	BytesDeleted         int64
	SecretKeys           []string
	SecretReal           int
	SecretNotReal        int
	SecretUnsure         int
	NudgesShown          int
	NudgesFollowed       int
	LastNudge            time.Time
	FirstReportMillis    int64
}

const (
	fileName = "stats.json"
	lockName = "stats.lock"
)

// Load reads the stats file. Missing or invalid yields the zero value.
func Load(stateDir string) Stats {
	var s Stats
	data, err := os.ReadFile(filepath.Join(stateDir, fileName))
	if err != nil || json.Unmarshal(data, &s) != nil {
		return Stats{}
	}
	return s
}

// Update loads the stats (missing or invalid becomes zero), applies fn and
// writes the result atomically, all under an exclusive flock so concurrent
// hooks and reports don't lose each other's updates.
func Update(stateDir string, fn func(*Stats)) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(filepath.Join(stateDir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := unix.Flock(int(lf.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lf.Fd()), unix.LOCK_UN)

	s := Load(stateDir)
	fn(&s)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, fileName+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(stateDir, fileName))
}
