package safety_test

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/gitwt"
	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/proc"
	"github.com/Audarya07/agent-receipts/internal/safety"
	"github.com/Audarya07/agent-receipts/internal/secrets"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func now() time.Time { return t0 }

func offerableInputs(t *testing.T) safety.Inputs {
	t.Helper()
	return safety.Inputs{
		Item: item.Item{Category: item.Worktree, Path: "/Users/u/repo/.claude/worktrees/x",
			Newest:   t0.Add(-72 * time.Hour),
			Evidence: item.Evidence{Strong: true, Source: "agent-folder", SessionID: "s1"}},
		Env:            env.Env{Home: "/Users/u", TmpDirs: []string{"/private/tmp"}, Now: now},
		Proc:           &proc.Snapshot{Procs: map[int]proc.Proc{}, OpenBy: map[string]int{}},
		Git:            &gitwt.State{},
		SessionAlive:   func(string) (bool, time.Time) { return false, time.Time{} },
		TrashAvailable: func(string) bool { return true },
		MainCheckouts:  []string{"/Users/u/repo"},
	}
}

func with(in safety.Inputs, f func(*safety.Inputs)) safety.Inputs {
	if in.Proc != nil {
		c := *in.Proc
		in.Proc = &c
	}
	if in.Git != nil {
		c := *in.Git
		in.Git = &c
	}
	f(&in)
	return in
}

func wantClass(t *testing.T, d item.Decision, c item.Class) {
	t.Helper()
	if d.Class != c {
		t.Fatalf("class = %s (%v), want %s", d.Class, d.Reasons, c)
	}
}

func wantReason(t *testing.T, in safety.Inputs, c item.Class, sub string) {
	t.Helper()
	d := safety.Evaluate(in)
	wantClass(t, d, c)
	if !strings.Contains(strings.Join(d.Reasons, "; "), sub) {
		t.Fatalf("reasons %v lack %q", d.Reasons, sub)
	}
}

func TestEvaluateRules(t *testing.T) {
	base := offerableInputs(t)
	wantClass(t, safety.Evaluate(base), item.Offer)
	wantReason(t, with(base, func(i *safety.Inputs) { i.ProcErr = errors.New("lsof") }), item.LeftAlone, "couldn't check running processes")
	wantReason(t, with(base, func(i *safety.Inputs) { i.Proc = nil }), item.LeftAlone, "couldn't check running processes")
	wantReason(t, with(base, func(i *safety.Inputs) { i.Proc.Open = []string{i.Item.Path + "/x"} }), item.LeftAlone, "has files open")
	wantReason(t, with(base, func(i *safety.Inputs) {
		i.SessionAlive = func(string) (bool, time.Time) { return true, t0.Add(-4 * time.Minute) }
	}), item.LeftAlone, "session active 4 min ago")
	wantReason(t, with(base, func(i *safety.Inputs) {
		i.SessionAlive = func(string) (bool, time.Time) { return true, t0.Add(-10 * time.Second) }
	}), item.LeftAlone, "session active just now")
	wantReason(t, with(base, func(i *safety.Inputs) { i.SessionAlive = nil }), item.LeftAlone, "couldn't check whether the session")
	wantClass(t, safety.Evaluate(with(base, func(i *safety.Inputs) {
		i.SessionAlive = nil
		i.Item.Evidence.SessionID = ""
	})), item.Offer)
	wantReason(t, with(base, func(i *safety.Inputs) { i.Item.Newest = t0.Add(-2 * time.Hour) }), item.LeftAlone, "changed 2h ago")
	wantReason(t, with(base, func(i *safety.Inputs) { i.Item.Newest = time.Time{} }), item.LeftAlone, "last changed")
	wantReason(t, with(base, func(i *safety.Inputs) { i.Git.IgnoredKept = []string{".env"} }), item.LeftAlone, ".env")
	wantReason(t, with(base, func(i *safety.Inputs) { i.Git.OpInProgress = "rebase-merge" }), item.LeftAlone, "rebase")
	wantReason(t, with(base, func(i *safety.Inputs) { i.Git.Locked = true }), item.LeftAlone, "locked")
	wantReason(t, with(base, func(i *safety.Inputs) { i.GitErr = errors.New("x") }), item.LeftAlone, "couldn't check git state")
	wantReason(t, with(base, func(i *safety.Inputs) { i.Git = nil }), item.LeftAlone, "couldn't check git state")
	wantClass(t, safety.Evaluate(with(base, func(i *safety.Inputs) { i.Item.Evidence = item.Evidence{} })), item.Hidden)
	d := safety.Evaluate(with(base, func(i *safety.Inputs) { i.Item.Evidence = item.Evidence{Weak: []string{"s1"}} }))
	if d.Class != item.LeftAlone || !d.DetailsOnly {
		t.Fatal(d)
	}
	if safety.Evaluate(base).Permanent {
		t.Fatal("trash available: want not permanent")
	}
	if !safety.Evaluate(with(base, func(i *safety.Inputs) { i.TrashAvailable = func(string) bool { return false } })).Permanent {
		t.Fatal("want permanent")
	}
	if !safety.Evaluate(with(base, func(i *safety.Inputs) { i.TrashAvailable = nil })).Permanent {
		t.Fatal("nil trash: want permanent")
	}
}

