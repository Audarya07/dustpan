package scan_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Audarya07/dustpan/internal/cmdparse"
	"github.com/Audarya07/dustpan/internal/item"
	"github.com/Audarya07/dustpan/internal/receipts"
	"github.com/Audarya07/dustpan/internal/report"
	"github.com/Audarya07/dustpan/internal/scan"
	"github.com/Audarya07/dustpan/internal/secrets"
)

func (h *fixture) mkdir(p string) string {
	h.t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func created(path string, isDir bool, session string) receipts.Receipt {
	return receipts.Receipt{Kind: receipts.KindCreated, Path: path, IsDir: isDir, SessionID: session,
		Source: "hook", At: time.Now()}
}

func TestScratchCollectorOnlyReceipted(t *testing.T) {
	h := newFixtureHome(t)
	h.write(h.tmp, "my-bin", "x")
	h.write(h.tmp, "notes.txt", "x")
	snap := h.mkdir(h.tmpPath("imgs-snap"))
	h.write(snap, "a.png", "x")
	ix := h.index(
		created(h.tmpPath("my-bin"), false, "s1"),
		created(snap, true, "s1"),
		created(filepath.Join(snap, "a.png"), false, "s1"),
		created(h.tmpPath("gone"), false, "s1"), // never made it to disk
	)
	res := mustRun(t, h.context(ix))
	gs := wantItem(t, res, h.tmpPath("my-bin"), item.Offer)
	if gs.Evidence.Source != "receipt" || gs.Evidence.SessionID != "s1" || !gs.Evidence.Strong {
		t.Fatalf("evidence: %+v", gs.Evidence)
	}
	wantItem(t, res, snap, item.Offer)
	wantNoItem(t, res, filepath.Join(snap, "a.png"))
	wantNoItem(t, res, h.tmpPath("notes.txt"))
	wantNoItem(t, res, h.tmpPath("gone"))
	if n := len(res.Items); n != 2 {
		t.Fatalf("items = %d, want 2: %+v", n, res.Items)
	}
}

func TestScratchNeverTouchReceiptsDoNotAbsorbRealScratch(t *testing.T) {
	h := newFixtureHome(t)
	h.write(h.tmp, "my-bin", "x")
	rt := h.mkdir(h.tmpPath("claude-501"))
	h.write(rt, "x", "x")
	ix := h.index(
		created(h.tmp, true, "s1"), // mkdir -p /tmp
		created(rt, true, "s1"),
		created(filepath.Join(rt, "x"), false, "s1"),
		created(h.tmpPath("my-bin"), false, "s1"),
	)
	res := mustRun(t, h.context(ix))
	wantItem(t, res, h.tmpPath("my-bin"), item.Offer)
	for _, p := range []string{h.tmp, rt, filepath.Join(rt, "x")} {
		wantNoItem(t, res, p)
	}
}

func TestScratchReceiptOlderThanPathIsNotOfferable(t *testing.T) {
	h := newFixtureHome(t)
	old := h.tmpPath("old-file")
	h.write(h.tmp, "old-file", "x")
	at := time.Now()
	if err := os.Chtimes(old, at.Add(-time.Hour), at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := created(old, false, "s1")
	r.At = at
	res := mustRun(t, h.context(h.index(r)))
	it := wantItem(t, res, old, item.LeftAlone)
	if it.Evidence.Strong || len(it.Evidence.Weak) == 0 || !it.Decision.DetailsOnly {
		t.Fatalf("want weak details-only, got %+v %+v", it.Evidence, it.Decision)
	}
	// Within the 2 minute slack it stays strong. A fresh file: on macOS
	// moving mtime back also moves the birth time back, and never forward.
	slack := h.tmpPath("slack-file")
	h.write(h.tmp, "slack-file", "x")
	if err := os.Chtimes(slack, at.Add(-time.Minute), at.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	r2 := created(slack, false, "s1")
	r2.At = at
	wantItem(t, mustRun(t, h.context(h.index(r2))), slack, item.Offer)
}

func TestScratchLiveSessionKeepsItsFiles(t *testing.T) {
	h := newFixtureHome(t)
	h.write(h.tmp, "f", "x")
	h.job("j", `{"state":"working","sessionId":"live","updatedAt":"`+h.now.Add(-time.Hour).Format(time.RFC3339)+`"}`)
	res := mustRun(t, h.context(h.index(created(h.tmpPath("f"), false, "live"))))
	wantItem(t, res, h.tmpPath("f"), item.LeftAlone)
}

func TestScratchSymlinkSwapNeverListsTarget(t *testing.T) {
	h := newFixtureHome(t)
	target := h.mkdir(filepath.Join(h.home, "proj"))
	h.write(target, "precious.txt", "x")
	link := h.tmpPath("foo")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	res := mustRun(t, h.context(h.index(created(link, true, "s1"))))
	wantNoItem(t, res, target)
	wantNoItem(t, res, filepath.Join(target, "precious.txt"))
	it, ok := find(res, link)
	if !ok || it.Size != 0 {
		t.Fatalf("want the link itself listed with size 0, got %+v (found=%v)", it, ok)
	}
}

func TestScratchMergedLiveSessionKeepsDir(t *testing.T) {
	h := newFixtureHome(t)
	dir := h.mkdir(h.tmpPath("work"))
	h.write(dir, "f", "x")
	h.job("j", `{"state":"working","sessionId":"live","updatedAt":"`+h.now.Add(-time.Hour).Format(time.RFC3339)+`"}`)
	ix := h.index(created(dir, true, "dead"), created(filepath.Join(dir, "f"), false, "live"))
	res := mustRun(t, h.context(ix))
	it := wantItem(t, res, dir, item.LeftAlone)
	if len(it.Evidence.Sessions) != 1 || it.Evidence.Sessions[0] != "live" {
		t.Fatalf("sessions: %+v", it.Evidence)
	}
}

func TestRecheckLinkItemNeverReturnsItsTarget(t *testing.T) {
	h := newFixtureHome(t)
	foo, bar := h.tmpPath("foo"), h.tmpPath("bar")
	h.write(h.tmp, "foo", "x")
	h.write(h.tmp, "bar", "x")
	ix := h.index(created(foo, false, "s1"), created(bar, false, "s1"))
	c := h.context(ix)
	it := wantItem(t, mustRun(t, c), foo, item.Offer)
	if err := os.Remove(foo); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bar, foo); err != nil {
		t.Fatal(err)
	}
	fresh, err := scan.Recheck(context.Background(), c, it)
	if err == nil && fresh.Path != foo {
		t.Fatalf("recheck(%s) returned %s", foo, fresh.Path)
	}
	if err := os.Remove(foo); err != nil {
		t.Fatal(err)
	}
	if fresh, err := scan.Recheck(context.Background(), c, it); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gone: got %+v, %v", fresh, err)
	}
}

func TestScratchFileItemIsSecretScanned(t *testing.T) {
	h := newFixtureHome(t)
	h.write(h.tmp, "acct.json", `{"aws_access_key_id":"AKIAZ7Q2XJ4K3M5N6Q2X"}`)
	c := h.context(h.index(created(h.tmpPath("acct.json"), false, "s1")))
	sc, err := secrets.New()
	if err != nil {
		t.Fatal(err)
	}
	c.Secrets = sc
	it := wantItem(t, mustRun(t, c), h.tmpPath("acct.json"), item.Offer)
	if len(it.Secrets) != 1 || it.Secrets[0].Path != "" || it.SecretsPartial {
		t.Fatalf("want 1 finding on the file itself, got %+v partial=%v", it.Secrets, it.SecretsPartial)
	}
}

// fromCommand turns a Bash command into receipts the way the hook does.
func fromCommand(cmd, cwd, session string, at time.Time) []receipts.Receipt {
	var rs []receipts.Receipt
	for _, o := range cmdparse.OutputPaths(cmd, cwd) {
		rs = append(rs, receipts.Receipt{Kind: receipts.KindCreated, Path: o.Path, IsDir: o.IsDir,
			Via: o.Via, SessionID: session, Source: "hook", At: at})
	}
	return rs
}

// backdate makes path look created at when. On macOS moving the mtime back
// moves the birth time back too; elsewhere the birth time is faked.
func backdate(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		if bt, ok := scan.RealBirthTime(path); !ok || bt.After(when.Add(time.Second)) {
			t.Fatalf("darwin birth time not backdated: %v %v", bt, ok)
		}
		return
	}
	t.Cleanup(scan.SetBirthTime(func(p string) (time.Time, bool) {
		if p == path {
			return when, true
		}
		return time.Time{}, false
	}))
}

