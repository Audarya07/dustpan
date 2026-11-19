// Package gitwt inspects git worktrees. It is safety-critical: a worktree may
// only be offered for removal when Inspect proves it holds no unsaved work.
// Whenever a git command needed for the proof fails, Inspect returns an error
// (callers keep the item) rather than guessing that the worktree is clean.
package gitwt

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Worktree is one entry of `git worktree list --porcelain`.
type Worktree struct {
	Repo, Path, Head, Branch         string
	Main, Detached, Locked, Prunable bool
}

// Regenerable lists directory names whose ignored contents can be rebuilt.
var Regenerable = []string{"node_modules", "dist", "build", "target", ".venv", "venv", "__pycache__", ".next", ".turbo", "coverage", ".pytest_cache", ".gradle", ".cache", ".parcel-cache", "out"}

// State is what Inspect found in a worktree.
type State struct {
	HeadSHA, Base                       string
	Uncommitted, Untracked, IgnoredKept []string
	HiddenFromStatus                    []string // assume-unchanged / skip-worktree files
	UnsavedCommits, CommitsBeyondBase   int
	DetachedUnreachable                 bool
	OpInProgress                        string
	Locked                              bool
}

// gitEnv returns the inherited environment without GIT_* variables (GIT_DIR,
// GIT_WORK_TREE, GIT_INDEX_FILE, ...) that would redirect git away from -C,
// keeping only the config-isolation ones.
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			switch strings.SplitN(kv, "=", 2)[0] {
			case "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM":
			default:
				continue
			}
		}
		env = append(env, kv)
	}
	return env
}

// git runs git in dir. It never takes optional locks and uses the C locale.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(gitEnv(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// List returns the worktrees of repo; the first is the main checkout.
func List(repo string) ([]Worktree, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var res []Worktree
	var cur *Worktree
	for _, line := range strings.Split(out, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			res = append(res, Worktree{Repo: repo, Path: val, Main: len(res) == 0})
			cur = &res[len(res)-1]
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	return res, nil
}

// RepoOf returns the main checkout path that path belongs to.
func RepoOf(path string) (string, error) {
	out, err := git(path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(out)
	if common == "" {
		return "", errors.New("empty git common dir")
	}
	return filepath.Dir(common), nil
}

// FindRepos returns root and directories at most depth levels below it that
// contain a .git directory. It skips node_modules and hidden directories and
// never follows symlinks.
func FindRepos(root string, depth int) []string {
	var res []string
	var walk func(dir string, level int)
	walk = func(dir string, level int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.Name() == ".git" && e.IsDir() { // DirEntry.IsDir is false for symlinks
				res = append(res, dir)
				break
			}
		}
		if level >= depth {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || name == "node_modules" || strings.HasPrefix(name, ".") {
				continue
			}
			walk(filepath.Join(dir, name), level+1)
		}
	}
	walk(root, 0)
	return res
}

func refExists(repo, ref string) bool {
	_, err := git(repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// BaseRef picks the ref work is considered saved into, or "" if none exists.
func BaseRef(repo string) string {
	if out, err := git(repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" && refExists(repo, ref) {
			return ref
		}
	}
	for _, ref := range []string{"origin/main", "origin/master", "main", "master"} {
		if refExists(repo, ref) {
			return ref
		}
	}
	return ""
}

func isRegenerable(path string) bool {
	for _, comp := range strings.Split(strings.Trim(path, "/"), "/") {
		for _, r := range Regenerable {
			if comp == r {
				return true
			}
		}
	}
	return false
}

// parseStatus splits `status --porcelain=v1 -z --ignored=matching` output.
func parseStatus(out string, st *State) error {
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts); i++ {
		e := parts[i]
		if e == "" {
			continue
		}
		if len(e) < 4 || e[2] != ' ' {
			return fmt.Errorf("unparseable git status entry %q", e)
		}
		xy, path := e[:2], e[3:]
		if xy[0] == 'R' || xy[0] == 'C' || xy[1] == 'R' || xy[1] == 'C' {
			i++ // skip the original-path entry
		}
		switch xy {
		case "??":
			st.Untracked = append(st.Untracked, path)
		case "!!":
			if !isRegenerable(path) {
				st.IgnoredKept = append(st.IgnoredKept, path)
			}
		default:
			st.Uncommitted = append(st.Uncommitted, path)
		}
	}
	return nil
}

func lines(s string) []string {
	var res []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			res = append(res, l)
		}
	}
	return res
}

