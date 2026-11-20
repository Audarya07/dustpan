package secrets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Audarya07/dustpan/internal/secrets"
)

// fakeAWS is an invented value, not a real key.
const fakeAWS = "AKIAZ7Q2XJ4K3M5N6Q2X"

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newScanner(t *testing.T) *secrets.Scanner {
	t.Helper()
	sc, err := secrets.New()
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestMask(t *testing.T) {
	for in, want := range map[string]string{
		fakeAWS:       "AKIA…6Q2X",
		"abcdefg":     "ab…",
		"abcdef":      "ab…",
		"abcdefghijk": "ab…",
		"abc":         "…",
		"":            "…",
	} {
		if got := secrets.Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScanFindsAndNeverLeaksRaw(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "acct_prod.json", `{"aws_access_key_id":"`+fakeAWS+`"}`)
	fs, partial, err := newScanner(t).Scan(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if partial || len(fs) == 0 {
		t.Fatalf("want finding, got %v partial=%v", fs, partial)
	}
	if fs[0].Path != "acct_prod.json" || fs[0].Line != 1 || fs[0].Masked == "" {
		t.Errorf("unexpected finding %+v", fs[0])
	}
	b, _ := json.Marshal(fs)
	if bytes.Contains(b, []byte(fakeAWS)) {
		t.Fatal("raw secret leaked")
	}
}

func TestScanDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "k.json", `{"aws_access_key_id":"`+fakeAWS+`"}`)
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "k.json"), filepath.Join(dir, "filelink.json")); err != nil {
		t.Fatal(err)
	}
	fs, _, err := newScanner(t).Scan(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Fatalf("followed symlink: %v", fs)
	}
}

func TestScanStopsAtMaxFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		write(t, dir, fmt.Sprint(i), "x")
	}
	sc := newScanner(t)
	sc.MaxFiles = 5
	_, partial, err := sc.Scan(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !partial {
		t.Fatal("want partial")
	}
}

func TestScanSkipsBinaryLargeGitAndSkipFunc(t *testing.T) {
	body := `{"aws_access_key_id":"` + fakeAWS + `"}`
	dir := t.TempDir()
	write(t, dir, "bin.json", "\x00"+body)
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, ".git"), "c.json", body)
	write(t, dir, "skipme.json", body)
	skip := func(rel string, _ os.DirEntry) bool { return rel == "skipme.json" }
	fs, partial, err := newScanner(t).Scan(context.Background(), dir, skip)
	if err != nil || partial || len(fs) != 0 {
		t.Fatalf("want nothing, got %v partial=%v err=%v", fs, partial, err)
	}

	big := t.TempDir()
	write(t, big, "big.json", body)
	sc := newScanner(t)
	sc.MaxFileBytes = 10
	fs, _, err = sc.Scan(context.Background(), big, nil)
	if err != nil || len(fs) != 0 {
		t.Fatalf("large file scanned: %v %v", fs, err)
	}
}

func TestScanHonorsCancelledContext(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := newScanner(t).Scan(ctx, dir, nil); err == nil {
		t.Fatal("want ctx error")
	}
}

func TestScanSingleFileRoot(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "notes.txt", `{"aws_access_key_id":"`+fakeAWS+`"}`)
	fs, partial, err := newScanner(t).Scan(context.Background(), filepath.Join(dir, "notes.txt"), nil)
	if err != nil || partial {
		t.Fatalf("err=%v partial=%v", err, partial)
	}
	if len(fs) != 1 || fs[0].Path != "" || fs[0].Line != 1 {
		t.Fatalf("want 1 finding with empty Path (the root itself), got %+v", fs)
	}
}

func TestScanSymlinkRootIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "k.json", `{"aws_access_key_id":"`+fakeAWS+`"}`)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "k.json"), link); err != nil {
		t.Fatal(err)
	}
	fs, partial, err := newScanner(t).Scan(context.Background(), link, nil)
	if err != nil || partial || len(fs) != 0 {
		t.Fatalf("followed symlink root: %v partial=%v err=%v", fs, partial, err)
	}
}

func TestScanUnreadableIsPartial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	dir := t.TempDir()
	write(t, dir, "f.json", `{"aws_access_key_id":"`+fakeAWS+`"}`)
	f := filepath.Join(dir, "f.json")
	if err := os.Chmod(f, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f, 0o600) })
	if _, partial, _ := newScanner(t).Scan(context.Background(), f, nil); !partial {
		t.Error("unreadable file root: want partial")
	}
	if _, partial, _ := newScanner(t).Scan(context.Background(), dir, nil); !partial {
		t.Error("unreadable file inside dir: want partial")
	}
	sub := filepath.Join(t.TempDir(), "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, sub, "x", "x")
	if err := os.Chmod(sub, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
	if _, partial, _ := newScanner(t).Scan(context.Background(), sub, nil); !partial {
		t.Error("unreadable dir root: want partial")
	}
	if _, partial, _ := newScanner(t).Scan(context.Background(), filepath.Join(dir, "missing"), nil); !partial {
		t.Error("missing root: want partial (couldn't look)")
	}
}
