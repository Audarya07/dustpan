package main

import (
	"testing"
	"time"

	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/scan"
	"github.com/Audarya07/agent-receipts/internal/secrets"
	"github.com/Audarya07/agent-receipts/internal/stats"
)

func TestRecordReport(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	res := scan.Result{Items: []item.Item{
		{ID: "aaaa1111", Decision: item.Decision{Class: item.Offer}, Secrets: []secrets.Finding{{Masked: "x"}}},
		{ID: "bbbb2222", Decision: item.Decision{Class: item.LeftAlone}},
	}}
	s := stats.Stats{LastNudge: now.Add(-2 * time.Hour), LastReport: now.Add(-48 * time.Hour), SecretKeys: []string{"old00000"}}
	recordReport(&s, res, now, 1234)
	recordReport(&s, res, now.Add(time.Minute), 9999)
	if s.ReportsRun != 2 || s.ItemsOffered != 1 || s.FirstReportMillis != 1234 || !s.FirstRun.Equal(now) {
		t.Errorf("%+v", s)
	}
	if s.NudgesFollowed != 1 { // second report must not count the same nudge again
		t.Errorf("NudgesFollowed = %d", s.NudgesFollowed)
	}
	if len(s.SecretKeys) != 2 || s.SecretKeys[0] != "aaaa1111" {
		t.Errorf("SecretKeys %v", s.SecretKeys)
	}
}
