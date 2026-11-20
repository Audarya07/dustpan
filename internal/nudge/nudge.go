// Package nudge decides whether to show the one-line session-start message.
// It backs off by itself when the user keeps ignoring it.
package nudge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/report"
	"github.com/Audarya07/agent-receipts/internal/scan"
)

const (
	fileName        = "nudge.json"
	interval        = 24 * time.Hour
	backoffAfter    = 3
	backoffGap      = 7 * 24 * time.Hour
	newBytesTrigger = int64(1_000_000_000)
)

// State is persisted at <StateDir>/nudge.json.
type State struct {
	LastShown     time.Time
	IgnoredStreak int
	LastBytes     int64
	LastSecretIDs []string
	// Judged is the LastShown value whose outcome (followed or ignored) has
	// already been counted, so repeated calls cannot inflate the streak.
	Judged time.Time
}

// Load reads the state. Missing or invalid yields the zero State.
func Load(stateDir string) (State, error) {
	var s State
	data, err := os.ReadFile(filepath.Join(stateDir, fileName))
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	if json.Unmarshal(data, &s) != nil {
		return State{}, nil
	}
	return s, nil
}

// Save writes the state atomically with mode 0600.
func Save(stateDir string, s State) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
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

// Decide returns the nudge message and whether to show it, plus the state to
// persist. lastReport is when the user last ran a report.
func Decide(now time.Time, cache scan.Result, st State, lastReport time.Time) (msg string, show bool, next State) {
	next = st
	if !st.LastShown.IsZero() && !st.Judged.Equal(st.LastShown) {
		windowEnd := st.LastShown.Add(interval)
		switch {
		case !lastReport.Before(st.LastShown) && !lastReport.After(windowEnd):
			next.IgnoredStreak = 0
			next.Judged = st.LastShown
		case !now.Before(windowEnd):
			next.IgnoredStreak++
			next.Judged = st.LastShown
		}
	}
	gap := interval
	if next.IgnoredStreak >= backoffAfter {
		gap = backoffGap
	}
	if !st.LastShown.IsZero() && now.Sub(st.LastShown) < gap {
		return "", false, next
	}

	var bytes int64
	var ids []string
	for _, it := range cache.Items {
		if it.Decision.DetailsOnly {
			continue
		}
		switch it.Decision.Class {
		case item.Offer:
			bytes += it.Size
			fallthrough
		case item.LeftAlone:
			if len(it.Secrets) > 0 {
				ids = append(ids, it.ID)
			}
		}
	}
	sort.Strings(ids)
	seen := map[string]bool{}
	for _, id := range st.LastSecretIDs {
		seen[id] = true
	}
	fresh := false
	for _, id := range ids {
		if !seen[id] {
			fresh = true
		}
	}
	if !fresh && bytes-st.LastBytes <= newBytesTrigger {
		return "", false, next
	}

	msg = fmt.Sprintf("%s: agents left %s on this machine", env.AppName, report.HumanSize(bytes))
	switch k := len(ids); {
	case k == 1:
		msg += ", 1 item looks like a secret"
	case k > 1:
		msg += fmt.Sprintf(", %d items look like secrets", k)
	}
	msg += fmt.Sprintf(". run '%s report'", env.AppName)
	next.LastShown = now
	next.LastBytes = bytes
	next.LastSecretIDs = ids
	return msg, true, next
}
