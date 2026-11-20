package scan_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/receipts"
	"github.com/Audarya07/agent-receipts/internal/scan"
	"github.com/Audarya07/agent-receipts/internal/secrets"
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
	// Within the 2 minute slack it stays strong.
	if err := os.Chtimes(old, at.Add(-time.Minute), at.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantItem(t, mustRun(t, h.context(h.index(r))), old, item.Offer)
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
