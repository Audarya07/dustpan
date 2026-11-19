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