// R26: copying into a week-old user folder bumps its mtime, but the folder
// is not the agent's.
func TestScratchCpIntoOldUserDirNotOffered(t *testing.T) {
	h := newFixtureHome(t)
	userdir := h.mkdir(h.tmpPath("userdir"))
	h.write(userdir, "mine.txt", "x")
	backdate(t, userdir, time.Now().Add(-7*24*time.Hour))
	h.write(userdir, "a.txt", "x") // the agent's cp lands; userdir mtime is now
	rs := fromCommand("cp a.txt "+userdir, h.repo, "s1", time.Now())
	if len(rs) != 1 {
		t.Fatalf("receipts: %+v", rs)
	}
	res := mustRun(t, h.context(h.index(rs...)))
	if it, ok := find(res, userdir); ok && (it.Decision.Class == item.Offer || !it.Decision.DetailsOnly) {
		t.Fatalf("old user dir offered or shown: %+v", it)
	}
}

// R26: appending to an existing user file creates nothing.
func TestScratchAppendToOldFileNotOffered(t *testing.T) {
	h := newFixtureHome(t)
	log := h.tmpPath("app.log")
	h.write(h.tmp, "app.log", "old\n")
	backdate(t, log, time.Now().Add(-7*24*time.Hour))
	h.write(h.tmp, "app.log", "old\nhi\n") // the append
	cmds := []string{"echo hi >> " + log, "echo hi | tee -a " + log}
	for _, cmd := range cmds {
		rs := fromCommand(cmd, h.repo, "s1", time.Now())
		if len(rs) != 0 {
			t.Fatalf("%q recorded %+v", cmd, rs)
		}
		wantNoItem(t, mustRun(t, h.context(h.index(rs...))), log)
	}
}

