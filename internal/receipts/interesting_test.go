package receipts_test

import (
	"runtime"
	"testing"

	"github.com/Audarya07/agent-receipts/internal/receipts"
)

func TestInteresting(t *testing.T) {
	tmp := []string{"/private/tmp"}
	cases := []struct {
		path, cwd, via string
		want           bool
	}{
		{"/private/tmp/a.go", "/repo", "Write", true},
		{"/repo/main.go", "/repo", "Write", false},
		{"main.go", "/repo", "Write", false},
		{"../x", "/repo", "Write", true},
		{"/repo/.claude/worktrees/x", "/repo", "git worktree add", true},
		{"/Users/u/elsewhere/out", "/repo", "-o", true},
		{"/repo2/x", "/repo", "Write", true},
	}
	for _, c := range cases {
		if got := receipts.Interesting(c.path, c.cwd, tmp, c.via); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

func TestWithin(t *testing.T) {
	if !receipts.Within("/a/b", "/a") || !receipts.Within("/a", "/a") || !receipts.Within("/a/b/../c", "/a/") {
		t.Fatal("want within")
	}
	if receipts.Within("/ab", "/a") || receipts.Within("/", "/a") {
		t.Fatal("want not within")
	}
	if !receipts.Within("/a", "/") {
		t.Fatal("everything is within /")
	}
}

func TestWithinFold(t *testing.T) {
	if !receipts.WithinFold("/a/b/c", "/a/b") || receipts.WithinFold("/a/bc", "/a/b") {
		t.Fatal("WithinFold must behave like Within for same-case paths")
	}
	got := receipts.WithinFold("/A/B/c", "/a/b")
	if want := runtime.GOOS == "darwin"; got != want {
		t.Fatalf("WithinFold case variant = %v, want %v on %s", got, want, runtime.GOOS)
	}
}
