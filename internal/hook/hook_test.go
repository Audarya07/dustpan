package hook_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/hook"
	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/nudge"
	"github.com/Audarya07/agent-receipts/internal/proc"
	"github.com/Audarya07/agent-receipts/internal/receipts"
	"github.com/Audarya07/agent-receipts/internal/report"
	"github.com/Audarya07/agent-receipts/internal/scan"
)

func ctx() context.Context { return context.Background() }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	hook.Deps
	tmp    string
	spawns *int
	snap   *proc.Snapshot
}

func testDeps(t *testing.T) fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	tmp := filepath.Join(root, "tmp")
	must(t, os.MkdirAll(tmp, 0o755))
	state := filepath.Join(root, "state")
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	spawns := 0
	f := fixture{tmp: tmp, spawns: &spawns, snap: &proc.Snapshot{Procs: map[int]proc.Proc{}}}
	f.Deps = hook.Deps{
		Env:          env.Env{Home: root, StateDir: state, TmpDirs: []string{tmp}, Now: func() time.Time { return now }},
		Store:        receipts.Open(state),
		ParentPID:    100,
		Procs:        func(context.Context) (*proc.Snapshot, error) { return f.snap, nil },
		SpawnRefresh: func() error { spawns++; return nil },
	}
	return f
}

func payload(event, session, cwd, tool, input string) string {
	if input == "" {
		input = "{}"
	}
	return `{"session_id":"` + session + `","cwd":"` + cwd + `","hook_event_name":"` + event + `","tool_name":"` + tool + `","tool_input":` + input + `}`
}

func wantPaths(t *testing.T, rs []receipts.Receipt, kind receipts.Kind, paths ...string) {
	t.Helper()
	var got []string
	for _, r := range rs {
		if r.Kind == kind {
			got = append(got, r.Path)
		}
	}
	if strings.Join(got, ",") != strings.Join(paths, ",") {
		t.Fatalf("%s paths = %v, want %v", kind, got, paths)
	}
}

func TestPostToolUseBashRecordsTmpOutput(t *testing.T) {
	f := testDeps(t)
	in := payload("PostToolUse", "s1", "/repo", "Bash", `{"command":"go build -o `+f.tmp+`/my-bin ./cmd/x"}`)
	must(t, hook.Handle(ctx(), f.Deps, "PostToolUse", strings.NewReader(in), io.Discard))
	rs, _ := f.Store.Load()
	wantPaths(t, rs, receipts.KindCreated, filepath.Join(f.tmp, "my-bin"))
	if rs[0].Source != "hook" || rs[0].Via == "" {
		t.Errorf("receipt = %+v", rs[0])
	}
}

func TestPostToolUseWriteAndNotebook(t *testing.T) {
	f := testDeps(t)
	for _, c := range []struct{ tool, input string }{
		{"Write", `{"file_path":"` + f.tmp + `/w.txt"}`},
		{"NotebookEdit", `{"notebook_path":"` + f.tmp + `/n.ipynb"}`},
		{"Write", `{"file_path":"/repo/inside.txt"}`}, // inside cwd: not interesting
		{"Read", `{"file_path":"` + f.tmp + `/r.txt"}`},
	} {
		must(t, hook.Handle(ctx(), f.Deps, "PostToolUse", strings.NewReader(payload("PostToolUse", "s1", "/repo", c.tool, c.input)), io.Discard))
	}
	rs, _ := f.Store.Load()
	wantPaths(t, rs, receipts.KindCreated, filepath.Join(f.tmp, "w.txt"), filepath.Join(f.tmp, "n.ipynb"))
	if rs[0].Via != "Write" || rs[1].Via != "NotebookEdit" {
		t.Errorf("via = %q %q", rs[0].Via, rs[1].Via)
	}
}

func TestPostToolUseCanonicalizesSymlinkedTmp(t *testing.T) {
	f := testDeps(t)
	link := filepath.Join(filepath.Dir(f.tmp), "link")
	must(t, os.Symlink(f.tmp, link))
	in := payload("PostToolUse", "s1", "/repo", "Write", `{"file_path":"`+link+`/x.txt"}`)
	must(t, hook.Handle(ctx(), f.Deps, "PostToolUse", strings.NewReader(in), io.Discard))
	rs, _ := f.Store.Load()
	wantPaths(t, rs, receipts.KindCreated, filepath.Join(f.tmp, "x.txt"))
}

