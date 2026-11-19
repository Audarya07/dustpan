package env_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Audarya07/agent-receipts/internal/env"
)

func want(t *testing.T, got, w string) {
	t.Helper()
	if got != w {
		t.Fatalf("got %q, want %q", got, w)
	}
}

func wantContains(t *testing.T, list []string, s string) {
	t.Helper()
	for _, x := range list {
		if x == s {
			return
		}
	}
	t.Fatalf("%v does not contain %q", list, s)
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFromOSHonorsXDGAndTmpdir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/x/state")
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("CLAUDE_JOB_DIR", "/jobs/abc")
	e := env.FromOS()
	want(t, e.StateDir, "/x/state/agent-receipts")
	want(t, e.ConfigDir, "/x/cfg/agent-receipts")
	wantContains(t, e.TmpDirs, mustEval(t, tmp))
	wantContains(t, e.TmpDirs, mustEval(t, "/tmp"))
	want(t, e.JobDir, "/jobs/abc")
	if e.Now == nil || e.Now().IsZero() {
		t.Fatal("Now not set")
	}
}

func TestFromOSDefaultsAndDedupe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("TMPDIR", "/tmp")
	e := env.FromOS()
	want(t, e.Home, home)
	want(t, e.StateDir, filepath.Join(home, ".local/state/agent-receipts"))
	want(t, e.ConfigDir, filepath.Join(home, ".config/agent-receipts"))
	if len(e.TmpDirs) != 1 {
		t.Fatalf("TmpDirs not de-duplicated: %v", e.TmpDirs)
	}
}

func TestEnsureStateDir(t *testing.T) {
	e := env.Env{StateDir: filepath.Join(t.TempDir(), "a", "b")}
	if err := e.EnsureStateDir(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(e.StateDir)
	if err != nil || !fi.IsDir() {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestCanonTmpSymlink(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-specific /tmp symlink")
	}
	if r, err := filepath.EvalSymlinks("/tmp"); err != nil || r == "/tmp" {
		t.Skip("/tmp is not a symlink")
	}
	want(t, env.Env{}.Canon("/tmp/does-not-exist/x"), "/private/tmp/does-not-exist/x")
}

func TestCanonSymlinkedDir(t *testing.T) {
	root := mustEval(t, t.TempDir())
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	e := env.Env{}
	want(t, e.Canon(filepath.Join(link, "missing", "f")), filepath.Join(real, "missing", "f"))
	want(t, e.Canon(link), real)
	want(t, e.Canon(filepath.Join(real, "..", "real", "x")), filepath.Join(real, "x"))
}

func TestCanonNeverFails(t *testing.T) {
	want(t, env.Env{}.Canon("/nonexistent-root-zzz/a/../b"), "/nonexistent-root-zzz/b")
}

func TestCanonParentDoesNotDereferenceFinalComponent(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	real := filepath.Join(root, "real")
	target := filepath.Join(root, "target")
	for _, d := range []string{real, target} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(real, "foo")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(root, "dirlink")
	if err := os.Symlink(real, dirLink); err != nil {
		t.Fatal(err)
	}
	e := env.Env{}
	want(t, e.CanonParent(link), link)
	want(t, e.CanonParent(filepath.Join(dirLink, "foo")), link)
	want(t, e.CanonParent(filepath.Join(dirLink, "missing")), filepath.Join(real, "missing"))
	want(t, e.CanonParent("/"), "/")
}
