package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/Audarya07/agent-receipts/internal/refreshlock"
	"io"
	"os"
	"os/exec"
	"sort"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/receipts"
	"github.com/Audarya07/agent-receipts/internal/report"
	"github.com/Audarya07/agent-receipts/internal/scan"
	"github.com/Audarya07/agent-receipts/internal/stats"
	"github.com/Audarya07/agent-receipts/internal/transcripts"
)

const (
	reconstructBudget = 40 * time.Second
	scanBudget        = 55 * time.Second // from the start of the command
)

// useColor reports whether stdout is a terminal and NO_COLOR is unset.
func useColor(w io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// startDetachedReconstruct continues day-one reconstruction in a new
// session, stdio on /dev/null. It never waits for it.
func startDetachedReconstruct() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "reconstruct")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// collect runs the shared report/refresh pipeline: reconstruct (bounded),
// scan, cache and stats. It returns the result and how long it took.
func collect(e env.Env, shown bool) (scan.Result, error) {
	started := time.Now()
	if err := e.EnsureStateDir(); err != nil {
		return scan.Result{}, err
	}
	rctx, cancel := context.WithTimeout(context.Background(), reconstructBudget)
	done, rerr := transcripts.Reconstruct(rctx, e, receipts.Open(e.StateDir))
	cancel()
	if !done {
		startDetachedReconstruct()
	}
	if rerr != nil {
		done = false // a failed reconstruction only makes the report less complete
	}

	sctx, cancel := context.WithDeadline(context.Background(), started.Add(scanBudget))
	defer cancel()
	c, err := scan.NewContext(sctx, e)
	if err != nil {
		return scan.Result{}, err
	}
	res, err := scan.Run(sctx, c)
	if err != nil {
		return scan.Result{}, err
	}
	if !done {
		res.Incomplete = true
	}
	if err := report.SaveCache(e.StateDir, res); err != nil {
		return res, fmt.Errorf("save cache: %w", err)
	}
	millis := time.Since(started).Milliseconds()
	err = stats.Update(e.StateDir, func(s *stats.Stats) { recordScan(s, res, e.Now(), millis, shown) })
	if err != nil {
		return res, fmt.Errorf("update stats: %w", err)
	}
	return res, nil
}

func recordScan(s *stats.Stats, res scan.Result, now time.Time, millis int64, shown bool) {
	if s.FirstRun.IsZero() {
		s.FirstRun = now
	}
	if shown {
		recordShown(s, now, millis)
	}
	recordFindings(s, res)
}

// recordShown counts a report the user actually saw. A background refresh
// must never call it: it would corrupt "reports opened after a nudge".
func recordShown(s *stats.Stats, now time.Time, millis int64) {
	// A nudge in the last 24 h that no report has followed yet counts as followed.
	if !s.LastNudge.IsZero() && now.Sub(s.LastNudge) <= 24*time.Hour && s.LastReport.Before(s.LastNudge) {
		s.NudgesFollowed++
	}
	s.ReportsRun++
	s.LastReport = now
	if s.FirstReportMillis == 0 {
		s.FirstReportMillis = millis
	}
}

func recordFindings(s *stats.Stats, res scan.Result) {
	offered := 0
	keys := map[string]bool{}
	for _, k := range s.SecretKeys {
		keys[k] = true
	}
	for _, it := range res.Items {
		if it.Decision.Class == item.Offer {
			offered++
		}
		if len(it.Secrets) > 0 {
			keys[it.ID] = true
		}
	}
	s.ItemsOffered = offered
	s.SecretKeys = s.SecretKeys[:0]
	for k := range keys {
		s.SecretKeys = append(s.SecretKeys, k)
	}
	sort.Strings(s.SecretKeys)
}

func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	details := fs.Bool("details", false, "list every item with its checks")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *details && *asJSON {
		fmt.Fprintf(stderr, "%s: report: --details and --json are mutually exclusive\n", env.AppName)
		return 2
	}
	res, err := collect(env.FromOS(), true)
	if err != nil && res.GeneratedAt.IsZero() {
		fmt.Fprintf(stderr, "%s: report: %v\n", env.AppName, err)
		return 1
	}
	switch {
	case *asJSON:
		if werr := report.WriteJSON(stdout, res); werr != nil {
			fmt.Fprintf(stderr, "%s: report: %v\n", env.AppName, werr)
			return 1
		}
	case *details:
		report.RenderDetails(stdout, res, useColor(stdout))
	default:
		report.RenderCompact(stdout, res, useColor(stdout))
	}
	if err != nil { // cache/stats trouble: the report itself was printed
		fmt.Fprintf(stderr, "%s: warning: %v\n", env.AppName, err)
	}
	return 0
}

// runRefresh updates the cache and stats without printing a report; the
// SessionEnd hook uses it.
func runRefresh(stderr io.Writer) int {
	e := env.FromOS()
	release, ok := refreshlock.TryAcquire(e.StateDir)
	if !ok {
		return 0 // another refresh is running
	}
	defer release()
	if _, err := collect(e, false); err != nil {
		fmt.Fprintf(stderr, "%s: refresh: %v\n", env.AppName, err)
		return 1
	}
	return 0
}