func TestSecretsDoNotForcePermanent(t *testing.T) {
	in := with(offerableInputs(t), func(i *safety.Inputs) { i.Item.Secrets = []secrets.Finding{{RuleID: "r"}} })
	d := safety.Evaluate(in)
	wantClass(t, d, item.Offer)
	if d.Permanent {
		t.Fatal("secrets must not set Permanent")
	}
}

func TestUnregisterSkipsGitAndRecency(t *testing.T) {
	in := with(offerableInputs(t), func(i *safety.Inputs) {
		i.Item.Unregister = true
		i.Item.Newest = time.Time{}
		i.Git = nil
	})
	wantClass(t, safety.Evaluate(in), item.Offer)
	wantReason(t, with(in, func(i *safety.Inputs) { i.Proc = nil }), item.LeftAlone, "couldn't check running processes")
	wantReason(t, with(in, func(i *safety.Inputs) {
		i.SessionAlive = func(string) (bool, time.Time) { return true, t0 }
	}), item.LeftAlone, "session active")
	wantClass(t, safety.Evaluate(with(in, func(i *safety.Inputs) { i.Item.Path = "/Users/u/repo/src" })), item.Hidden)
}

func TestNeverTouchWinsOverEverything(t *testing.T) {
	in := with(offerableInputs(t), func(i *safety.Inputs) { i.Item.Path = "/Users/u/repo/src" })
	wantClass(t, safety.Evaluate(in), item.Hidden)
}

func TestNeverTouchRoots(t *testing.T) {
	e := env.Env{Home: "/Users/u", TmpDirs: []string{"/private/tmp"}, JobDir: "/Users/u/.claude/jobs/abc"}
	checkouts := []string{"/Users/u/repo"}
	for _, p := range []string{"/", "/Users", "/Users/u", "/private/tmp", "/Users/u/repo", "/Users/u/repo/src",
		"/Users/u/.claude/jobs/abc/tmp/x", "/Users/u/.claude/settings.json", "/private/tmp/claude-501", "/Users/u/.zshrc",
		"/private/tmp/claude-501/x/y", "/private/tmp/cc-daemon-1", "/private/tmp/cc-socks", "/Users/u/.claude/jobs",
		"/Users/u/repo/.claude/worktrees", "/Users/u/.claude.json", "/Users/u/x/CLAUDE.md", "/Users/u/.claude/projects/p/memory/a.md",
		"/Users/u/.codex/auth.json", "/Users/u/x/mcp.json", "/Users/u/.claude/.credentials.json", "/private", "/Users/u/keep/sub"} {
		ex := []string(nil)
		if p == "/Users/u/keep/sub" {
			ex = []string{"/Users/u/keep"}
		}
		if ok, _ := safety.NeverTouch(p, e, checkouts, ex); !ok {
			t.Errorf("%s should be never-touch", p)
		}
	}
	for _, p := range []string{"/private/tmp/my-bin", "/Users/u/repo/.claude/worktrees/x", "/Users/u/.claude/jobs/old",
		"/Users/u/repo/.claude/worktrees/x/src", "/Users/u/.claude/worktrees/y"} {
		if ok, why := safety.NeverTouch(p, e, checkouts, nil); ok {
			t.Errorf("%s wrongly never-touch: %s", p, why)
		}
	}
}

