// Package scan runs the collectors, applies the safety engine and fills in
// sizes and secrets.
package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Audarya07/agent-receipts/internal/config"
	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/gitwt"
	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/proc"
	"github.com/Audarya07/agent-receipts/internal/receipts"
	"github.com/Audarya07/agent-receipts/internal/safety"
	"github.com/Audarya07/agent-receipts/internal/secrets"
	"github.com/Audarya07/agent-receipts/internal/trash"
)

// Context is everything a scan needs. Proc and ProcErr come from the caller;
// collectors never take a process snapshot themselves.
type Context struct {
	Env     env.Env
	Config  config.Config
	Index   *receipts.Index
	Proc    *proc.Snapshot
	ProcErr error
	Trash   trash.Trash
	Secrets *secrets.Scanner
}

// Result is the outcome of one scan. Hidden items are dropped.
type Result struct {
	GeneratedAt time.Time
	Items       []item.Item
	Incomplete  bool
}

// candidate is an item proposed by a collector, with the git state the safety
// engine needs (worktrees only).
type candidate struct {
	it     item.Item
	git    *gitwt.State
	gitErr error
}

// runState is shared by the collectors and the evaluation of one run.
type runState struct {
	mainCheckouts []string // canonical main checkout of every repo discovered
	seenMain      map[string]bool
}

func (s *runState) addMain(path string) {
	if s.seenMain == nil {
		s.seenMain = map[string]bool{}
	}
	if !s.seenMain[path] {
		s.seenMain[path] = true
		s.mainCheckouts = append(s.mainCheckouts, path)
	}
}

// collector proposes candidates. Scratch runs after worktrees so it knows the
// main checkouts.
type collector func(ctx context.Context, c Context, st *runState) []candidate

var collectors = []collector{
	collectWorktreeCandidates, collectJobCandidates, collectScratchCandidates, collectTranscriptCandidates,
}

// Run collects, evaluates and enriches every item.
func Run(ctx context.Context, c Context) (Result, error) {
	res := Result{GeneratedAt: c.Env.Now()}
	st := &runState{}
	var cands []candidate
	for _, col := range collectors {
		if ctx.Err() != nil {
			res.Incomplete = true
			break
		}
		cands = append(cands, col(ctx, c, st)...)
	}
	alive := SessionAliveFunc(c)
	for _, cand := range cands {
		if ctx.Err() != nil {
			res.Incomplete = true
			break
		}
		it, partialErr := finish(ctx, c, st, alive, cand)
		if partialErr {
			res.Incomplete = true
		}
		if it.Decision.Class != item.Hidden {
			res.Items = append(res.Items, it)
		}
	}
	sort.SliceStable(res.Items, func(i, j int) bool {
		a, b := res.Items[i], res.Items[j]
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		return a.Path < b.Path
	})
	return res, nil
}

// finish canonicalizes, measures, evaluates and (unless hidden) secret-scans
// one candidate. The bool reports a secret scan that failed.
func finish(ctx context.Context, c Context, st *runState, alive func(string) (bool, time.Time), cand candidate) (item.Item, bool) {
	it := cand.it
	if it.Category == item.Worktree {
		it.Path = c.Env.Canon(it.Path)
	} else {
		// Never dereference the item itself: a symlink is measured and moved
		// as the link, not its target.
		it.Path = c.Env.CanonParent(it.Path)
	}
	if it.Repo != "" {
		it.Repo = c.Env.Canon(it.Repo)
	}
	it.ID = item.MakeID(it.Category, it.Path)
	if !it.Unregister {
		it.Size, it.Newest = Measure(it.Path)
	}
	it.Decision = safety.Evaluate(safety.Inputs{
		Item: it, Env: c.Env, Proc: c.Proc, ProcErr: c.ProcErr,
		Git: cand.git, GitErr: cand.gitErr,
		SessionAlive:    alive,
		TrashAvailable:  c.Trash.Available,
		MainCheckouts:   st.mainCheckouts,
		ExtraNeverTouch: c.Config.ExtraNeverTouch,
	})
	failed := false
	if it.Decision.Class != item.Hidden && !it.Unregister && c.Secrets != nil {
		var skip func(string, fs.DirEntry) bool
		if it.Category == item.Worktree {
			skip = worktreeSkip(it.Path)
		}
		found, partial, err := c.Secrets.Scan(ctx, it.Path, skip)
		it.Secrets, it.SecretsPartial = found, partial
		if err != nil {
			failed = true
			it.SecretsPartial = true
		}
	}
	head := ""
	if cand.git != nil {
		head = cand.git.HeadSHA
	}
	it.Fingerprint = item.Fingerprint(it.Size, it.Newest, head)
	return it, failed
}

// worktreeSkip skips git-tracked files (and .git) so only untracked and
// ignored files are scanned. If the tracked set can't be read, nothing but
// .git is skipped: scanning more is the safe direction.
func worktreeSkip(path string) func(string, fs.DirEntry) bool {
	tracked, err := gitwt.TrackedFiles(path)
	if err != nil {
		tracked = nil
	}
	return func(rel string, d fs.DirEntry) bool {
		if rel == ".git" {
			return true
		}
		return !d.IsDir() && tracked[filepath.ToSlash(rel)]
	}
}