func TestPostToolUseWriteNeverStoresContent(t *testing.T) {
	f := testDeps(t)
	fake := "AKIAFAKEFAKEFAKE1234"
	in := payload("PostToolUse", "s1", "/repo", "Write", `{"file_path":"`+f.tmp+`/k.json","content":"`+fake+`"}`)
	must(t, hook.Handle(ctx(), f.Deps, "PostToolUse", strings.NewReader(in), io.Discard))
	b, err := os.ReadFile(filepath.Join(f.Env.StateDir, "receipts.jsonl"))
	must(t, err)
	if len(b) == 0 || bytes.Contains(b, []byte(fake)) {
		t.Fatalf("receipts: %q", b)
	}
}

func TestSessionStartRecordsAgentPID(t *testing.T) {
	f := testDeps(t)
	start := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	f.snap.Procs = map[int]proc.Proc{
		100: {PID: 100, PPID: 50, Start: start, Comm: "/usr/bin/zsh"},
		50:  {PID: 50, PPID: 1, Start: start, Comm: "/usr/local/bin/claude"},
	}
	in := `{"session_id":"s1","cwd":"` + f.tmp + `","transcript_path":"/t/x.jsonl","source":"startup"}`
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(in), io.Discard))
	rs, _ := f.Store.Load()
	if len(rs) != 1 || rs[0].Kind != receipts.KindSessionStart || rs[0].Agent != "claude" || rs[0].PID != 50 ||
		!rs[0].PIDStart.Equal(start) || rs[0].TranscriptPath != "/t/x.jsonl" || rs[0].Cwd != f.tmp || rs[0].Source != "hook" {
		t.Fatalf("receipt = %+v", rs)
	}
	// No agent ancestor: PID stays zero.
	f.snap.Procs = map[int]proc.Proc{100: {PID: 100, PPID: 1, Start: start, Comm: "zsh"}}
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(strings.Replace(in, "s1", "s2", 1)), io.Discard))
	rs, _ = f.Store.Load()
	if rs[1].PID != 0 {
		t.Fatalf("PID = %d, want 0", rs[1].PID)
	}
}

func cacheBig(t *testing.T, f fixture) {
	t.Helper()
	r := scan.Result{GeneratedAt: f.Env.Now()}
	r.Items = append(r.Items, item.Item{ID: "big", Size: 2_000_000_000, Decision: item.Decision{Class: item.Offer}})
	must(t, report.SaveCache(f.Env.StateDir, r))
}

func TestSessionStartNudgeJSON(t *testing.T) {
	f := testDeps(t)
	cacheBig(t, f)
	in := payload("SessionStart", "s1", f.tmp, "", "")
	var out bytes.Buffer
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(in), &out))
	want := `{"systemMessage":"agent-receipts: agents left 2.0 GB on this machine. run 'agent-receipts report'"}` + "\n"
	if out.String() != want {
		t.Fatalf("out = %q\nwant %q", out.String(), want)
	}
	out.Reset()
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(in), &out))
	if out.Len() != 0 {
		t.Fatalf("second nudge: %q", out.String())
	}
}

func TestSessionStartNoCacheNoNudge(t *testing.T) {
	f := testDeps(t)
	var out bytes.Buffer
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(payload("SessionStart", "s1", f.tmp, "", "")), &out))
	if out.Len() != 0 {
		t.Fatalf("out = %q", out.String())
	}
}

func TestNudgeStateSavedWhenNotShown(t *testing.T) {
	f := testDeps(t)
	cacheBig(t, f)
	// A shown nudge a day+ ago, never followed: the hook must persist the judged-ignored count.
	shown := f.Env.Now().Add(-25 * time.Hour)
	must(t, nudge.Save(f.Env.StateDir, nudge.State{LastShown: shown, LastBytes: 2_000_000_000}))
	var out bytes.Buffer
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(payload("SessionStart", "s1", f.tmp, "", "")), &out))
	if out.Len() != 0 {
		t.Fatalf("unexpected nudge: %q", out.String())
	}
	st, _ := nudge.Load(f.Env.StateDir)
	if st.IgnoredStreak != 1 || !st.Judged.Equal(shown) {
		t.Fatalf("state not saved: %+v", st)
	}
}

