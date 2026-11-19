package scan_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/receipts"
	"github.com/Audarya07/agent-receipts/internal/scan"
)

func (h *fixture) jobPath(id string) string { return filepath.Join(h.home, ".claude", "jobs", id) }

func (h *fixture) jobDir(id string) string {
	h.t.Helper()
	if err := os.MkdirAll(h.jobPath(id), 0o755); err != nil {
		h.t.Fatal(err)
	}
	return h.jobPath(id)
}

func (h *fixture) job(id, state string) string {
	h.t.Helper()
	d := h.jobDir(id)
	h.write(d, "state.json", state)
	return d
}

func TestJobsCollector(t *testing.T) {
	h := newFixtureHome(t)
	h.job("7f3a91c2", `{"state":"done","name":"nightly db backup","sessionId":"sess-1","updatedAt":"2026-10-15T07:07:24.496Z"}`)
	h.job("f591d7b5", `{"state":"blocked","name":"beta plan+impl","updatedAt":"2026-08-03T04:52:23Z"}`)
	h.job("stale", `{"state":"working","name":"y","updatedAt":"`+h.now.Add(-15*24*time.Hour).Format(time.RFC3339)+`"}`)
	h.job("fresh", `{"state":"working","name":"x","updatedAt":"`+h.now.Add(-time.Hour).Format(time.RFC3339)+`"}`)
	h.job("junk", `not json`)
	h.job("nostate", `{"name":"x"}`)
	h.jobDir("49aabbe2") // empty, no state.json
	h.job("current", `{"state":"done","name":"me"}`)
	h.write(h.jobPath("current"), "output.txt", "x")
	c := h.context(nil)
	c.Env.JobDir = h.jobPath("current")
	res := mustRun(t, c)

	done := wantDead(t, res, h.jobPath("7f3a91c2"), "done")
	if done.Evidence.Source != "agent-folder" || !done.Evidence.Strong ||
		done.Evidence.SessionID != "sess-1" || done.Evidence.SessionName != "nightly db backup" {
		t.Fatalf("evidence: %+v", done.Evidence)
	}
	wantDead(t, res, h.jobPath("f591d7b5"), "blocked since Aug 3")
	wantDead(t, res, h.jobPath("stale"), "stale since "+h.now.Add(-15*24*time.Hour).UTC().Format("Jan 2"))
	wantDead(t, res, h.jobPath("49aabbe2"), "empty")
	for _, id := range []string{"fresh", "junk", "nostate", "current"} {
		wantNoItem(t, res, h.jobPath(id))
	}
	wantNoItem(t, res, filepath.Join(h.jobPath("current"), "output.txt"))
}

func TestJobsCollectorSkipsSymlinkedJobFolder(t *testing.T) {
	h := newFixtureHome(t)
	h.jobDir("real")
	if err := os.Symlink(h.repo, h.jobPath("link")); err != nil {
		t.Fatal(err)
	}
	res := mustRun(t, h.context(nil))
	wantNoItem(t, res, h.jobPath("link"))
	wantNoItem(t, res, h.repo)
}

func TestWorkingJobKeepsItsSessionAlive(t *testing.T) {
	h := newFixtureHome(t)
	// A done job whose session has another job still working is left alone.
	h.job("a", `{"state":"done","sessionId":"s","updatedAt":"2026-01-01T00:00:00Z"}`)
	h.job("b", `{"state":"working","sessionId":"s","updatedAt":"`+h.now.Add(-time.Hour).Format(time.RFC3339)+`"}`)
	res := mustRun(t, h.context(nil))
	wantItem(t, res, h.jobPath("a"), item.LeftAlone)
}

func TestRecheckJobScratchTranscript(t *testing.T) {
	h := newFixtureHome(t)
	job := h.job("j", `{"state":"done"}`)
	scratch := h.tmpPath("my-bin")
	h.write(h.tmp, "my-bin", "x")
	tr := h.transcript("p", "tsess", 40)
	ix := h.index(receipts.Receipt{Kind: receipts.KindCreated, Path: scratch, SessionID: "s1", At: time.Now()})
	c := h.context(ix)
	res := mustRun(t, c)
	for _, p := range []string{job, scratch, tr} {
		it := wantItem(t, res, p, item.Offer)
		fresh, err := scan.Recheck(context.Background(), c, it)
		if err != nil || fresh.Decision.Class != item.Offer || fresh.Path != p || fresh.Fingerprint == "" {
			t.Fatalf("recheck %s: %+v, %v", p, fresh, err)
		}
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		if _, err := scan.Recheck(context.Background(), c, it); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("gone %s: err = %v, want ErrNotExist", p, err)
		}
	}
}
