// Package config loads the optional user config file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// FileName is the config file inside the config directory.
const FileName = "config.toml"

// Config holds user settings. Every field is optional.
type Config struct {
	Roots           []string `toml:"roots"`
	StaleJobDays    int      `toml:"stale_job_days"`
	ExtraNeverTouch []string `toml:"extra_never_touch"`
}

// Defaults returns the configuration used when no file exists.
func Defaults() Config { return Config{StaleJobDays: 14} }

// Load reads dir/config.toml. A missing file yields defaults; a malformed
// file yields an error naming it.
func Load(dir string) (Config, error) {
	cfg := Defaults()
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Defaults(), fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Defaults(), fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.StaleJobDays <= 0 {
		cfg.StaleJobDays = 14
	}
	return cfg, nil
}