func TestNeverTouchSymlinkedRoots(t *testing.T) {
	dir := t.TempDir()
	real := dir + "/real"
	link := dir + "/link"
	if err := mkdirSymlink(real, link); err != nil {
		t.Fatal(err)
	}
	e := env.Env{Home: link}
	if ok, _ := safety.NeverTouch(real, e, nil, nil); !ok {
		t.Error("real path of symlinked home must be never-touch")
	}
	if ok, _ := safety.NeverTouch(real+"/.zshrc", e, []string{link + "/repo"}, nil); !ok {
		t.Error("dotfile under symlinked home")
	}
	if ok, _ := safety.NeverTouch(real+"/repo/a", env.Env{Home: "/nope"}, []string{link + "/repo"}, nil); !ok {
		t.Error("real path inside symlinked checkout must be never-touch")
	}
}

func mkdirSymlink(real, link string) error {
	if err := os.MkdirAll(real, 0o755); err != nil {
		return err
	}
	return os.Symlink(real, link)
}

func TestExtraAncestorNeverTouch(t *testing.T) {
	e := env.Env{Home: "/Users/u"}
	if ok, _ := safety.NeverTouch("/Users/u/keep", e, nil, []string{"/Users/u/keep/sub"}); !ok {
		t.Error("ancestor of an extra must be never-touch")
	}
}

func TestNeverTouchCaseVariantsDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case-insensitive filesystems only")
	}
	e := env.Env{Home: "/Users/u", TmpDirs: []string{"/private/tmp"}}
	for _, p := range []string{"/users/u", "/USERS/u/.zshrc", "/PRIVATE/TMP", "/private/TMP/claude-501", "/Users/u/Repo/src"} {
		if ok, _ := safety.NeverTouch(p, e, []string{"/Users/u/repo"}, nil); !ok {
			t.Errorf("%s should be never-touch", p)
		}
	}
}

func TestAgentContainers(t *testing.T) {
	e := env.Env{Home: "/Users/u"}
	for _, p := range []string{"/Users/u/.claude", "/Users/u/.codex", "/Users/u/.cursor", "/Users/u/.claude/jobs",
		"/Users/u/.claude/projects", "/Users/u/.claude/projects/p", "/Users/u/.codex/worktrees",
		"/Users/u/.codex/sessions", "/Users/u/.cursor/worktrees"} {
		if ok, _ := safety.NeverTouch(p, e, nil, nil); !ok {
			t.Errorf("%s should be never-touch", p)
		}
	}
	for _, p := range []string{"/Users/u/.claude/jobs/old", "/Users/u/.claude/projects/p/s.jsonl",
		"/Users/u/.codex/worktrees/a", "/Users/u/.codex/sessions/2026/x.jsonl", "/Users/u/.cursor/worktrees/a"} {
		if ok, why := safety.NeverTouch(p, e, nil, nil); ok {
			t.Errorf("%s wrongly never-touch: %s", p, why)
		}
	}
}

func TestUnregisterLockedAndNotPermanent(t *testing.T) {
	in := with(offerableInputs(t), func(i *safety.Inputs) {
		i.Item.Unregister = true
		i.Git = &gitwt.State{Locked: true}
	})
	wantReason(t, in, item.LeftAlone, "locked")
	in = with(in, func(i *safety.Inputs) {
		i.Git = nil
		i.TrashAvailable = func(string) bool { return false }
	})
	d := safety.Evaluate(in)
	wantClass(t, d, item.Offer)
	if d.Permanent {
		t.Fatal("unregister must not be permanent")
	}
}

func TestMergedSessionsKeepItemWhileAnyIsAlive(t *testing.T) {
	in := with(offerableInputs(t), func(i *safety.Inputs) {
		i.Item.Evidence.SessionID = "dead"
		i.Item.Evidence.Sessions = []string{"live"}
		i.SessionAlive = func(id string) (bool, time.Time) { return id == "live", t0.Add(-4 * time.Minute) }
	})
	wantReason(t, in, item.LeftAlone, "session active 4 min ago")
	wantReason(t, with(in, func(i *safety.Inputs) { i.SessionAlive = nil }), item.LeftAlone, "couldn't check whether the session")
	wantClass(t, safety.Evaluate(with(in, func(i *safety.Inputs) {
		i.SessionAlive = func(string) (bool, time.Time) { return false, time.Time{} }
	})), item.Offer)
}
