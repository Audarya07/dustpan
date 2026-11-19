package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// run executes a git command in dir with a hermetic environment (set by newRepo).
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// hermetic isolates git from the owner's config and environment.
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

// newRepo creates a repo on branch main with one commit and a bare origin
// that main has been pushed to. Returns the repo path.
func newRepo(t *testing.T) string {
	t.Helper()
	hermetic(t)
	root := realpath(t, t.TempDir())
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, root, "-c", "init.defaultBranch=main", "init", "--bare", origin)
	run(t, root, "-c", "init.defaultBranch=main", "init", repo)
	run(t, repo, "remote", "add", "origin", origin)
	write(t, filepath.Join(repo, "a.txt"), "v0\n")
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-m", "init")
	run(t, repo, "push", "origin", "main")
	run(t, repo, "fetch", "origin")
	return repo
}

// addWT creates a worktree on a new branch next to the repo.
func addWT(t *testing.T, repo, name string) Worktree {
	t.Helper()
	p := filepath.Join(filepath.Dir(repo), name)
	run(t, repo, "worktree", "add", "-b", name, p)
	return Worktree{Repo: repo, Path: p, Branch: name}
}

func TestInspectCleanNoCommits(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.CommitsBeyondBase != 0 || st.UnsavedCommits != 0 {
		t.Fatalf("unexpected state %+v", st)
	}
	if u := st.Unsaved(); len(u) != 0 {
		t.Fatalf("want nothing unsaved, got %v", u)
	}
	if st.HeadSHA == "" || st.Base != "main" {
		t.Fatalf("HeadSHA/Base not set: %+v", st)
	}
}

func TestInspectDirty(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	write(t, filepath.Join(wt.Path, "wip.txt"), "x")
	write(t, filepath.Join(wt.Path, "a.txt"), "changed\n")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Untracked, []string{"wip.txt"}) {
		t.Fatalf("Untracked = %v", st.Untracked)
	}
	if !reflect.DeepEqual(st.Uncommitted, []string{"a.txt"}) {
		t.Fatalf("Uncommitted = %v", st.Uncommitted)
	}
	if len(st.Unsaved()) == 0 {
		t.Fatal("Unsaved must be non-empty")
	}
}

func TestInspectEnvIsKeptNodeModulesIsNot(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), ".env\nnode_modules/\nconfig/local.json\n")
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-m", "ignore")
	run(t, repo, "push", "origin", "main")
	wt := addWT(t, repo, "feat")
	write(t, filepath.Join(wt.Path, ".env"), "SECRET=1")
	write(t, filepath.Join(wt.Path, "node_modules", "pkg", "i.js"), "x")
	write(t, filepath.Join(wt.Path, "config", "local.json"), "{}")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".env", "config/local.json"}
	if !reflect.DeepEqual(st.IgnoredKept, want) {
		t.Fatalf("IgnoredKept = %v, want %v", st.IgnoredKept, want)
	}
	if len(st.Uncommitted)+len(st.Untracked) != 0 {
		t.Fatalf("unexpected dirty: %+v", st)
	}
	if len(st.Unsaved()) == 0 {
		t.Fatal("IgnoredKept must count as unsaved")
	}
}

func TestInspectOnlyNodeModulesIsClean(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, ".gitignore"), "node_modules/\n")
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-m", "ignore")
	run(t, repo, "push", "origin", "main")
	wt := addWT(t, repo, "feat")
	write(t, filepath.Join(wt.Path, "node_modules", "pkg", "i.js"), "x")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if u := st.Unsaved(); len(u) != 0 {
		t.Fatalf("want clean, got %v", u)
	}
}

func TestInspectUnpushedCommit(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	write(t, filepath.Join(wt.Path, "b.txt"), "b\n")
	run(t, wt.Path, "add", "-A")
	run(t, wt.Path, "commit", "-m", "b")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.UnsavedCommits != 1 || st.CommitsBeyondBase != 1 {
		t.Fatalf("state %+v", st)
	}
	if len(st.Unsaved()) == 0 {
		t.Fatal("must be unsaved")
	}
}

func TestInspectPushedCommit(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	write(t, filepath.Join(wt.Path, "b.txt"), "b\n")
	run(t, wt.Path, "add", "-A")
	run(t, wt.Path, "commit", "-m", "b")
	run(t, wt.Path, "push", "origin", "feat")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.UnsavedCommits != 0 || st.CommitsBeyondBase != 1 {
		t.Fatalf("state %+v", st)
	}
	if u := st.Unsaved(); len(u) != 0 {
		t.Fatalf("want clean, got %v", u)
	}
}

func TestInspectSquashMerged(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	write(t, filepath.Join(wt.Path, "a.txt"), "v1\n")
	run(t, wt.Path, "commit", "-am", "one")
	write(t, filepath.Join(wt.Path, "a.txt"), "v2\n")
	write(t, filepath.Join(wt.Path, "c.txt"), "c\n")
	run(t, wt.Path, "add", "-A")
	run(t, wt.Path, "commit", "-m", "two")
	// Squash the same final content onto main, without pushing the branch.
	write(t, filepath.Join(repo, "a.txt"), "v2\n")
	write(t, filepath.Join(repo, "c.txt"), "c\n")
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-m", "squash")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.UnsavedCommits != 0 {
		t.Fatalf("squash-merged: UnsavedCommits = %d", st.UnsavedCommits)
	}
	if st.CommitsBeyondBase != 2 {
		t.Fatalf("CommitsBeyondBase = %d", st.CommitsBeyondBase)
	}
}