func TestSessionEndWeakAttributionAndRefresh(t *testing.T) {
	f := testDeps(t)
	start := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	// another live session recorded earlier
	must(t, f.Store.Append(receipts.Receipt{Kind: receipts.KindSessionStart, SessionID: "other", PID: 7, PIDStart: start}))
	f.snap.Procs = map[int]proc.Proc{7: {PID: 7, Start: start}}
	must(t, os.WriteFile(filepath.Join(f.tmp, "old.bin"), []byte("x"), 0o644))
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(payload("SessionStart", "s1", "/repo", "", "")), io.Discard))
	must(t, os.WriteFile(filepath.Join(f.tmp, "new.bin"), []byte("x"), 0o644))
	must(t, os.WriteFile(filepath.Join(f.tmp, "strong.bin"), []byte("x"), 0o644))
	must(t, f.Store.Append(receipts.Receipt{Kind: receipts.KindCreated, SessionID: "s1", Path: filepath.Join(f.tmp, "strong.bin")}))
	must(t, hook.Handle(ctx(), f.Deps, "SessionEnd", strings.NewReader(payload("SessionEnd", "s1", "/repo", "", "")), io.Discard))
	rs, _ := f.Store.Load()
	wantPaths(t, rs, receipts.KindWeakCreated, filepath.Join(f.tmp, "new.bin"))
	for _, r := range rs {
		if r.Kind == receipts.KindWeakCreated {
			if strings.Join(r.Sessions, ",") != "s1,other" && strings.Join(r.Sessions, ",") != "other,s1" {
				t.Errorf("sessions = %v", r.Sessions)
			}
		}
	}
	if *f.spawns != 1 {
		t.Errorf("spawns = %d", *f.spawns)
	}
	if es, _ := os.ReadDir(filepath.Join(f.Env.StateDir, "snapshots")); len(es) != 0 {
		t.Errorf("snapshot not deleted: %v", es)
	}
}

func TestSessionEndSkipsRefreshWhenLocked(t *testing.T) {
	f := testDeps(t)
	must(t, os.MkdirAll(f.Env.StateDir, 0o700))
	lf, err := os.OpenFile(filepath.Join(f.Env.StateDir, "refresh.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	must(t, err)
	defer lf.Close()
	must(t, flockEx(lf))
	must(t, hook.Handle(ctx(), f.Deps, "SessionEnd", strings.NewReader(payload("SessionEnd", "s1", "/repo", "", "")), io.Discard))
	if *f.spawns != 0 {
		t.Errorf("spawns = %d", *f.spawns)
	}
}

func TestSubagentStopIsNoOp(t *testing.T) {
	f := testDeps(t)
	var out bytes.Buffer
	must(t, hook.Handle(ctx(), f.Deps, "SubagentStop", strings.NewReader(payload("SubagentStop", "s1", "/repo", "", "")), &out))
	if _, err := os.Stat(f.Env.StateDir); !os.IsNotExist(err) || out.Len() != 0 || *f.spawns != 0 {
		t.Fatalf("SubagentStop wrote something (stat err %v, out %q)", err, out.String())
	}
}

func TestBadInputIsSilent(t *testing.T) {
	f := testDeps(t)
	var out bytes.Buffer
	for _, ev := range []string{"SessionStart", "PostToolUse", "SessionEnd", "Bogus"} {
		must(t, hook.Handle(ctx(), f.Deps, ev, strings.NewReader("not json"), &out))
	}
	must(t, hook.Handle(ctx(), f.Deps, "Bogus", strings.NewReader(payload("Bogus", "s1", "/", "", "")), &out))
	if _, err := os.Stat(f.Env.StateDir); !os.IsNotExist(err) || out.Len() != 0 {
		t.Fatal("bad input wrote something")
	}
}

func TestEmptyOrRelativeStateDirDoesNothing(t *testing.T) {
	f := testDeps(t)
	f.Env.StateDir = "relative/state"
	cwd, _ := os.Getwd()
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(payload("SessionStart", "s1", "/repo", "", "")), io.Discard))
	if _, err := os.Stat(filepath.Join(cwd, "relative")); !os.IsNotExist(err) {
		t.Fatal("created relative dir")
	}
	f.Env.Home = ""
	f.Env.StateDir = ""
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(payload("SessionStart", "s1", "/repo", "", "")), io.Discard))
}

func TestCraftedSessionIDStaysInsideSnapshots(t *testing.T) {
	f := testDeps(t)
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(payload("SessionStart", "../../x", f.tmp, "", "")), io.Discard))
	es, err := os.ReadDir(filepath.Join(f.Env.StateDir, "snapshots"))
	must(t, err)
	if len(es) != 1 || strings.ContainsAny(es[0].Name(), "/\\") || strings.HasPrefix(es[0].Name(), ".") {
		t.Fatalf("snapshots = %v", es)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.Env.StateDir), "x.json")); !os.IsNotExist(err) {
		t.Fatal("escaped snapshots dir")
	}
}

