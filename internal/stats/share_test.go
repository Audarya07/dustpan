package stats_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Audarya07/dustpan/internal/stats"
)

func TestShareTextHasNoPaths(t *testing.T) {
	s := stats.Stats{
		ReportsRun: 3, ItemsCleaned: 3, ItemsKept: 1, BytesTrashed: 3_100_000_000,
		SecretKeys: []string{"abcd1234"}, SecretReal: 1, NudgesShown: 4, NudgesFollowed: 2,
		FirstRun: time.Now(), FirstReportMillis: 4200,
	}
	out := stats.ShareText(s)
	for _, bad := range []string{"/", "abcd1234"} {
		if strings.Contains(out, bad) {
			t.Errorf("share text contains %q:\n%s", bad, out)
		}
	}
	for _, want := range []string{"3.1 GB", "25%", "4 shown, 2 followed", "nothing was sent anywhere)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestShareTextZeroStats(t *testing.T) {
	out := stats.ShareText(stats.Stats{})
	if !strings.Contains(out, "keep rate n/a") {
		t.Errorf("want n/a keep rate:\n%s", out)
	}
}
