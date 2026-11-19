package receipts

import (
	"path/filepath"
	"strings"
)

// Within reports whether path equals dir or lies beneath it. Both are cleaned
// first; the check is separator-aware so "/ab" is not within "/a". Pure string
// logic: no filesystem access, no symlink resolution.
func Within(path, dir string) bool {
	p, d := filepath.Clean(path), filepath.Clean(dir)
	if p == d {
		return true
	}
	if d == "/" {
		return strings.HasPrefix(p, "/")
	}
	return strings.HasPrefix(p, d+"/")
}

func abs(path, cwd string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

// Interesting reports whether a created path is worth recording: it came from
// "git worktree add", lives in a temp directory, or is outside the session cwd.
func Interesting(path, cwd string, tmpDirs []string, via string) bool {
	if via == "git worktree add" {
		return true
	}
	p := abs(path, cwd)
	for _, t := range tmpDirs {
		if Within(p, t) {
			return true
		}
	}
	return !Within(p, cwd)
}
