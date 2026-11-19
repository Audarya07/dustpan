package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Audarya07/agent-receipts/internal/item"
)

const (
	defaultClaudeRetentionDays = 30
	codexRetentionDays         = 30
)

// CollectTranscripts returns the session transcripts past their agent's
// retention as unevaluated items. Run is the full pipeline.
func CollectTranscripts(ctx context.Context, c Context) []item.Item {
	return itemsOf(collectTranscriptCandidates(ctx, c, &runState{}))
}

// claudeRetentionDays reads cleanupPeriodDays from ~/.claude/settings.json.
func claudeRetentionDays(home string) int {
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return defaultClaudeRetentionDays
	}
	var s struct {
		Days *float64 `json:"cleanupPeriodDays"`
	}
	if json.Unmarshal(data, &s) != nil || s.Days == nil || *s.Days < 1 {
		return defaultClaudeRetentionDays
	}
	return int(*s.Days)
}

func collectTranscriptCandidates(ctx context.Context, c Context, _ *runState) []candidate {
	e := c.Env
	now := e.Now()
	var out []candidate
	add := func(path, agent, session string, days int) {
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() {
			return
		}
		age := now.Sub(fi.ModTime())
		if age <= time.Duration(days)*24*time.Hour {
			return
		}
		out = append(out, candidate{it: item.Item{
			Category: item.Transcript, Path: e.Canon(path),
			Evidence:   item.Evidence{Strong: true, Source: "agent-folder", Agent: agent, SessionID: session},
			DeadReason: fmt.Sprintf("older than %dd", int(age/(24*time.Hour))),
		}})
	}
	days := claudeRetentionDays(e.Home)
	files, _ := filepath.Glob(filepath.Join(e.Home, ".claude", "projects", "*", "*.jsonl"))
	for _, f := range files {
		if ctx.Err() != nil {
			return out
		}
		add(f, "claude", strings.TrimSuffix(filepath.Base(f), ".jsonl"), days)
	}
	_ = filepath.WalkDir(filepath.Join(e.Home, ".codex", "sessions"), func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return fs.SkipAll
		}
		if err == nil && d.Type().IsRegular() && strings.HasSuffix(p, ".jsonl") {
			add(p, "codex", "", codexRetentionDays)
		}
		return nil
	})
	return out
}
