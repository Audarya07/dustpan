package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Audarya07/dustpan/internal/refreshlock"
)

func TestRefreshSkipsWhenLockHeld(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "dustpan")
	release, ok := refreshlock.TryAcquire(dir)
	if !ok {
		t.Fatal("could not take lock")
	}
	var stderr bytes.Buffer
	if code := runRefresh(&stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("code %d stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "last-report.json")); !os.IsNotExist(err) {
		t.Fatal("cache written while lock held")
	}
	release()
	if !func() bool { r, ok := refreshlock.TryAcquire(dir); r(); return ok }() {
		t.Fatal("lock not free after release")
	}
}