func TestInspectNotSquashMergedWhenMainLacksChange(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	write(t, filepath.Join(wt.Path, "a.txt"), "v1\n")
	run(t, wt.Path, "commit", "-am", "one")
	write(t, filepath.Join(wt.Path, "only-here.txt"), "x\n")
	run(t, wt.Path, "add", "-A")
	run(t, wt.Path, "commit", "-m", "two")
	// main gets only part of the change.
	write(t, filepath.Join(repo, "a.txt"), "v1\n")
	run(t, repo, "commit", "-am", "partial")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.UnsavedCommits == 0 {
		t.Fatalf("must report unsaved commits: %+v", st)
	}
}

func TestInspectCherryPickedCommitIsSaved(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	write(t, filepath.Join(wt.Path, "b.txt"), "b\n")
	run(t, wt.Path, "add", "-A")
	run(t, wt.Path, "commit", "-m", "b")
	write(t, filepath.Join(wt.Path, "d.txt"), "d\n")
	run(t, wt.Path, "add", "-A")
	run(t, wt.Path, "commit", "-m", "d")
	// cherry-pick only the first onto main (patch-equivalent), leaving "d" unsaved
	first := run(t, wt.Path, "rev-parse", "HEAD~1")
	run(t, repo, "cherry-pick", first)
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.UnsavedCommits != 1 {
		t.Fatalf("UnsavedCommits = %d, want 1", st.UnsavedCommits)
	}
}