// Inspect reports everything in wt that would be lost by removing it.
// base is the ref work is merged into ("" if unknown, which makes every
// commit not on a remote count as unsaved).
func Inspect(wt Worktree, base string) (State, error) {
	st := State{Base: base}
	dir := wt.Path

	out, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		return State{}, err
	}
	st.HeadSHA = strings.TrimSpace(out)

	out, err = git(dir, "status", "--porcelain=v1", "-z", "--ignored=matching", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil {
		return State{}, err
	}
	if err := parseStatus(out, &st); err != nil {
		return State{}, err
	}
	sort.Strings(st.Uncommitted)
	sort.Strings(st.Untracked)
	sort.Strings(st.IgnoredKept)

	hidden, err := hiddenFiles(dir)
	if err != nil {
		return State{}, err
	}
	st.HiddenFromStatus = hidden

	// Commits not reachable from any remote ref or the base.
	revArgs := []string{"rev-list", "HEAD", "--not", "--remotes"}
	if base != "" {
		revArgs = append(revArgs, base)
	}
	out, err = git(dir, revArgs...)
	if err != nil {
		return State{}, err
	}
	candidates := map[string]bool{}
	for _, sha := range lines(out) {
		candidates[sha] = true
	}

	if base != "" {
		out, err = git(dir, "rev-list", "--count", base+"..HEAD")
		if err != nil {
			return State{}, err
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &st.CommitsBeyondBase); err != nil {
			return State{}, fmt.Errorf("unparseable rev-list count %q", out)
		}

		// Patches already in base (cherry-picked, rebased) are saved.
		out, err = git(dir, "cherry", base, "HEAD")
		if err != nil {
			return State{}, err
		}
		for _, l := range lines(out) {
			if sha, ok := strings.CutPrefix(l, "- "); ok {
				delete(candidates, strings.TrimSpace(sha))
			}
		}
	}
	st.UnsavedCommits = len(candidates)

	// Squash-merged: merging HEAD into base changes nothing.
	if base != "" && st.UnsavedCommits > 0 {
		merged, mErr := git(dir, "merge-tree", "--write-tree", base, "HEAD")
		var exitErr *exec.ExitError
		switch {
		case mErr == nil:
			baseTree, err := git(dir, "rev-parse", base+"^{tree}")
			if err != nil {
				return State{}, err
			}
			first, _, _ := strings.Cut(merged, "\n")
			if t := strings.TrimSpace(first); t != "" && t == strings.TrimSpace(baseTree) {
				st.UnsavedCommits = 0
			}
		case errors.As(mErr, &exitErr) && exitErr.ExitCode() == 1:
			// conflicts: HEAD has changes base lacks; leave the count as is
		default:
			return State{}, mErr
		}
	}

	if wt.Detached {
		out, err = git(dir, "for-each-ref", "--contains", "HEAD")
		if err != nil {
			return State{}, err
		}
		st.DetachedUnreachable = strings.TrimSpace(out) == ""
	}

	out, err = git(dir, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return State{}, err
	}
	gitDir := strings.TrimSpace(out)
	for _, op := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG"} {
		if _, err := os.Lstat(filepath.Join(gitDir, op)); err == nil {
			st.OpInProgress = op
			break
		}
	}
	st.Locked = wt.Locked
	if _, err := os.Lstat(filepath.Join(gitDir, "index.lock")); err == nil {
		st.Locked = true
	}
	return st, nil
}

// hiddenFiles lists tracked files git status cannot see changes in:
// assume-unchanged (lowercase ls-files tag) and skip-worktree (tag S). A
// skip-worktree file that is absent from disk in a sparse checkout is exempt.
func hiddenFiles(dir string) ([]string, error) {
	out, err := git(dir, "ls-files", "-v", "-z")
	if err != nil {
		return nil, err
	}
	sparse := false
	var res []string
	for _, e := range strings.Split(out, "\x00") {
		if e == "" {
			continue
		}
		if len(e) < 3 || e[1] != ' ' {
			return nil, fmt.Errorf("unparseable git ls-files entry %q", e)
		}
		tag, path := e[0], e[2:]
		switch {
		case tag >= 'a' && tag <= 'z':
		case tag == 'S':
			if !sparse {
				v, err := git(dir, "config", "--type=bool", "--get", "core.sparseCheckout")
				var ee *exec.ExitError
				if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) {
					return nil, err
				}
				sparse = strings.TrimSpace(v) == "true"
				if !sparse {
					res = append(res, path)
					continue
				}
			}
			if _, err := os.Lstat(filepath.Join(dir, path)); os.IsNotExist(err) {
				continue
			}
		default:
			continue
		}
		res = append(res, path)
	}
	sort.Strings(res)
	return res, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Unsaved returns human-readable reasons the worktree holds unsaved work;
// empty means nothing is unsaved.
func (s State) Unsaved() []string {
	var r []string
	if n := len(s.Uncommitted); n > 0 {
		r = append(r, plural(n, "uncommitted file", "uncommitted files"))
	}
	if n := len(s.Untracked); n > 0 {
		r = append(r, plural(n, "untracked file", "untracked files"))
	}
	if len(s.IgnoredKept) > 0 {
		r = append(r, "ignored files that aren't build output: "+strings.Join(s.IgnoredKept, ", "))
	}
	if len(s.HiddenFromStatus) > 0 {
		r = append(r, "files hidden from git status (assume-unchanged/skip-worktree): "+strings.Join(s.HiddenFromStatus, ", "))
	}
	if s.UnsavedCommits > 0 {
		r = append(r, plural(s.UnsavedCommits, "commit", "commits")+" not pushed or merged")
	}
	if s.DetachedUnreachable {
		r = append(r, "detached HEAD not on any branch")
	}
	if s.OpInProgress != "" {
		r = append(r, s.OpInProgress+" in progress")
	}
	if s.Locked {
		r = append(r, "worktree is locked")
	}
	return r
}

// Remove unregisters and deletes a worktree without --force. git only refuses
// for modified or untracked files it can see; it does NOT refuse for ignored
// files, assume-unchanged/skip-worktree edits, or untracked files hidden by
// status.showUntrackedFiles=no. Callers must Inspect immediately before Remove.
func Remove(repo, path string) error {
	_, err := git(repo, "worktree", "remove", path)
	return err
}

// TrackedFiles returns the set of files git tracks in the worktree at path,
// as slash-separated paths relative to it. It uses the same sanitized git
// environment as the rest of the package.
func TrackedFiles(path string) (map[string]bool, error) {
	out, err := git(path, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			set[f] = true
		}
	}
	return set, nil
}
