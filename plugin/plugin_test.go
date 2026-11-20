package plugin_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func mustDecode(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestHooksJSON(t *testing.T) {
	var h struct {
		Hooks map[string][]struct {
			Matcher string
			Hooks   []struct {
				Type, Command string
				Timeout       int
			}
		}
	}
	mustDecode(t, "hooks/hooks.json", &h)
	for _, ev := range []string{"SessionStart", "PostToolUse", "SessionEnd"} {
		if len(h.Hooks[ev]) == 0 {
			t.Errorf("missing %s", ev)
		}
	}
	for _, bad := range []string{"WorktreeCreate", "WorktreeRemove", "SubagentStop"} {
		if _, ok := h.Hooks[bad]; ok {
			t.Errorf("%s must not be used", bad)
		}
	}
	if m := h.Hooks["SessionStart"][0].Matcher; m != "startup|resume|clear|compact" {
		t.Errorf("SessionStart matcher %q", m)
	}
	if m := h.Hooks["PostToolUse"][0].Matcher; m != "Write|NotebookEdit|Bash" {
		t.Errorf("PostToolUse matcher %q", m)
	}
	for ev, hs := range h.Hooks {
		for _, x := range hs[0].Hooks {
			want := "command -v dustpan >/dev/null 2>&1 && dustpan hook " + ev + " || true"
			if x.Type != "command" || x.Command != want || x.Timeout != 1 {
				t.Errorf("%s: %+v", ev, x)
			}
		}
	}
}

func TestManifests(t *testing.T) {
	var p struct {
		Name, Description, Version, License string
		Author                              struct{ Name string }
	}
	mustDecode(t, ".claude-plugin/plugin.json", &p)
	if p.Name != "dustpan" || p.Version != "0.1.0" || p.License != "MIT" || p.Author.Name != "Audarya Uttarwar" || p.Description == "" {
		t.Errorf("plugin.json: %+v", p)
	}
	var m struct {
		Plugins []struct{ Name, Source string }
	}
	mustDecode(t, ".claude-plugin/marketplace.json", &m)
	if len(m.Plugins) != 1 || m.Plugins[0].Name != "dustpan" || m.Plugins[0].Source != "./" {
		t.Errorf("marketplace.json: %+v", m)
	}
}

func TestLeftoversCommandForbidsDeletion(t *testing.T) {
	b, err := os.ReadFile("commands/leftovers.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"description: Show what coding agents left behind on this machine",
		"dustpan report --details",
		"never delete, move, or edit",
		"never run `dustpan clean`",
		"dustpan clean",
		"own terminal",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("leftovers.md missing %q", want)
		}
	}
}
