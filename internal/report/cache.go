package report

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/Audarya07/dustpan/internal/scan"
)

const cacheName = "last-report.json"

// SaveCache writes the result atomically (temp file + rename, 0600) so the
// hooks reading it never see a half-written file.
func SaveCache(stateDir string, r scan.Result) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, cacheName+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(stateDir, cacheName))
}

// LoadCache reads the last saved result.
func LoadCache(stateDir string) (scan.Result, error) {
	var r scan.Result
	data, err := os.ReadFile(filepath.Join(stateDir, cacheName))
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(data, &r)
	return r, err
}
