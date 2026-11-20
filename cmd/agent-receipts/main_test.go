package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersionAndUnknown(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "agent-receipts") {
		t.Fatal(code, out.String())
	}
	errb.Reset()
	if code := run([]string{"bogus"}, &out, &errb); code != 2 {
		t.Fatal(code)
	}
	for _, s := range []string{"report", "clean", "stats", "hook"} {
		if !strings.Contains(errb.String(), s) {
			t.Fatalf("usage missing %s: %q", s, errb.String())
		}
	}
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatal(code)
	}
}

func TestStatsNoFileIsZeros(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := run([]string{"stats"}, &out, &errb); code != 0 {
		t.Fatal(code, errb.String())
	}
	for _, want := range []string{"reports run:         0", "keep rate:           n/a", "first run:           never"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := run([]string{"stats", "--share"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "keep rate n/a") {
		t.Fatal(code, out.String())
	}
}