// Measure returns the total size of regular files under path and the newest
// modification time of anything in it, never following symlinks.
func Measure(path string) (size int64, newest time.Time) {
	walkFailed := false
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if !(p == path && os.IsNotExist(err)) {
				walkFailed = true
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			walkFailed = true
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	if walkFailed {
		// Partial data: report "unknown" so the 24 h rule keeps the item.
		newest = time.Time{}
	}
	return size, newest
}

// CanonReceipts returns a copy of rs with Path (parent resolved, the final
// component left as written) and Cwd canonicalized, so
// receipts written with symlinked paths (e.g. /tmp on macOS) still match
// the canonical paths git and lsof report.
func CanonReceipts(e env.Env, rs []receipts.Receipt) []receipts.Receipt {
	out := make([]receipts.Receipt, len(rs))
	for i, r := range rs {
		if r.Path != "" {
			r.Path = e.CanonParent(r.Path)
		}
		if r.Cwd != "" {
			r.Cwd = e.Canon(r.Cwd)
		}
		out[i] = r
	}
	return out
}

// Recheck re-collects the single item at it.Path and returns it freshly
// evaluated. It wraps os.ErrNotExist when the item is gone.
func Recheck(ctx context.Context, c Context, it item.Item) (item.Item, error) {
	st := &runState{}
	var (
		cand candidate
		err  error
	)
	switch it.Category {
	case item.Worktree:
		cand, err = recheckWorktree(c, st, it)
	case item.Job, item.Scratch, item.Transcript:
		cand, err = recheckByCollecting(ctx, c, st, it)
	default:
		return item.Item{}, fmt.Errorf("recheck %s: unsupported category %q", it.Path, it.Category)
	}
	if err != nil {
		return item.Item{}, err
	}
	fresh, _ := finish(ctx, c, st, SessionAliveFunc(c), cand)
	return fresh, nil
}

// recheckByCollecting re-runs the item's category collector and picks out the
// one path. Not being proposed any more (deleted, resumed, changed) counts as
// gone.
func recheckByCollecting(ctx context.Context, c Context, st *runState, it item.Item) (candidate, error) {
	var col collector
	switch it.Category {
	case item.Job:
		col = collectJobCandidates
	case item.Scratch:
		discoverRepos(ctx, c, st) // fills st.mainCheckouts for the never-touch rules
		col = collectScratchCandidates
	default:
		col = collectTranscriptCandidates
	}
	// CanonParent, not Canon: a link item is identified by the link itself.
	path := c.Env.CanonParent(it.Path)
	for _, cand := range col(ctx, c, st) {
		if c.Env.CanonParent(cand.it.Path) == path {
			return cand, nil
		}
	}
	return candidate{}, fmt.Errorf("%s %s: %w", it.Category, it.Path, os.ErrNotExist)
}

// SessionAliveFunc returns a function reporting whether a session still looks
// active, and when it was last seen. A session is alive if its process is
// running, a Claude job for it is "working" and recently updated, or its
// transcript changed in the last 24 hours.
func SessionAliveFunc(c Context) func(string) (bool, time.Time) {
	var (
		jobs   map[string]time.Time
		loaded bool
	)
	return func(id string) (bool, time.Time) {
		now := c.Env.Now()
		var alive bool
		var last time.Time
		mark := func(t time.Time) {
			alive = true
			if t.After(last) {
				last = t
			}
		}
		var s receipts.Session
		if c.Index != nil {
			s = c.Index.Sessions()[id]
		}
		if s.PID != 0 && c.Proc != nil && c.Proc.Alive(s.PID, s.PIDStart) {
			mark(now)
		}
		if !loaded {
			jobs, loaded = workingJobs(c), true
		}
		if t, ok := jobs[id]; ok {
			mark(t)
		}
		if t, ok := transcriptTime(c, s, id); ok && now.Sub(t) < safety.RecentWindow {
			mark(t)
		}
		return alive, last
	}
}

// workingJobs maps session id to updatedAt for Claude jobs whose state is
// "working" and that were updated within the stale-job window.
func workingJobs(c Context) map[string]time.Time {
	days := c.Config.StaleJobDays
	if days <= 0 {
		days = 14
	}
	window := time.Duration(days) * 24 * time.Hour
	out := map[string]time.Time{}
	files, _ := filepath.Glob(filepath.Join(c.Env.Home, ".claude", "jobs", "*", "state.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var js struct {
			SessionID string          `json:"sessionId"`
			State     string          `json:"state"`
			UpdatedAt json.RawMessage `json:"updatedAt"`
		}
		if json.Unmarshal(data, &js) != nil || js.SessionID == "" || js.State != "working" {
			continue
		}
		t, ok := parseTime(js.UpdatedAt)
		if !ok || c.Env.Now().Sub(t) >= window {
			continue
		}
		if t.After(out[js.SessionID]) {
			out[js.SessionID] = t
		}
	}
	return out
}

// parseTime accepts an RFC 3339 string or a Unix timestamp (seconds or ms).
func parseTime(raw json.RawMessage) (time.Time, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		t, err := time.Parse(time.RFC3339, s)
		return t, err == nil
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		if n > 1e11 {
			return time.UnixMilli(int64(n)), true
		}
		return time.Unix(int64(n), 0), true
	}
	return time.Time{}, false
}

// transcriptTime returns the transcript's modification time, looking at the
// recorded path first and then ~/.claude/projects/*/<id>.jsonl.
func transcriptTime(c Context, s receipts.Session, id string) (time.Time, bool) {
	paths := []string{}
	if s.TranscriptPath != "" {
		paths = append(paths, s.TranscriptPath)
	}
	if id != "" && !strings.ContainsAny(id, "/*?[\\") {
		m, _ := filepath.Glob(filepath.Join(c.Env.Home, ".claude", "projects", "*", id+".jsonl"))
		paths = append(paths, m...)
	}
	var newest time.Time
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest, !newest.IsZero()
}
