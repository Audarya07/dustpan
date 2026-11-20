package scan_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Audarya07/agent-receipts/internal/config"
	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/proc"
	"github.com/Audarya07/agent-receipts/internal/receipts"
	"github.com/Audarya07/agent-receipts/internal/scan"
	"github.com/Audarya07/agent-receipts/internal/secrets"
	"github.com/Audarya07/agent-receipts/internal/trash"
)

type fixture struct {
	t         *testing.T
	home, tmp string
	repo      string
	now       time.Time // the scan clock: fixtures are "3 days old"
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newFixtureHome(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &fixture{t: t, home: filepath.Join(root, "home"), tmp: filepath.Join(root, "tmp"),
		now: time.Now().Add(72 * time.Hour)}
	h.repo = filepath.Join(h.home, "repo")
	origin := filepath.Join(root, "origin.git")
	for _, d := range []string{h.repo, h.tmp, origin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run(t, origin, "init", "--bare", "-b", "main")
	run(t, h.repo, "init", "-b", "main")
	h.write(h.repo, "README.md", "hi")
	run(t, h.repo, "add", ".")
	run(t, h.repo, "commit", "-m", "init")
	run(t, h.repo, "remote", "add", "origin", origin)
	run(t, h.repo, "push", "-u", "origin", "main")
	run(t, h.repo, "remote", "set-head", "origin", "main")
	return h
}

func (h *fixture) abs(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(h.home, p)
}

func (h *fixture) tmpPath(name string) string { return filepath.Join(h.tmp, name) }

func (h *fixture) addWorktree(p string) string {
	h.t.Helper()
	p = h.abs(p)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	run(h.t, h.repo, "worktree", "add", "-b", "b-"+filepath.Base(p), p)
	return p
}

func (h *fixture) write(dir, name, content string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *fixture) index(rs ...receipts.Receipt) *receipts.Index {
	e := env.Env{Home: h.home}
	return receipts.BuildIndex(scan.CanonReceipts(e, rs))
}

func (h *fixture) context(ix *receipts.Index) scan.Context {
	now := h.now
	return scan.Context{
		Env: env.Env{Home: h.home, StateDir: filepath.Join(h.home, "state"), TmpDirs: []string{h.tmp},
			Now: func() time.Time { return now }},
		Config:  config.Config{Roots: []string{h.home}, StaleJobDays: 14},
		Index:   ix,
		Proc:    &proc.Snapshot{Procs: map[int]proc.Proc{}, OpenBy: map[string]int{}},
		Trash:   trash.Trash{Dir: filepath.Join(h.home, ".Trash")},
		Secrets: nil,
	}
}

func mustRun(t *testing.T, c scan.Context) scan.Result {
	t.Helper()
	res, err := scan.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func find(res scan.Result, path string) (item.Item, bool) {
	for _, it := range res.Items {
		if it.Path == path {
			return it, true
		}
	}
	return item.Item{}, false
}

func wantItem(t *testing.T, res scan.Result, path string, class item.Class) item.Item {
	t.Helper()
	it, ok := find(res, path)
	if !ok {
		t.Fatalf("no item for %s; have %+v", path, res.Items)
	}
	if it.Decision.Class != class {
		t.Fatalf("%s class = %s (%v), want %s", path, it.Decision.Class, it.Decision.Reasons, class)
	}
	return it
}

func wantDead(t *testing.T, res scan.Result, path, reason string) item.Item {
	t.Helper()
	it := wantItem(t, res, path, item.Offer)
	if it.DeadReason != reason {
		t.Fatalf("%s DeadReason = %q, want %q", path, it.DeadReason, reason)
	}
	return it
}

func wantNoItem(t *testing.T, res scan.Result, path string) {
	t.Helper()
	if _, ok := find(res, path); ok {
		t.Fatalf("unexpected item for %s", path)
	}
}

func TestWorktreeCollector(t *testing.T) {
	h := newFixtureHome(t)
	agentWT := h.addWorktree("repo/.claude/worktrees/x")
	tmpWT := h.addWorktree(h.tmpPath("wt-931"))
	handWT := h.addWorktree("handmade")
	dirtyWT := h.addWorktree("repo/.claude/worktrees/dirty")
	h.write(dirtyWT, "wip.txt", "x")
	ix := h.index(receipts.Receipt{Kind: receipts.KindCreated, Path: tmpWT, IsDir: true,
		Via: "git worktree add", SessionID: "s1", Source: "hook"})

	res := mustRun(t, h.context(ix))

	a := wantItem(t, res, agentWT, item.Offer)
	if a.Evidence.Source != "agent-folder" || a.DeadReason != "no commits" || a.ID == "" || a.Fingerprint == "" || a.Size == 0 {
		t.Fatalf("agent item: %+v", a)
	}
	tw := wantItem(t, res, tmpWT, item.Offer)
	if tw.Evidence.Source != "receipt" || tw.Evidence.SessionID != "s1" {
		t.Fatalf("tmp evidence: %+v", tw.Evidence)
	}
	wantNoItem(t, res, handWT)
	wantItem(t, res, dirtyWT, item.LeftAlone)
	wantNoItem(t, res, h.repo)
}

func TestPrunableWorktreeIsUnregisterOffer(t *testing.T) {
	h := newFixtureHome(t)
	gone := h.addWorktree("repo/.claude/worktrees/gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	res := mustRun(t, h.context(receipts.BuildIndex(nil)))
	it := wantItem(t, res, gone, item.Offer)
	if !it.Unregister || it.DeadReason != "folder already gone" {
		t.Fatalf("item: %+v", it)
	}
}

func TestRunningProcessKeepsWorktree(t *testing.T) {
	h := newFixtureHome(t)
	wt := h.addWorktree("repo/.claude/worktrees/busy")
	c := h.context(receipts.BuildIndex(nil))
	c.Proc = &proc.Snapshot{Procs: map[int]proc.Proc{}, Open: []string{wt}, OpenBy: map[string]int{}}
	wantItem(t, mustRun(t, c), wt, item.LeftAlone)
	c.Proc, c.ProcErr = nil, errors.New("lsof failed")
	wantItem(t, mustRun(t, c), wt, item.LeftAlone)
}

func TestSessionAliveSignals(t *testing.T) {
	h := newFixtureHome(t)
	wt := h.addWorktree("repo/.claude/worktrees/s")
	ix := h.index(receipts.Receipt{Kind: receipts.KindCreated, Path: wt, IsDir: true, SessionID: "live", Via: "git worktree add"})
	c := h.context(ix)
	now := c.Env.Now()

	// no signal: offered
	wantItem(t, mustRun(t, c), wt, item.Offer)

	// working job updated recently: kept
	jobDir := filepath.Join(h.home, ".claude", "jobs", "j1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.write(jobDir, "state.json", `{"sessionId":"live","state":"working","updatedAt":"`+
		now.Add(-time.Hour).UTC().Format(time.RFC3339)+`"}`)
	if alive, _ := scan.SessionAliveFunc(c)("live"); !alive {
		t.Fatal("working job should mean alive")
	}
	wantItem(t, mustRun(t, c), wt, item.LeftAlone)

	// stale job: not alive
	h.write(jobDir, "state.json", `{"sessionId":"live","state":"working","updatedAt":"`+
		now.Add(-15*24*time.Hour).UTC().Format(time.RFC3339)+`"}`)
	if alive, _ := scan.SessionAliveFunc(c)("live"); alive {
		t.Fatal("stale job should not count")
	}

	// transcript modified within 24h of now: alive
	proj := filepath.Join(h.home, ".claude", "projects", "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	h.write(proj, "live.jsonl", "{}")
	if err := os.Chtimes(filepath.Join(proj, "live.jsonl"), now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if alive, seen := scan.SessionAliveFunc(c)("live"); !alive || seen.IsZero() {
		t.Fatalf("recent transcript should mean alive, got %v %v", alive, seen)
	}
}

func TestRecheck(t *testing.T) {
	h := newFixtureHome(t)
	wt := h.addWorktree("repo/.claude/worktrees/r")
	c := h.context(receipts.BuildIndex(nil))
	res := mustRun(t, c)
	it := wantItem(t, res, wt, item.Offer)

	fresh, err := scan.Recheck(context.Background(), c, it)
	if err != nil || fresh.Decision.Class != item.Offer || fresh.Path != wt {
		t.Fatalf("recheck: %+v, %v", fresh, err)
	}
	h.write(wt, "new.txt", "x")
	fresh, err = scan.Recheck(context.Background(), c, it)
	if err != nil || fresh.Decision.Class != item.LeftAlone {
		t.Fatalf("recheck dirty: %+v, %v", fresh, err)
	}
	run(t, h.repo, "worktree", "remove", "--force", wt)
	if _, err := scan.Recheck(context.Background(), c, it); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gone: err = %v, want ErrNotExist", err)
	}
}

func TestWorktreeSecretScanSkipsTrackedFiles(t *testing.T) {
	h := newFixtureHome(t)
	const key = "AKIAIOSFODNN7ABCDEFG\n"
	h.write(h.repo, "tracked.env", "aws_access_key_id = "+key)
	run(t, h.repo, "add", ".")
	run(t, h.repo, "commit", "-m", "tracked")
	run(t, h.repo, "push")
	wt := h.addWorktree("repo/.claude/worktrees/sec")
	// Untracked file, but also make the tree otherwise clean enough to scan.
	h.write(wt, "untracked.env", "aws_access_key_id = "+key)
	c := h.context(receipts.BuildIndex(nil))
	sc, err := secrets.New()
	if err != nil {
		t.Fatal(err)
	}
	c.Secrets = sc
	it := wantItem(t, mustRun(t, c), wt, item.LeftAlone)
	if len(it.Secrets) != 1 || it.Secrets[0].Path != "untracked.env" {
		t.Fatalf("findings: %+v", it.Secrets)
	}
}

func TestMeasureDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "big"), make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	size, newest := scan.Measure(dir)
	if size != 10 || newest.IsZero() {
		t.Fatalf("size=%d newest=%v", size, newest)
	}
}

func TestAncestorReceiptsDoNotMakeHandmadeWorktreesOfferable(t *testing.T) {
	h := newFixtureHome(t)
	mine := h.addWorktree("wts/mine")
	for _, kind := range []receipts.Kind{receipts.KindCreated, receipts.KindWeakCreated} {
		ix := h.index(receipts.Receipt{Kind: kind, Path: filepath.Join(h.home, "wts"), IsDir: true, SessionID: "s1"})
		wantNoItem(t, mustRun(t, h.context(ix)), mine)
	}
}

func TestReceiptWithSymlinkedPathStillMatches(t *testing.T) {
	h := newFixtureHome(t)
	wt := h.addWorktree(h.tmpPath("at-sym"))
	link := filepath.Join(filepath.Dir(h.tmp), "tmplink")
	if err := os.Symlink(h.tmp, link); err != nil {
		t.Fatal(err)
	}
	ix := h.index(receipts.Receipt{Kind: receipts.KindCreated, Path: filepath.Join(link, "at-sym"), IsDir: true, SessionID: "s1", Via: "git worktree add"})
	wantItem(t, mustRun(t, h.context(ix)), wt, item.Offer)
}

func TestMeasureUnreadableDirGivesUnknownNewest(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	if _, newest := scan.Measure(dir); !newest.IsZero() {
		t.Fatalf("newest = %v, want zero on walk error", newest)
	}
}

// R31: a git that hangs (e.g. a lazy fetch waiting on the network) makes the
// re-check fail, so clean skips the worktree instead of acting on it.
func TestRecheckWorktreeHangingGitIsError(t *testing.T) {
	h := newFixtureHome(t)
	wt := h.addWorktree("repo/.claude/worktrees/hang")
	c := h.context(receipts.BuildIndex(nil))
	it := wantItem(t, mustRun(t, c), wt, item.Offer)
	fake := t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "git"), []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	fresh, err := scan.Recheck(ctx, c, it)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recheck with hanging git: %+v, %v; want a re-check error", fresh.Decision, err)
	}
}

// R32: only a "git worktree add" receipt for the exact path makes a worktree
// agent-made; `cp .env ../my-wt` records the same path with Via "cp".
func TestExactReceiptMustBeWorktreeAdd(t *testing.T) {
	h := newFixtureHome(t)
	mine := h.addWorktree("my-wt")
	cp := receipts.Receipt{Kind: receipts.KindCreated, Path: mine, IsDir: false, SessionID: "s1", Via: "cp", Source: "hook"}
	wantNoItem(t, mustRun(t, h.context(h.index(cp))), mine)
	add := cp
	add.Via, add.IsDir = "git worktree add", true
	wantItem(t, mustRun(t, h.context(h.index(add, cp))), mine, item.Offer)
}

// R32: git also marks a worktree prunable when its .git is broken while the
// folder exists; that must never become "unregister".
func TestPrunableButPresentWorktreeIsKept(t *testing.T) {
	h := newFixtureHome(t)
	wt := h.addWorktree("repo/.claude/worktrees/broken")
	if err := os.Remove(filepath.Join(wt, ".git")); err != nil {
		t.Fatal(err)
	}
	it := wantItem(t, mustRun(t, h.context(receipts.BuildIndex(nil))), wt, item.LeftAlone)
	if it.Unregister {
		t.Fatalf("broken worktree with its folder present marked unregister: %+v", it)
	}
}

// R32: a worktree path that is itself a symlink is kept as the link, never
// resolved to (and offered as) its target.
func TestSymlinkWorktreePathIsKept(t *testing.T) {
	h := newFixtureHome(t)
	wt := h.addWorktree("repo/.claude/worktrees/lnk")
	target := filepath.Join(h.home, "elsewhere", "t")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(wt, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, wt); err != nil {
		t.Fatal(err)
	}
	ix := h.index(receipts.Receipt{Kind: receipts.KindCreated, Path: target, IsDir: true, SessionID: "s1",
		Via: "git worktree add", Source: "hook"})
	res := mustRun(t, h.context(ix))
	it := wantItem(t, res, wt, item.LeftAlone)
	if len(it.Decision.Reasons) == 0 || it.Decision.Reasons[0] != "worktree path is a symlink" {
		t.Fatalf("reasons: %v", it.Decision.Reasons)
	}
	wantNoItem(t, res, target)
}
