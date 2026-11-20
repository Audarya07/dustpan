package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Audarya07/agent-receipts/internal/env"
)

func TestCheckEnv(t *testing.T) {
	good := env.Env{Home: "/Users/u", TmpDirs: []string{"/private/tmp"}}
	if err := checkEnv(good); err != nil {
		t.Fatal(err)
	}
	for _, e := range []env.Env{
		{Home: "", TmpDirs: []string{"/private/tmp"}},
		{Home: "relative/home", TmpDirs: []string{"/private/tmp"}},
		{Home: "/Users/u"},
	} {
		if checkEnv(e) == nil {
			t.Errorf("%+v: want refusal", e)
		}
	}
}

// R30: report and clean refuse (exit 2, one line) without an absolute home.
func TestReportAndCleanRefuseWithoutHome(t *testing.T) {
	// Safety net: even a broken guard could only write under temp dirs.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", "")
	for name, cmd := range map[string]func([]string, *bytes.Buffer, *bytes.Buffer) int{
		"report": func(a []string, o, e *bytes.Buffer) int { return runReport(a, o, e) },
		"clean":  func(a []string, o, e *bytes.Buffer) int { return runClean(a, o, e) },
	} {
		var out, errb bytes.Buffer
		if code := cmd(nil, &out, &errb); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
		msg := errb.String()
		if strings.Count(msg, "\n") != 1 || !strings.Contains(msg, "home folder") || out.Len() != 0 {
			t.Errorf("%s: stdout %q stderr %q", name, out.String(), msg)
		}
	}
}

// R32: the detached reconstruct started by report runs at low priority.
func TestReconstructLowersPriority(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root) // no transcripts: nothing real is read
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	calls := 0
	old := lowerPriority
	lowerPriority = func() { calls++ }
	t.Cleanup(func() { lowerPriority = old })
	var out, errb bytes.Buffer
	if code := run([]string{"reconstruct"}, &out, &errb); code != 0 || calls != 1 {
		t.Fatalf("exit %d, lowerPriority calls %d, stderr %q", code, calls, errb.String())
	}
}
