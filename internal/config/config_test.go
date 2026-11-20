package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Audarya07/dustpan/internal/config"
)

func TestConfigDefaultsAndBadFile(t *testing.T) {
	dir := t.TempDir()
	c, err := config.Load(dir)
	if err != nil || c.StaleJobDays != 14 || len(c.Roots) != 0 {
		t.Fatalf("defaults: %+v, %v", c, err)
	}

	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("roots = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(dir); err == nil || !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("bad file error = %v", err)
	}

	good := "roots = [\"/a\", \"/b\"]\nstale_job_days = 7\nextra_never_touch = [\"/keep\"]\n"
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = config.Load(dir)
	if err != nil || c.StaleJobDays != 7 || len(c.Roots) != 2 || c.ExtraNeverTouch[0] != "/keep" {
		t.Fatalf("parsed: %+v, %v", c, err)
	}

	if err := os.WriteFile(path, []byte("roots = [\"/a\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _ = config.Load(dir); c.StaleJobDays != 14 {
		t.Fatalf("omitted stale_job_days = %d, want 14", c.StaleJobDays)
	}
}
