// Package env describes the machine environment the tool runs in.
package env

import (
	"os"
	"path/filepath"
	"time"
)

// AppName is the only place the product name lives.
const AppName = "dustpan"

// Version is set via -ldflags "-X .../internal/env.Version=...".
var Version = "dev"

// Env holds the locations and clock the rest of the program depends on.
type Env struct {
	Home      string
	StateDir  string
	ConfigDir string
	TmpDirs   []string
	JobDir    string
	UID       int
	Now       func() time.Time
}

// FromOS builds an Env from the process environment.
func FromOS() Env {
	home, _ := os.UserHomeDir()
	e := Env{
		Home:      home,
		StateDir:  xdgDir("XDG_STATE_HOME", filepath.Join(home, ".local", "state")),
		ConfigDir: xdgDir("XDG_CONFIG_HOME", filepath.Join(home, ".config")),
		JobDir:    os.Getenv("CLAUDE_JOB_DIR"),
		UID:       os.Getuid(),
		Now:       time.Now,
	}
	seen := map[string]bool{}
	for _, d := range []string{"/tmp", os.Getenv("TMPDIR")} {
		if d == "" {
			continue
		}
		r, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue
		}
		r = filepath.Clean(r)
		if !seen[r] {
			seen[r] = true
			e.TmpDirs = append(e.TmpDirs, r)
		}
	}
	return e
}

func xdgDir(key, fallback string) string {
	base := os.Getenv(key)
	if base == "" {
		base = fallback
	}
	return filepath.Join(base, AppName)
}

// EnsureStateDir creates the state directory with mode 0700.
func (e Env) EnsureStateDir() error {
	return os.MkdirAll(e.StateDir, 0o700)
}

// CanonParent canonicalizes the parent directory of path and appends the
// final component unchanged, so the item itself is never dereferenced: a
// symlink stays a symlink rather than becoming its target.
func (e Env) CanonParent(path string) string {
	p := filepath.Clean(path)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if p == "/" {
		return p
	}
	return filepath.Join(e.Canon(filepath.Dir(p)), filepath.Base(p))
}

// Canon returns the cleaned absolute path with symlinks resolved on its
// longest existing ancestor; the non-existing remainder is appended
// unchanged. It never fails: if nothing resolves it returns the cleaned path.
func (Env) Canon(path string) string {
	p := filepath.Clean(path)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	rest := ""
	cur := p
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
