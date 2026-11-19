package proc_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Audarya07/agent-receipts/internal/proc"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestParsePSAndAlive(t *testing.T) {
	procs, err := proc.ParsePS("  101     1 Thu Nov 19 01:16:16 2026 /usr/local/bin/claude\n  202   101 Thu Nov 19 01:16:17 2026 /bin/zsh\n")
	must(t, err)
	s := &proc.Snapshot{Procs: procs}
	start := time.Date(2026, 11, 19, 1, 16, 16, 0, time.Local)
	if !s.Alive(101, start) {
		t.Fatal("want alive")
	}
	if s.Alive(101, start.Add(time.Hour)) {
		t.Fatal("PID reuse must not count as alive")
	}
	if s.Alive(999, start) {
		t.Fatal("missing pid alive")
	}
	p, agent, ok := s.AgentAncestor(202)
	if !ok || p.PID != 101 || agent != "claude" {
		t.Fatal(p, agent, ok)
	}
}

func TestParsePSSpacedCommAndPaddedDay(t *testing.T) {
	procs, err := proc.ParsePS(" 7 1 Mon Nov  9 01:02:03 2026 /Applications/Visual Studio Code.app/x\n")
	must(t, err)
	if p := procs[7]; p.Comm != "/Applications/Visual Studio Code.app/x" || p.Start.Day() != 9 {
		t.Fatal(p)
	}
	if _, err := proc.ParsePS("garbage line\n"); err == nil {
		t.Fatal("want error on malformed line")
	}
}

func TestAgentAncestorVariantsAndLimits(t *testing.T) {
	procs, err := proc.ParsePS("1 0 Thu Nov 19 01:00:00 2026 /sbin/launchd\n" +
		"2 1 Thu Nov 19 01:00:00 2026 /opt/bin/codex\n" +
		"3 2 Thu Nov 19 01:00:00 2026 /bin/sh\n" +
		"4 1 Thu Nov 19 01:00:00 2026 /bin/sh\n" +
		"5 6 Thu Nov 19 01:00:00 2026 /bin/sh\n" +
		"6 5 Thu Nov 19 01:00:00 2026 /bin/sh\n")
	must(t, err)
	s := &proc.Snapshot{Procs: procs}
	if p, a, ok := s.AgentAncestor(3); !ok || p.PID != 2 || a != "codex" {
		t.Fatal(p, a, ok)
	}
	if _, _, ok := s.AgentAncestor(4); ok {
		t.Fatal("no agent expected")
	}
	if _, _, ok := s.AgentAncestor(5); ok { // cycle must terminate
		t.Fatal("no agent expected in cycle")
	}
}

func TestParseLsofAndInUse(t *testing.T) {
	s := &proc.Snapshot{Open: proc.ParseLsofF("p42\nn/private/tmp/wt-931\np43\nn/private/tmp/wt-9310/x\n")}
	if ok, reason := s.InUse("/private/tmp/wt-931"); !ok || !strings.Contains(reason, "has files open here") {
		t.Fatal("want in use", reason)
	}
	if ok, _ := s.InUse("/private/tmp/at-93"); ok {
		t.Fatal("prefix must not match")
	}
}

func TestInUseReasonWithPID(t *testing.T) {
	procs := map[int]proc.Proc{42: {PID: 42, Comm: "/usr/bin/vim"}}
	paths, by := proc.ParseLsofPIDs("p42\nn/private/tmp/wt-931/f\n")
	s := &proc.Snapshot{Procs: procs, Open: paths, OpenBy: by}
	ok, reason := s.InUse("/private/tmp/wt-931")
	if !ok || reason != "process vim (42) has files open here" {
		t.Fatal(ok, reason)
	}
	s2 := &proc.Snapshot{Open: []string{"/a/b"}}
	if ok, reason := s2.InUse("/a"); !ok || reason != "a process has files open here" {
		t.Fatal(ok, reason)
	}
}

func TestTakeLive(t *testing.T) {
	begin := time.Now()
	s, err := proc.Take(context.Background())
	must(t, err)
	t.Logf("Take wall time: %v", time.Since(begin))
	if _, ok := s.Procs[os.Getpid()]; !ok {
		t.Fatal("own pid missing")
	}
	if len(s.Open) == 0 {
		t.Fatal("no open paths")
	}
}

func TestTakeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := proc.Take(ctx); err == nil {
		t.Fatal("want error on cancelled context")
	}
}

func TestAgentAncestorRealWorldComm(t *testing.T) {
	cases := []struct {
		comm, want string
	}{
		{"claude bg-spare", "claude"},
		{"/Users/u/.local/share/claude/versions/2.1.327", "claude"},
		{"/opt/homebrew/bin/codex", "codex"},
		{"/usr/local/bin/cursor-agent", "cursor-agent"},
		{"claudette", ""},
		{"/usr/bin/zsh", ""},
	}
	for _, c := range cases {
		procs, err := proc.ParsePS("9 1 Thu Nov 19 01:00:00 2026 " + c.comm + "\n10 9 Thu Nov 19 01:00:00 2026 /bin/sh\n")
		must(t, err)
		if procs[9].Comm != c.comm {
			t.Fatalf("comm %q parsed as %q", c.comm, procs[9].Comm)
		}
		s := &proc.Snapshot{Procs: procs}
		_, agent, ok := s.AgentAncestor(10)
		if ok != (c.want != "") || agent != c.want {
			t.Errorf("%q: got %q %v, want %q", c.comm, agent, ok, c.want)
		}
	}
}
