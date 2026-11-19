package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Audarya07/agent-receipts/internal/gitwt"
	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/receipts"
)

// CollectWorktrees returns the abandoned-worktree candidates as unevaluated
// items (no size, decision or secrets). Run is the full pipeline.
func CollectWorktrees(ctx context.Context, c Context) []item.Item {
	var items []item.Item
	for _, cand := range collectWorktreeCandidates(ctx, c, &runState{}) {
		items = append(items, cand.it)
	}
	return items
}

// expandRoot resolves a leading "~" against home.
func expandRoot(home, p string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	}
	return p
}

// agentFolderDirs lists the per-worktree directories inside the agent-owned
// worktree folders under home.
func agentFolderDirs(home string) []string {
	var out []string
	for _, pat := range []string{
		filepath.Join(home, ".codex", "worktrees", "*"),
		filepath.Join(home, ".cursor", "worktrees", "*", "*"),
	} {
		m, _ := filepath.Glob(pat)
		for _, p := range m {
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				out = append(out, p)
			}
		}
	}
	return out
}

// discoverRepos returns the canonical main checkout of every repo found under
// the configured roots, session working directories and agent folders, and
// records each in st.mainCheckouts.
func discoverRepos(ctx context.Context, c Context, st *runState) []string {
	e := c.Env
	var repos []string
	seen := map[string]bool{}
	add := func(r string) {
		r = e.Canon(r)
		if seen[r] {
			return
		}
		seen[r] = true
		repos = append(repos, r)
		st.addMain(r)
	}
	var roots []string
	for _, r := range c.Config.Roots {
		roots = append(roots, e.Canon(expandRoot(e.Home, r)))
	}
	var cwds []string
	if c.Index != nil {
		cwds = c.Index.Cwds()
	}
	for _, cwd := range cwds {
		roots = append(roots, e.Canon(cwd))
	}
	doneRoot := map[string]bool{}
	for _, root := range roots {
		if ctx.Err() != nil {
			return repos
		}
		if doneRoot[root] {
			continue
		}
		doneRoot[root] = true
		for _, r := range gitwt.FindRepos(root, 3) {
			add(r)
		}
	}
	// A session cwd may be inside a repo or inside one of its worktrees.
	for _, cwd := range cwds {
		if ctx.Err() != nil {
			return repos
		}
		cwd = e.Canon(cwd)
		if fi, err := os.Stat(cwd); err == nil && fi.IsDir() {
			if r, err := gitwt.RepoOf(cwd); err == nil {
				add(r)
			}
		}
	}
	for _, d := range agentFolderDirs(e.Home) {
		if r, err := gitwt.RepoOf(d); err == nil {
			add(r)
		}
	}
	return repos
}

// agentFolders returns the canonical folders whose worktrees are agent-made.
func agentFolders(c Context, repos []string) []string {
	e := c.Env
	dirs := []string{
		e.Canon(filepath.Join(e.Home, ".codex", "worktrees")),
		e.Canon(filepath.Join(e.Home, ".cursor", "worktrees")),
	}
	for _, r := range repos {
		dirs = append(dirs, filepath.Join(r, ".claude", "worktrees"))
	}
	return dirs
}

func collectWorktreeCandidates(ctx context.Context, c Context, st *runState) []candidate {
	repos := discoverRepos(ctx, c, st)
	folders := agentFolders(c, repos)
	var out []candidate
	seen := map[string]bool{}
	for _, repo := range repos {
		if ctx.Err() != nil {
			break
		}
		wts, err := gitwt.List(repo)
		if err != nil {
			continue
		}
		base := ""
		baseDone := false
		for _, wt := range wts {
			if wt.Main {
				st.addMain(c.Env.Canon(wt.Path))
				continue
			}
			path := c.Env.Canon(wt.Path)
			if seen[path] {
				continue
			}
			seen[path] = true
			if !baseDone && !wt.Prunable {
				base, baseDone = gitwt.BaseRef(repo), true
			}
			if cand, ok := worktreeCandidate(c, folders, repo, wt, base); ok {
				out = append(out, cand)
			}
		}
	}
	return out
}