func TestHookWithinBudget(t *testing.T) {
	f := testDeps(t)
	f.Procs = proc.TakeProcsOnly // real ps
	f.ParentPID = os.Getppid()
	start := time.Now()
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(payload("SessionStart", "s1", f.tmp, "", "")), io.Discard))
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("SessionStart took %v", d)
	}
}

func TestBinaryBudgetAndSilence(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root := t.TempDir()
	bin := filepath.Join(root, "agent-receipts")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/agent-receipts").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	var best time.Duration
	for i := 0; i < 5; i++ {
		cmd := exec.Command(bin, "hook", "SessionStart")
		cmd.Env = append(os.Environ(), "HOME="+root, "XDG_STATE_HOME="+filepath.Join(root, "state"))
		cmd.Stdin = strings.NewReader(payload("SessionStart", "s1", root, "", ""))
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		t0 := time.Now()
		err := cmd.Run()
		d := time.Since(t0)
		if err != nil || se.Len() != 0 {
			t.Fatalf("err %v stderr %q", err, se.String())
		}
		if best == 0 || d < best {
			best = d
		}
	}
	t.Logf("process wall time SessionStart (best of 5): %v", best)
	if best > 500*time.Millisecond {
		t.Fatalf("too slow: %v", best)
	}
}

func TestCompactKeepsSnapshotBaseline(t *testing.T) {
	f := testDeps(t)
	run := func(ev, src string) {
		in := `{"session_id":"s1","cwd":"/repo","source":"` + src + `"}`
		must(t, hook.Handle(ctx(), f.Deps, ev, strings.NewReader(in), io.Discard))
	}
	run("SessionStart", "startup")
	must(t, os.WriteFile(filepath.Join(f.tmp, "before-compact.bin"), []byte("x"), 0o644))
	run("SessionStart", "compact")
	run("SessionEnd", "")
	rs, _ := f.Store.Load()
	wantPaths(t, rs, receipts.KindWeakCreated, filepath.Join(f.tmp, "before-compact.bin"))
}

func TestSessionEndProcsFailureSkipsWeak(t *testing.T) {
	f := testDeps(t)
	must(t, hook.Handle(ctx(), f.Deps, "SessionStart", strings.NewReader(payload("SessionStart", "s1", "/repo", "", "")), io.Discard))
	must(t, os.WriteFile(filepath.Join(f.tmp, "n.bin"), []byte("x"), 0o644))
	f.Deps.Procs = func(context.Context) (*proc.Snapshot, error) { return nil, os.ErrInvalid }
	must(t, hook.Handle(ctx(), f.Deps, "SessionEnd", strings.NewReader(payload("SessionEnd", "s1", "/repo", "", "")), io.Discard))
	rs, _ := f.Store.Load()
	wantPaths(t, rs, receipts.KindWeakCreated)
}

func TestPruneSnapshots(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "snapshots")
	must(t, os.MkdirAll(dir, 0o700))
	now := time.Now()
	for name, age := range map[string]time.Duration{"old.json": 8 * 24 * time.Hour, "new.json": time.Hour} {
		p := filepath.Join(dir, name)
		must(t, os.WriteFile(p, nil, 0o600))
		must(t, os.Chtimes(p, now.Add(-age), now.Add(-age)))
	}
	hook.PruneSnapshots(state, 7*24*time.Hour, now)
	if _, err := os.Stat(filepath.Join(dir, "old.json")); !os.IsNotExist(err) {
		t.Error("old snapshot kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "new.json")); err != nil {
		t.Error("new snapshot removed")
	}
}

// R30: a raw path with a ".." element is never recorded.
func TestPostToolUseDropsDotDotPaths(t *testing.T) {
	f := testDeps(t)
	for _, c := range []struct{ cwd, tool, input string }{
		{"/repo", "Write", `{"file_path":"` + f.tmp + `/a/../b.txt"}`},
		{"/repo", "Bash", `{"command":"mkdir -p ` + f.tmp + `/x/../y"}`},
		{f.tmp + "/sub/..", "Write", `{"file_path":"c.txt"}`},
		{f.tmp + "/sub/..", "Bash", `{"command":"mkdir d"}`},
		{"/repo", "Write", `{"file_path":"` + f.tmp + `/ok.txt"}`},
	} {
		must(t, hook.Handle(ctx(), f.Deps, "PostToolUse", strings.NewReader(payload("PostToolUse", "s1", c.cwd, c.tool, c.input)), io.Discard))
	}
	rs, _ := f.Store.Load()
	wantPaths(t, rs, receipts.KindCreated, filepath.Join(f.tmp, "ok.txt"))
}
