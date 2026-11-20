package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/report"
	"github.com/Audarya07/agent-receipts/internal/stats"
)

func runStats(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	share := fs.Bool("share", false, "print an anonymised summary you can paste anywhere")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s := stats.Load(env.FromOS().StateDir)
	if *share {
		fmt.Fprint(stdout, stats.ShareText(s))
		return 0
	}
	writeStats(stdout, s)
	return 0
}

func writeStats(w io.Writer, s stats.Stats) {
	when := func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		return t.Local().Format("2006-01-02 15:04")
	}
	fmt.Fprintf(w, "%s stats (local only, nothing is sent anywhere)\n\n", env.AppName)
	fmt.Fprintf(w, "  first run:           %s\n", when(s.FirstRun))
	fmt.Fprintf(w, "  last report:         %s\n", when(s.LastReport))
	fmt.Fprintf(w, "  reports run:         %d\n", s.ReportsRun)
	fmt.Fprintf(w, "  first report took:   %s\n", s.FirstReportString())
	fmt.Fprintf(w, "  items offered:       %d (latest report)\n", s.ItemsOffered)
	fmt.Fprintf(w, "  items cleaned:       %d\n", s.ItemsCleaned)
	fmt.Fprintf(w, "  items kept:          %d\n", s.ItemsKept)
	fmt.Fprintf(w, "  keep rate:           %s\n", s.KeepRate())
	fmt.Fprintf(w, "  moved to Trash:      %s\n", report.HumanSize(s.BytesTrashed))
	fmt.Fprintf(w, "  permanently deleted: %s\n", report.HumanSize(s.BytesDeleted))
	fmt.Fprintf(w, "  secret items found:  %d\n", len(s.SecretKeys))
	fmt.Fprintf(w, "  secret answers:      %d real, %d not real, %d unsure\n", s.SecretReal, s.SecretNotReal, s.SecretUnsure)
	fmt.Fprintf(w, "  nudges shown:        %d (followed by a report: %d)\n", s.NudgesShown, s.NudgesFollowed)
	fmt.Fprintf(w, "  last nudge:          %s\n", when(s.LastNudge))
	fmt.Fprintf(w, "\nrun '%s stats --share' for an anonymised summary.\n", env.AppName)
}