// evidenceFor decides how we know an agent made the worktree at path.
func evidenceFor(c Context, folders []string, path, rawPath string) (item.Evidence, bool) {
	var ev item.Evidence
	if c.Index != nil {
		rec, ok := c.Index.StrongCreator(path)
		if !ok && rawPath != path {
			rec, ok = c.Index.StrongCreator(rawPath)
		}
		if ok {
			ev.Strong, ev.Source = true, rec.Source
			if ev.Source == "" || ev.Source == "hook" {
				ev.Source = "receipt"
			}
			ev.SessionID, ev.Agent, ev.SessionName = rec.SessionID, rec.Agent, rec.SessionName
			if s, found := c.Index.Sessions()[rec.SessionID]; found {
				if ev.Agent == "" {
					ev.Agent = s.Agent
				}
				if ev.SessionName == "" {
					ev.SessionName = s.Name
				}
			}
		}
		ev.Weak = c.Index.WeakCreators(path)
	}
	for _, f := range folders {
		if path != f && receipts.Within(path, f) {
			// The folder is the strongest proof; keep receipt details if any.
			ev.Strong, ev.Source = true, "agent-folder"
			break
		}
	}
	if !ev.Strong && len(ev.Weak) == 0 {
		return ev, false
	}
	return ev, true
}

// worktreeCandidate builds the candidate for one non-main worktree, or
// reports false when nothing ties it to an agent.
func worktreeCandidate(c Context, folders []string, repo string, wt gitwt.Worktree, base string) (candidate, bool) {
	path := c.Env.Canon(wt.Path)
	ev, ok := evidenceFor(c, folders, path, filepath.Clean(wt.Path))
	if !ok {
		return candidate{}, false
	}
	it := item.Item{
		Category: item.Worktree, Path: path, Repo: repo, Branch: wt.Branch, Evidence: ev,
	}
	if wt.Prunable {
		it.Unregister, it.DeadReason = true, "folder already gone"
		return candidate{it: it, git: &gitwt.State{Locked: wt.Locked}}, true
	}
	wt.Path = path
	state, err := gitwt.Inspect(wt, base)
	if err != nil {
		return candidate{it: it, gitErr: fmt.Errorf("inspect %s: %w", path, err)}, true
	}
	state.Locked = state.Locked || wt.Locked
	switch {
	case base != "" && state.CommitsBeyondBase == 0:
		it.DeadReason = "no commits"
	case state.UnsavedCommits == 0 && base != "":
		it.DeadReason = "merged"
	case state.UnsavedCommits == 0:
		it.DeadReason = "pushed"
	}
	return candidate{it: it, git: &state}, true
}

// recheckWorktree re-lists the item's repo and rebuilds its candidate.
func recheckWorktree(c Context, st *runState, it item.Item) (candidate, error) {
	ctx := context.Background()
	repos := discoverRepos(ctx, c, st)
	repo := c.Env.Canon(it.Repo)
	if it.Repo != "" {
		st.addMain(repo)
		found := false
		for _, r := range repos {
			found = found || r == repo
		}
		if !found {
			repos = append(repos, repo)
		}
	}
	gone := fmt.Errorf("worktree %s: %w", it.Path, os.ErrNotExist)
	wts, err := gitwt.List(repo)
	if err != nil {
		if _, serr := os.Stat(repo); os.IsNotExist(serr) {
			return candidate{}, gone
		}
		return candidate{}, fmt.Errorf("list worktrees of %s: %w", repo, err)
	}
	for _, wt := range wts {
		if wt.Main || c.Env.Canon(wt.Path) != it.Path {
			continue
		}
		base := ""
		if !wt.Prunable {
			base = gitwt.BaseRef(repo)
		}
		if cand, ok := worktreeCandidate(c, agentFolders(c, repos), repo, wt, base); ok {
			return cand, nil
		}
		break
	}
	return candidate{}, gone
}
