package scan_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Audarya07/agent-receipts/internal/item"
)

// transcript writes a Claude transcript last modified ageDays before the scan clock.
func (h *fixture) transcript(project, session string, ageDays int) string {
	h.t.Helper()
	dir := h.mkdir(filepath.Join(h.home, ".claude", "projects", project))
	h.write(dir, session+".jsonl", "{}")
	p := filepath.Join(dir, session+".jsonl")
	h.age(p, ageDays)
	return p
}

func (h *fixture) age(p string, days int) {
	h.t.Helper()
	at := h.now.Add(-time.Duration(days) * 24 * time.Hour)
	if err := os.Chtimes(p, at, at); err != nil {
		h.t.Fatal(err)
	}
}

func TestTranscriptsRespectCleanupPeriod(t *testing.T) {
	h := newFixtureHome(t)
	h.mkdir(filepath.Join(h.home, ".claude"))
	h.write(filepath.Join(h.home, ".claude"), "settings.json", `{"cleanupPeriodDays": 7}`)
	old := h.transcript("-Users-me-proj", "old-sess", 10)
	recent := h.transcript("-Users-me-proj", "new-sess", 3)
	res := mustRun(t, h.context(nil))
	it := wantDead(t, res, old, "older than 10d")
	if it.Category != item.Transcript || it.Evidence.Source != "agent-folder" || it.Evidence.SessionID != "old-sess" {
		t.Fatalf("item: %+v", it)
	}
	wantNoItem(t, res, recent)
	wantNoItem(t, res, filepath.Dir(old))
}

func TestTranscriptsDefaultsAndCodex(t *testing.T) {
	h := newFixtureHome(t)
	c29 := h.transcript("p", "a", 29)
	c31 := h.transcript("p", "b", 31)
	cdx := h.mkdir(filepath.Join(h.home, ".codex", "sessions", "2026", "09", "01"))
	h.write(cdx, "rollout-old.jsonl", "{}")
	h.write(cdx, "rollout-new.jsonl", "{}")
	h.write(cdx, "notes.txt", "{}")
	h.age(filepath.Join(cdx, "rollout-old.jsonl"), 45)
	h.age(filepath.Join(cdx, "rollout-new.jsonl"), 5)
	h.age(filepath.Join(cdx, "notes.txt"), 45)
	res := mustRun(t, h.context(nil))
	wantNoItem(t, res, c29)
	wantDead(t, res, c31, "older than 31d")
	cx := wantDead(t, res, filepath.Join(cdx, "rollout-old.jsonl"), "older than 45d")
	if cx.Evidence.Agent != "codex" || cx.Evidence.SessionID != "" {
		t.Fatalf("codex evidence: %+v", cx.Evidence)
	}
	wantNoItem(t, res, filepath.Join(cdx, "rollout-new.jsonl"))
	wantNoItem(t, res, filepath.Join(cdx, "notes.txt"))
}

func TestTranscriptOfLiveSessionIsLeftAlone(t *testing.T) {
	h := newFixtureHome(t)
	p := h.transcript("p", "live", 40)
	h.job("j", `{"state":"working","sessionId":"live","updatedAt":"`+h.now.Add(-time.Hour).Format(time.RFC3339)+`"}`)
	wantItem(t, mustRun(t, h.context(nil)), p, item.LeftAlone)
}

func TestTranscriptsSkipSymlinkedProjectDir(t *testing.T) {
	h := newFixtureHome(t)
	notes := h.mkdir(filepath.Join(h.home, "notes"))
	h.write(notes, "data.jsonl", "{}")
	h.age(filepath.Join(notes, "data.jsonl"), 60)
	projects := h.mkdir(filepath.Join(h.home, ".claude", "projects"))
	if err := os.Symlink(notes, filepath.Join(projects, "evil")); err != nil {
		t.Fatal(err)
	}
	res := mustRun(t, h.context(nil))
	wantNoItem(t, res, filepath.Join(notes, "data.jsonl"))
	wantNoItem(t, res, filepath.Join(projects, "evil", "data.jsonl"))
	if len(res.Items) != 0 {
		t.Fatalf("items: %+v", res.Items)
	}
}
