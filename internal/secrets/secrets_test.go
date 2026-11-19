package secrets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Audarya07/agent-receipts/internal/secrets"
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
