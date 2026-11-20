// Package stats keeps the local, never-transmitted usage numbers.
package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Audarya07/dustpan/internal/env"
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

// ShareText is the anonymised summary printed by `stats --share`: counts and
// durations only, never paths, item IDs, secret keys or session names.
func ShareText(s Stats) string {
	gb := func(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }
	keep, first := s.KeepRate(), s.FirstReportString()
	var b strings.Builder
	fmt.Fprintf(&b, "%s usage summary\n", env.AppName)
	fmt.Fprintf(&b, "  reports run:            %d\n", s.ReportsRun)
	fmt.Fprintf(&b, "  recovered:              %s trashed, %s permanently deleted\n", gb(s.BytesTrashed), gb(s.BytesDeleted))
	fmt.Fprintf(&b, "  secrets found:          %d\n", len(s.SecretKeys))
	fmt.Fprintf(&b, "  real-secret answers:    %d real, %d not real, %d unsure\n", s.SecretReal, s.SecretNotReal, s.SecretUnsure)
	fmt.Fprintf(&b, "  items cleaned, kept:    %d cleaned, %d kept (keep rate %s)\n", s.ItemsCleaned, s.ItemsKept, keep)
	fmt.Fprintf(&b, "  nudges:                 %d shown, %d followed\n", s.NudgesShown, s.NudgesFollowed)
	fmt.Fprintf(&b, "  first report took:      %s\n", first)
	fmt.Fprintf(&b, "(generated locally by %s %s; nothing was sent anywhere)\n", env.AppName, env.VersionString())
	return b.String()
}

// KeepRate is kept/(cleaned+kept) as a percentage, or "n/a" with no answers.
func (s Stats) KeepRate() string {
	d := s.ItemsCleaned + s.ItemsKept
	if d == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d%%", int(float64(s.ItemsKept)/float64(d)*100+0.5))
}

// FirstReportString is how long the first report took, or "n/a".
func (s Stats) FirstReportString() string {
	if s.FirstReportMillis <= 0 {
		return "n/a"
	}
	return (time.Duration(s.FirstReportMillis) * time.Millisecond).String()
}
