package scan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/Audarya07/dustpan/internal/item"
	"github.com/Audarya07/dustpan/internal/receipts"
)

// CollectJobs returns the finished or stale Claude Code job folders as
// unevaluated items. Run is the full pipeline.
func CollectJobs(ctx context.Context, c Context) []item.Item {
	return itemsOf(collectJobCandidates(ctx, c, &runState{}))
}

func itemsOf(cands []candidate) []item.Item {
	var items []item.Item
	for _, cand := range cands {
		items = append(items, cand.it)
	}
	return items
}

// collectJobCandidates proposes every ~/.claude/jobs/<id>/ that is done,
// stale or empty. The running job's own folder is never proposed.
func collectJobCandidates(ctx context.Context, c Context, _ *runState) []candidate {
	e := c.Env
	jobsDir := filepath.Join(e.Home, ".claude", "jobs")
	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		return nil
	}
	days := c.Config.StaleJobDays
	if days <= 0 {
		days = 14
	}
	stale := time.Duration(days) * 24 * time.Hour
	canonJobs := e.Canon(jobsDir)
	var running string
	if e.JobDir != "" {
		running = e.Canon(e.JobDir)
	}
	var out []candidate
	for _, ent := range entries {
		if ctx.Err() != nil {
			break
		}
		// Real directories only: a symlink is never followed out of the jobs folder.
		if ent.Type()&os.ModeSymlink != 0 || !ent.IsDir() {
			continue
		}
		path := e.CanonParent(filepath.Join(jobsDir, ent.Name()))
		if filepath.Dir(path) != canonJobs {
			continue
		}
		if running != "" && (receipts.Within(path, running) || receipts.Within(running, path)) {
			continue
		}
		if reason, ev, ok := jobDeadReason(path, e.Now(), stale); ok {
			out = append(out, candidate{it: item.Item{
				Category: item.Job, Path: path, Evidence: ev, DeadReason: reason,
			}})
		}
	}
	return out
}

// jobDeadReason reads a job folder and says why it is dead, if it is.
func jobDeadReason(path string, now time.Time, stale time.Duration) (string, item.Evidence, bool) {
	ev := item.Evidence{Strong: true, Source: "agent-folder", Agent: "claude"}
	data, err := os.ReadFile(filepath.Join(path, "state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			if ents, rerr := os.ReadDir(path); rerr == nil && len(ents) == 0 {
				return "empty", ev, true
			}
		}
		return "", ev, false
	}
	var js struct {
		State     string          `json:"state"`
		Name      string          `json:"name"`
		SessionID string          `json:"sessionId"`
		UpdatedAt json.RawMessage `json:"updatedAt"`
	}
	if json.Unmarshal(data, &js) != nil {
		return "", ev, false
	}
	ev.SessionID, ev.SessionName = js.SessionID, js.Name
	if js.State == "done" {
		return "done", ev, true
	}
	if js.State != "blocked" && js.State != "working" {
		return "", ev, false
	}
	t, ok := parseTime(js.UpdatedAt)
	if !ok || now.Sub(t) < stale {
		return "", ev, false
	}
	if js.State == "blocked" {
		return "blocked since " + t.UTC().Format("Jan 2"), ev, true
	}
	return "stale since " + t.UTC().Format("Jan 2"), ev, true
}
