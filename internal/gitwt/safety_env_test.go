package gitwt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGit puts a shell script named git first on PATH.
func fakeGit(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// R31: git never prompts, never lazy-fetches, never uses a transport, and
// still drops redirecting GIT_* variables.
func TestGitRunsWithoutNetworkOrPrompts(t *testing.T) {
	out := filepath.Join(t.TempDir(), "seen")
	fakeGit(t, `env > "`+out+`"; echo "ARGS $*" >> "`+out+`"`)
	t.Setenv("GIT_DIR", "/elsewhere")
	if _, err := git(bg, t.TempDir(), "status"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// Keep only git's own variables and the arguments: failure output must
	// not dump the whole environment.
	var kept []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "GIT_") || strings.HasPrefix(l, "ARGS ") {
			kept = append(kept, l)
		}
	}
	seen := strings.Join(kept, "\n") + "\n"
	for _, want := range []string{"GIT_NO_LAZY_FETCH=1\n", "GIT_TERMINAL_PROMPT=0\n", "GIT_OPTIONAL_LOCKS=0\n", "ARGS -c protocol.allow=never -C "} {
		if !strings.Contains(seen, want) {
			t.Errorf("git invocation lacks %q:\n%s", want, seen)
		}
	}
	if strings.Contains(seen, "GIT_DIR=") {
		t.Error("GIT_DIR leaked into git's environment")
	}
}

// R31: a hanging git is killed by the per-call timeout or the caller's
// context, and that is an error (callers keep the item).
func TestGitHangIsBoundedError(t *testing.T) {
	fakeGit(t, "sleep 30") // a child that keeps the output pipes open
	old := callTimeout
	callTimeout = 300 * time.Millisecond
	t.Cleanup(func() { callTimeout = old })
	start := time.Now()
	if _, err := List(bg, t.TempDir()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("List on hanging git: err = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
	callTimeout = time.Minute
	ctx, cancel := context.WithTimeout(bg, 200*time.Millisecond)
	defer cancel()
	if _, err := Inspect(ctx, Worktree{Path: t.TempDir()}, ""); err == nil {
		t.Fatal("Inspect on hanging git with a short ctx: want error")
	}
	done, cancel2 := context.WithCancel(bg)
	cancel2()
	if _, err := RepoOf(done, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx: err = %v", err)
	}
}