func TestInspectRebaseInProgress(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	gitdir := run(t, wt.Path, "rev-parse", "--path-format=absolute", "--git-dir")
	if err := os.Mkdir(filepath.Join(gitdir, "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.OpInProgress != "rebase-merge" {
		t.Fatalf("OpInProgress = %q", st.OpInProgress)
	}
	if len(st.Unsaved()) == 0 {
		t.Fatal("op in progress must be unsaved")
	}
}

func TestInspectDetachedUnreachable(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	run(t, wt.Path, "checkout", "--detach")
	write(t, filepath.Join(wt.Path, "b.txt"), "b\n")
	run(t, wt.Path, "add", "-A")
	run(t, wt.Path, "commit", "-m", "orphan")
	wt.Detached = true
	wt.Branch = ""
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !st.DetachedUnreachable {
		t.Fatalf("want DetachedUnreachable: %+v", st)
	}
	if len(st.Unsaved()) == 0 {
		t.Fatal("must be unsaved")
	}
}

func TestInspectDetachedReachableIsClean(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	run(t, wt.Path, "checkout", "--detach")
	wt.Detached = true
	wt.Branch = ""
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.DetachedUnreachable || len(st.Unsaved()) != 0 {
		t.Fatalf("want clean: %+v %v", st, st.Unsaved())
	}
}

func TestInspectErrorsWhenGitFails(t *testing.T) {
	repo := newRepo(t)
	wt := Worktree{Repo: repo, Path: filepath.Join(filepath.Dir(repo), "does-not-exist")}
	if _, err := Inspect(wt, "main"); err == nil {
		t.Fatal("want error for missing worktree path")
	}
	wt = addWT(t, repo, "feat")
	if _, err := Inspect(wt, "no-such-base"); err == nil {
		t.Fatal("want error for unknown base ref")
	}
}

func TestInspectLockedViaIndexLock(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	gitdir := run(t, wt.Path, "rev-parse", "--path-format=absolute", "--git-dir")
	write(t, filepath.Join(gitdir, "index.lock"), "")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Locked {
		t.Fatal("want Locked")
	}
}

func TestListAndRepoOf(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	l, err := List(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 || !l[0].Main || l[0].Path != repo || l[0].Branch != "main" {
		t.Fatalf("list = %+v", l)
	}
	if l[1].Main || l[1].Path != realpath(t, wt.Path) || l[1].Branch != "feat" || l[1].Head == "" || l[1].Repo != repo {
		t.Fatalf("list[1] = %+v", l[1])
	}
	for _, p := range []string{repo, wt.Path} {
		got, err := RepoOf(p)
		if err != nil || got != repo {
			t.Fatalf("RepoOf(%s) = %q, %v; want %q", p, got, err, repo)
		}
	}
}

func TestBaseRef(t *testing.T) {
	repo := newRepo(t)
	if got := BaseRef(repo); got != "origin/main" {
		t.Fatalf("BaseRef = %q", got)
	}
	run(t, repo, "remote", "set-head", "origin", "main")
	if got := BaseRef(repo); got != "origin/main" {
		t.Fatalf("BaseRef with origin/HEAD = %q", got)
	}
	run(t, repo, "update-ref", "-d", "refs/remotes/origin/main")
	run(t, repo, "update-ref", "-d", "refs/remotes/origin/HEAD")
	if got := BaseRef(repo); got != "main" {
		t.Fatalf("BaseRef fallback = %q", got)
	}
}

func TestRemoveAfterMoveUnregistersOnlyThat(t *testing.T) {
	repo := newRepo(t)
	a := addWT(t, repo, "wta")
	b := addWT(t, repo, "wtb")
	elsewhere := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(a.Path, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(b.Path); err != nil {
		t.Fatal(err)
	}
	if err := Remove(repo, a.Path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	l, err := List(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 {
		t.Fatalf("list = %+v", l)
	}
	if !l[0].Main || l[1].Branch != "wtb" || !l[1].Prunable {
		t.Fatalf("list = %+v", l)
	}
	if _, err := os.Stat(elsewhere); err != nil {
		t.Fatalf("moved dir must be untouched: %v", err)
	}
}

func TestRemoveRefusesDirtyWorktree(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	f := filepath.Join(wt.Path, "wip.txt")
	write(t, f, "precious")
	if err := Remove(repo, wt.Path); err == nil {
		t.Fatal("Remove must fail on a worktree with an untracked file")
	}
	if b, err := os.ReadFile(f); err != nil || string(b) != "precious" {
		t.Fatalf("file must survive: %v", err)
	}
}

func TestFindReposSkipsSymlinksAndNodeModules(t *testing.T) {
	hermetic(t)
	outside := realpath(t, t.TempDir())
	run(t, outside, "init")
	root := realpath(t, t.TempDir())
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("a/.git")
	mk("deep/er/b/.git")
	mk("deep/er/c/.git")
	mk("node_modules/x/.git")
	mk(".hidden/y/.git")
	mk("wt")
	write(t, filepath.Join(root, "wt", ".git"), "gitdir: /nowhere\n") // worktree: .git is a file
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	mk(".git")
	got := FindRepos(root, 2)
	want := []string{root, filepath.Join(root, "a")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FindRepos = %v, want %v", got, want)
	}
	got = FindRepos(root, 3)
	want = []string{root, filepath.Join(root, "a"), filepath.Join(root, "deep/er/b"), filepath.Join(root, "deep/er/c")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FindRepos depth 3 = %v, want %v", got, want)
	}
}

func TestInspectAssumeUnchangedEditIsUnsaved(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	run(t, wt.Path, "update-index", "--assume-unchanged", "a.txt")
	write(t, filepath.Join(wt.Path, "a.txt"), "edited\n")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.HiddenFromStatus, []string{"a.txt"}) || len(st.Unsaved()) == 0 {
		t.Fatalf("state %+v", st)
	}
}

func TestInspectSkipWorktreeEditIsUnsaved(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	run(t, wt.Path, "update-index", "--skip-worktree", "a.txt")
	write(t, filepath.Join(wt.Path, "a.txt"), "edited\n")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.HiddenFromStatus, []string{"a.txt"}) || len(st.Unsaved()) == 0 {
		t.Fatalf("state %+v", st)
	}
	// Absent from disk but sparse checkout NOT enabled: still unsaved.
	if err := os.Remove(filepath.Join(wt.Path, "a.txt")); err != nil {
		t.Fatal(err)
	}
	st, err = Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.HiddenFromStatus) != 1 {
		t.Fatalf("non-sparse absent skip-worktree must be flagged: %+v", st)
	}
}

func TestInspectSparseAbsentSkipWorktreeExempt(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	run(t, wt.Path, "config", "core.sparseCheckout", "true")
	run(t, wt.Path, "update-index", "--skip-worktree", "a.txt")
	if err := os.Remove(filepath.Join(wt.Path, "a.txt")); err != nil {
		t.Fatal(err)
	}
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.HiddenFromStatus) != 0 || len(st.Unsaved()) != 0 {
		t.Fatalf("want exempt: %+v %v", st, st.Unsaved())
	}
	// Reappearing on disk makes git clear the bit and status shows it as modified.
	write(t, filepath.Join(wt.Path, "a.txt"), "edited\n")
	st, err = Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Unsaved()) == 0 {
		t.Fatalf("edited file must be unsaved: %+v", st)
	}
}

func TestInspectIgnoresInheritedGitEnv(t *testing.T) {
	repo := newRepo(t)
	wt := addWT(t, repo, "feat")
	write(t, filepath.Join(wt.Path, "wip.txt"), "x")
	t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
	t.Setenv("GIT_WORK_TREE", repo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(repo, ".git", "index"))
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Untracked, []string{"wip.txt"}) {
		t.Fatalf("Untracked = %v", st.Untracked)
	}
}

func TestInspectShowUntrackedFilesNoConfig(t *testing.T) {
	repo := newRepo(t)
	run(t, repo, "config", "status.showUntrackedFiles", "no")
	wt := addWT(t, repo, "feat")
	write(t, filepath.Join(wt.Path, "wip.txt"), "x")
	st, err := Inspect(wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Untracked, []string{"wip.txt"}) {
		t.Fatalf("Untracked = %v", st.Untracked)
	}
}
