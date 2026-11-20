package scan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Audarya07/dustpan/internal/item"
	"github.com/Audarya07/dustpan/internal/receipts"
)

const checkoutWalkDepth = 4

// checkoutWalkCap bounds how many entries the git-checkout walk may look at
// below one item; tests lower it.
var checkoutWalkCap = 10_000

var errTooManyEntries = errors.New("too many entries to check")

// gitCheckoutFunc returns the safety engine's git-checkout check for a
// non-worktree item: a scratch folder or job may hold (or sit in) a clone
// with unsaved work, which the worktree checks never see.
func gitCheckoutFunc(c Context, cat item.Category) func(string) (string, error) {
	return func(path string) (string, error) {
		stop, ok := containerOf(c, cat, path)
		if !ok {
			return "", fmt.Errorf("%s is not inside its %s folder", path, cat)
		}
		return gitCheckoutReason(path, stop)
	}
}

// containerOf returns the canonical folder a non-worktree item must live in:
// the ancestor walk stops there.
func containerOf(c Context, cat item.Category, path string) (string, bool) {
	e := c.Env
	var roots []string
	switch cat {
	case item.Scratch:
		for _, d := range e.TmpDirs {
			roots = append(roots, e.Canon(d))
		}
	case item.Job:
		roots = []string{e.Canon(filepath.Join(e.Home, ".claude", "jobs"))}
	case item.Transcript:
		roots = []string{
			e.Canon(filepath.Join(e.Home, ".claude", "projects")),
			e.Canon(filepath.Join(e.Home, ".codex", "sessions")),
		}
	}
	best := ""
	for _, r := range roots {
		if r != path && receipts.Within(path, r) && len(r) > len(best) {
			best = r
		}
	}
	return best, best != ""
}

// gitCheckoutReason reports "inside a git checkout" when an ancestor of path
// below stop has a .git entry, and "contains a git checkout" when one is
// found at most checkoutWalkDepth levels below path. Symlinks are never
// followed. Anything it cannot read, or a tree over checkoutWalkCap
// entries, is an error: the caller keeps the item.
func gitCheckoutReason(path, stop string) (string, error) {
	if filepath.Base(path) == ".git" {
		return "contains a git checkout", nil
	}
	for d := filepath.Dir(path); d != stop && receipts.Within(d, stop); d = filepath.Dir(d) {
		switch _, err := os.Lstat(filepath.Join(d, ".git")); {
		case err == nil:
			return "inside a git checkout", nil
		case !errors.Is(err, os.ErrNotExist):
			return "", err
		}
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", nil // a file or a symlink: nothing below it to look at
	}
	seen := 0
	var walk func(dir string, level int) (bool, error)
	walk = func(dir string, level int) (bool, error) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return false, err
		}
		seen += len(ents)
		if seen > checkoutWalkCap {
			return false, errTooManyEntries
		}
		for _, e := range ents {
			if e.Name() == ".git" {
				return true, nil
			}
		}
		if level+1 >= checkoutWalkDepth {
			return false, nil
		}
		for _, e := range ents {
			if e.IsDir() { // false for symlinks: never followed
				if found, err := walk(filepath.Join(dir, e.Name()), level+1); found || err != nil {
					return found, err
				}
			}
		}
		return false, nil
	}
	found, err := walk(path, 0)
	if err != nil {
		return "", err
	}
	if found {
		return "contains a git checkout", nil
	}
	return "", nil
}
