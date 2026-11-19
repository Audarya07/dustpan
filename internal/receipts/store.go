// Package receipts records which agent session created which path.
// Callers pass already-canonical paths; nothing here touches the filesystem
// beyond the receipts file itself.
package receipts

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type Kind string

const (
	KindSessionStart Kind = "session-start"
	KindSessionEnd   Kind = "session-end"
	KindCreated      Kind = "created"
	KindWeakCreated  Kind = "weak-created"
)

type Receipt struct {
	At             time.Time `json:"at,omitzero"`
	Kind           Kind      `json:"kind"`
	Agent          string    `json:"agent,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	SessionName    string    `json:"session_name,omitempty"`
	Path           string    `json:"path,omitempty"`
	IsDir          bool      `json:"is_dir,omitempty"`
	Cwd            string    `json:"cwd,omitempty"`
	Via            string    `json:"via,omitempty"`
	Source         string    `json:"source,omitempty"` // "hook" | "transcript" | "snapshot"
	PID            int       `json:"pid,omitempty"`
	PIDStart       time.Time `json:"pid_start,omitzero"`
	TranscriptPath string    `json:"transcript_path,omitempty"`
	Sessions       []string  `json:"sessions,omitempty"`
}

const fileName = "receipts.jsonl"

type Store struct{ dir string }

func Open(stateDir string) *Store { return &Store{dir: stateDir} }

func (s *Store) path() string { return filepath.Join(s.dir, fileName) }

// Append writes each receipt as one JSON line using a single write call on an
// O_APPEND file, so concurrent writers never interleave within a line.
func (s *Store) Append(rs ...Receipt) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(), os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	// A crash can leave a truncated last line; start on a fresh line so it
	// cannot swallow the next receipt. A racing blank line is harmless.
	prefix := needsNewline(f)
	for _, r := range rs {
		b, err := json.Marshal(r)
		if err != nil {
			f.Close()
			return err
		}
		b = append(b, '\n')
		if prefix {
			b = append([]byte{'\n'}, b...)
			prefix = false
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// Load returns all parseable receipts in file order. Lines that fail to parse
// (including a truncated last line) are skipped. A missing file yields none.
func (s *Store) Load() ([]Receipt, error) {
	f, err := os.Open(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Receipt
	br := bufio.NewReader(f)
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			var r Receipt
			if json.Unmarshal(line, &r) == nil && r.Kind != "" {
				out = append(out, r)
			}
		}
		if rerr != nil {
			break
		}
	}
	return out, nil
}

func needsNewline(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return false
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], fi.Size()-1); err != nil {
		return false
	}
	return last[0] != '\n'
}
