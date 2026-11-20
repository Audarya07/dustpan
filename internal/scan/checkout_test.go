package scan_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Audarya07/dustpan/internal/item"
	"github.com/Audarya07/dustpan/internal/receipts"
	"github.com/Audarya07/dustpan/internal/scan"
)

func wantKept(t *testing.T, res scan.Result, path, reason string) {
	t.Helper()
	it := wantItem(t, res, path, item.LeftAlone)
	if !strings.Contains(strings.Join(it.Decision.Reasons, "; "), reason) {
		t.Fatalf("%s reasons %v, want %q", path, it.Decision.Reasons, reason)
	}
}

// R27: an agent-made scratch folder holding a repo with an unpushed commit
// must not be offered.
func TestScratchContainingGitCheckoutIsKept(t *testing.T) {
	h := newFixtureHome(t)
	work := h.mkdir(h.tmpPath("work"))
	proj := h.mkdir(filepath.Join(work, "a", "proj"))
	run(t, proj, "init", "-b", "main")
	h.write(proj, "x.go", "package x")
	run(t, proj, "add", ".")
	run(t, proj, "commit", "-m", "unpushed")
	plain := h.mkdir(h.tmpPath("plain"))
	h.write(plain, "f", "x")
	res := mustRun(t, h.context(h.index(created(work, true, "s1"), created(plain, true, "s1"))))
	wantKept(t, res, work, "contains a git checkout")
	wantItem(t, res, plain, item.Offer)
}

func TestScratchInsideGitCheckoutIsKept(t *testing.T) {
	h := newFixtureHome(t)
	repo := h.mkdir(h.tmpPath("userrepo"))
	run(t, repo, "init", "-b", "main")
	out := filepath.Join(repo, "out.bin")
	h.write(repo, "out.bin", "x")
	res := mustRun(t, h.context(h.index(created(out, false, "s1"))))
	wantKept(t, res, out, "inside a git checkout")
}

func TestJobContainingGitCheckoutIsKept(t *testing.T) {
	h := newFixtureHome(t)
	d := h.job("withrepo", `{"state":"done"}`)
	h.mkdir(filepath.Join(d, "src", ".git"))
	plain := h.job("plain", `{"state":"done"}`)
	res := mustRun(t, h.context(receipts.BuildIndex(nil)))
	wantKept(t, res, d, "contains a git checkout")
	wantItem(t, res, plain, item.Offer)
}

// A tree too big to check fully is kept.
func TestScratchCheckoutWalkCapKeeps(t *testing.T) {
	h := newFixtureHome(t)
	t.Cleanup(scan.SetCheckoutWalkCap(20))
	big := h.mkdir(h.tmpPath("big"))
	for i := 0; i < 30; i++ {
		h.write(big, fmt.Sprint(i), "x")
	}
	small := h.mkdir(h.tmpPath("small"))
	h.write(small, "f", "x")
	res := mustRun(t, h.context(h.index(created(big, true, "s1"), created(small, true, "s1"))))
	wantKept(t, res, big, "couldn't check for git checkouts")
	wantItem(t, res, small, item.Offer)
}

// A .git below the depth bound or behind a symlink is not looked at; a
// symlink is never followed out of the item.
func TestScratchCheckoutWalkDoesNotFollowSymlinks(t *testing.T) {
	h := newFixtureHome(t)
	dir := h.mkdir(h.tmpPath("links"))
	if err := os.Symlink(h.repo, filepath.Join(dir, "repo")); err != nil {
		t.Fatal(err)
	}
	wantItem(t, mustRun(t, h.context(h.index(created(dir, true, "s1")))), dir, item.Offer)
}

// R27: every discovered worktree, not only main checkouts, is never-touch
// for scratch items.
func TestScratchInsideDiscoveredWorktreeIsHidden(t *testing.T) {
	h := newFixtureHome(t)
	wt := h.addWorktree(h.tmpPath("hand-wt")) // the user's own, no receipt
	out := filepath.Join(wt, "build-out")
	h.write(wt, "build-out", "x")
	res := mustRun(t, h.context(h.index(created(out, false, "s1"))))
	wantNoItem(t, res, out)
	wantNoItem(t, res, wt)
}