func TestScratchGenuineNewFileStillOffered(t *testing.T) {
	h := newFixtureHome(t)
	bin := h.tmpPath("my-bin")
	h.write(h.tmp, "my-bin", "x")
	rs := fromCommand("go build -o "+bin+" ./cmd/x", h.repo, "s1", time.Now())
	wantItem(t, mustRun(t, h.context(h.index(rs...))), bin, item.Offer)
	dir := h.mkdir(h.tmpPath("snap"))
	rs = fromCommand("mkdir -p "+dir, h.repo, "s1", time.Now())
	wantItem(t, mustRun(t, h.context(h.index(rs...))), dir, item.Offer)
}

// R26: with no birth time, a directory is strong only when the receipt says
// it was made by mkdir or git worktree add; a file keeps the mtime rule.
func TestScratchWithoutBirthTime(t *testing.T) {
	h := newFixtureHome(t)
	t.Cleanup(scan.SetBirthTime(func(string) (time.Time, bool) { return time.Time{}, false }))
	made := h.mkdir(h.tmpPath("made"))
	copied := h.mkdir(h.tmpPath("copied"))
	h.write(copied, "f", "x")
	file := h.tmpPath("copied.txt")
	h.write(h.tmp, "copied.txt", "x")
	now := time.Now()
	var rs []receipts.Receipt
	rs = append(rs, fromCommand("mkdir "+made, h.repo, "s1", now)...)
	rs = append(rs, fromCommand("cp -r src "+copied, h.repo, "s1", now)...)
	rs = append(rs, fromCommand("cp a "+file, h.repo, "s1", now)...)
	res := mustRun(t, h.context(h.index(rs...)))
	wantItem(t, res, made, item.Offer)
	wantItem(t, res, file, item.Offer)
	if it := wantItem(t, res, copied, item.LeftAlone); !it.Decision.DetailsOnly || it.Evidence.Strong {
		t.Fatalf("cp'd dir without birth time: %+v", it)
	}
}

// R29: snapshot (weak) attribution for /tmp is listed in --details only.
func TestScratchWeakReceiptIsDetailsOnly(t *testing.T) {
	h := newFixtureHome(t)
	h.write(h.tmp, "maybe.json", `{"aws_access_key_id":"AKIAZ7Q2XJ4K3M5N6Q2X"}`)
	h.write(h.tmp, "strong.bin", "x")
	weak := func(p string) receipts.Receipt {
		return receipts.Receipt{Kind: receipts.KindWeakCreated, Path: p, SessionID: "s1",
			Sessions: []string{"s1", "s2"}, Source: "snapshot", At: time.Now()}
	}
	ix := h.index(
		weak(h.tmpPath("maybe.json")),
		weak(h.tmpPath("strong.bin")), created(h.tmpPath("strong.bin"), false, "s1"),
		weak(h.tmpPath("gone")),
		weak(h.tmp),
	)
	c := h.context(ix)
	sc, err := secrets.New()
	if err != nil {
		t.Fatal(err)
	}
	c.Secrets = sc
	res := mustRun(t, c)
	it := wantItem(t, res, h.tmpPath("maybe.json"), item.LeftAlone)
	if !it.Decision.DetailsOnly || it.Evidence.Strong || it.Evidence.Source != "snapshot" ||
		len(it.Evidence.Weak) != 2 || len(it.Secrets) != 1 {
		t.Fatalf("weak item: %+v", it)
	}
	if s := wantItem(t, res, h.tmpPath("strong.bin"), item.Offer); !s.Evidence.Strong {
		t.Fatalf("strong item downgraded: %+v", s.Evidence)
	}
	wantNoItem(t, res, h.tmpPath("gone"))
	wantNoItem(t, res, h.tmp)
	if n := len(res.Items); n != 2 {
		t.Fatalf("items = %d: %+v", n, res.Items)
	}
	var buf bytes.Buffer
	report.RenderCompact(&buf, res, false)
	if strings.Contains(buf.String(), "maybe.json") || strings.Contains(buf.String(), "AKIA") {
		t.Fatalf("weak item in compact report:\n%s", buf.String())
	}
	buf.Reset()
	report.RenderDetails(&buf, res, false)
	if !strings.Contains(buf.String(), "maybe.json") || !strings.Contains(buf.String(), "(details only)") {
		t.Fatalf("weak item missing from details:\n%s", buf.String())
	}
}

// R30: a receipt without a time can't prove the path is new.
func TestScratchZeroTimeReceiptIsWeak(t *testing.T) {
	h := newFixtureHome(t)
	h.write(h.tmp, "f", "x")
	r := created(h.tmpPath("f"), false, "s1")
	r.At = time.Time{}
	it := wantItem(t, mustRun(t, h.context(h.index(r))), h.tmpPath("f"), item.LeftAlone)
	if it.Evidence.Strong || !it.Decision.DetailsOnly {
		t.Fatalf("zero-time receipt: %+v", it)
	}
}
