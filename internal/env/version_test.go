package env

import (
	"runtime/debug"
	"testing"
)

func TestVersionFrom(t *testing.T) {
	withMain := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	cases := []struct {
		name   string
		ldflag string
		bi     *debug.BuildInfo
		ok     bool
		want   string
	}{
		{"ldflags win", "v1.2.3", withMain("v0.1.0"), true, "v1.2.3"},
		{"go install tag", "dev", withMain("v0.1.0"), true, "v0.1.0"},
		{"local build", "dev", withMain("(devel)"), true, "dev"},
		{"pseudo version", "dev", withMain("v0.0.0-20261120110155-c7a3a220e63e"), true, "v0.0.0-20261120110155-c7a3a220e63e"},
		{"no build info", "dev", nil, false, "dev"},
	}
	for _, c := range cases {
		if got := versionFrom(c.ldflag, c.bi, c.ok); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
