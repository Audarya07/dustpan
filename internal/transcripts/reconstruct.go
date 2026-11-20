// Package transcripts rebuilds receipts from existing Claude Code transcripts
// so the first report can attribute leftovers. It reads tool-call metadata
// only (paths and commands); message text and file contents are never decoded.
package transcripts

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Audarya07/agent-receipts/internal/cmdparse"
	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/receipts"
)

const (
	agentName     = "claude"
	sourceName    = "transcript"
	ctxCheckEvery = 1000
	progressName  = "reconstruct.json"
	lockName      = "reconstruct.lock"
)

// header holds the only per-line fields read from every line.
type header struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	AITitle   string `json:"aiTitle"`
}

// assistantLine decodes tool_use metadata. Input is a narrow struct: content,
// old_string, new_string and message text are skipped by the decoder and never
// stored.
type assistantLine struct {
	Message struct {
		Content []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				FilePath     string `json:"file_path"`
				NotebookPath string `json:"notebook_path"`
				Command      string `json:"command"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

type session struct {
	id          string
	name        string
	cwd         string
	first, last time.Time
	created     []receipts.Receipt
}

// ParseFile reads complete JSONL lines of a Claude transcript starting at
// offset and emits receipts: one session-start (only when offset is 0, so a
// resumed parse never restarts a session), "created" receipts for interesting
// outputs, and one session-end. It returns the offset just past the last
// complete line read.
func ParseFile(e env.Env, path string, offset int64, emit func(receipts.Receipt)) (int64, error) {
	off, _, err := parseFile(context.Background(), e, path, offset, emit)
	return off, err
}

// parseFile is ParseFile plus cancellation: ctx is checked every
// ctxCheckEvery lines; on cancellation it emits what it has and returns
// cancelled=true with the offset of the last complete line processed.
func parseFile(ctx context.Context, e env.Env, path string, offset int64, emit func(receipts.Receipt)) (int64, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, false, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, false, err
	}
	br := bufio.NewReaderSize(f, 256*1024)

	sessions := map[string]*session{}
	var order []string
	get := func(id string) *session {
		s := sessions[id]
		if s == nil {
			s = &session{id: id}
			sessions[id] = s
			order = append(order, id)
		}
		return s
	}

	off := offset
	cancelled := false
	var readErr error
	for lines := 0; ; lines++ {
		if lines > 0 && lines%ctxCheckEvery == 0 && ctx.Err() != nil {
			cancelled = true
			break
		}
		line, rerr := br.ReadBytes('\n')
		if rerr != nil {
			// A final line without a newline is incomplete; leave it for later.
			if !errors.Is(rerr, io.EOF) {
				readErr = rerr
			}
			break
		}
		off += int64(len(line))
		var h header
		if json.Unmarshal(line, &h) != nil || h.SessionID == "" {
			continue
		}
		s := get(h.SessionID)
		if s.cwd == "" && h.Cwd != "" {
			s.cwd = e.Canon(h.Cwd)
		}
		if ts, err := time.Parse(time.RFC3339, h.Timestamp); err == nil {
			if s.first.IsZero() || ts.Before(s.first) {
				s.first = ts
			}
			if ts.After(s.last) {
				s.last = ts
			}
		}
		switch h.Type {
		case "ai-title":
			if h.AITitle != "" {
				s.name = h.AITitle
			}
		case "assistant":
			var a assistantLine
			if json.Unmarshal(line, &a) != nil {
				continue
			}
			at, _ := time.Parse(time.RFC3339, h.Timestamp)
			for _, c := range a.Message.Content {
				if c.Type != "tool_use" {
					continue
				}
				s.created = append(s.created, toReceipts(e, h.Cwd, at, c.Name, c.Input.FilePath, c.Input.NotebookPath, c.Input.Command)...)
			}
		}
	}

	for _, id := range order {
		s := sessions[id]
		base := receipts.Receipt{Agent: agentName, Source: sourceName, SessionID: id, SessionName: s.name, Cwd: s.cwd}
		if offset == 0 && !s.first.IsZero() {
			r := base
			r.Kind, r.At, r.TranscriptPath = receipts.KindSessionStart, s.first, path
			emit(r)
		}
		for _, r := range s.created {
			r.Agent, r.Source, r.SessionID, r.SessionName = agentName, sourceName, id, s.name
			emit(r)
		}
		if !s.last.IsZero() {
			r := base
			r.Kind, r.At = receipts.KindSessionEnd, s.last
			emit(r)
		}
	}
	return off, cancelled, readErr
}

// toReceipts maps one tool call to zero or more interesting "created"
// receipts, canonicalized per the controller rulings.
func toReceipts(e env.Env, cwd string, at time.Time, tool, filePath, notebookPath, command string) []receipts.Receipt {
	type out struct {
		path  string
		isDir bool
		via   string
	}
	var outs []out
	switch tool {
	case "Write":
		if filePath != "" {
			outs = append(outs, out{filePath, false, "Write"})
		}
	case "NotebookEdit":
		if notebookPath != "" {
			outs = append(outs, out{notebookPath, false, "NotebookEdit"})
		}
	case "Bash":
		if command != "" {
			for _, o := range cmdparse.OutputPaths(command, cwd) {
				outs = append(outs, out{o.Path, o.IsDir, o.Via})
			}
		}
	}
	if len(outs) == 0 {
		return nil
	}
	ccwd := cwd
	if cwd != "" {
		ccwd = e.Canon(cwd)
	}
	var rs []receipts.Receipt
	for _, o := range outs {
		p := o.path
		if receipts.HasDotDot(p) || (!filepath.IsAbs(p) && receipts.HasDotDot(cwd)) {
			continue // never resolve ".." lexically
		}
		if !filepath.IsAbs(p) {
			if cwd == "" {
				continue
			}
			p = filepath.Join(cwd, p)
		}
		p = e.CanonParent(p)
		if !receipts.Interesting(p, ccwd, e.TmpDirs, o.via) {
			continue
		}
		rs = append(rs, receipts.Receipt{At: at, Kind: receipts.KindCreated, Path: p, IsDir: o.isDir, Cwd: ccwd, Via: o.via})
	}
	return rs
}

// entry is the saved position in one transcript plus the file identity it
// belongs to, so a replaced file restarts from 0. An old-format bare number is
// accepted as an offset with unknown identity.
type entry struct {
	Offset int64  `json:"offset"`
	Dev    uint64 `json:"dev,omitempty"`
	Ino    uint64 `json:"ino,omitempty"`
}

func (en *entry) UnmarshalJSON(b []byte) error {
	var n int64
	if json.Unmarshal(b, &n) == nil {
		*en = entry{Offset: n}
		return nil
	}
	type plain entry
	return json.Unmarshal(b, (*plain)(en))
}

type progress struct {
	Files map[string]entry `json:"files"`
}

func loadProgress(dir string) progress {
	p := progress{Files: map[string]entry{}}
	if b, err := os.ReadFile(filepath.Join(dir, progressName)); err == nil {
		var q progress
		if json.Unmarshal(b, &q) == nil && q.Files != nil {
			p = q
		}
	}
	return p
}

func saveProgress(dir string, p progress) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, progressName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, progressName))
}

type tfile struct {
	path  string
	mtime time.Time
	size  int64
	dev   uint64
	ino   uint64
}

func listTranscripts(home string) []tfile {
	var list []tfile
	root := filepath.Join(home, ".claude", "projects")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() {
			tf := tfile{path: p, mtime: fi.ModTime(), size: fi.Size()}
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				tf.dev, tf.ino = uint64(st.Dev), uint64(st.Ino)
			}
			list = append(list, tf)
		}
		return nil
	})
	sort.Slice(list, func(i, j int) bool { return list[i].mtime.After(list[j].mtime) })
	return list
}

// Reconstruct reads Claude transcripts newest first and appends receipts to
// st, resuming from <StateDir>/reconstruct.json. It returns done=true when
// every transcript has been read to its current size. If another Reconstruct
// holds the lock it returns (false, nil) without touching anything.
func Reconstruct(ctx context.Context, e env.Env, st *receipts.Store) (bool, error) {
	if err := e.EnsureStateDir(); err != nil {
		return false, err
	}
	lf, err := os.OpenFile(filepath.Join(e.StateDir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer lf.Close()
	if err := unix.Flock(int(lf.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	defer unix.Flock(int(lf.Fd()), unix.LOCK_UN)

	prog := loadProgress(e.StateDir)
	for _, tf := range listTranscripts(e.Home) {
		if ctx.Err() != nil {
			return false, nil
		}
		saved := prog.Files[tf.path]
		start := saved.Offset
		replaced := saved.Dev != 0 && (saved.Dev != tf.dev || saved.Ino != tf.ino)
		if tf.size < start || replaced {
			start = 0
		}
		if start == tf.size {
			prog.Files[tf.path] = entry{start, tf.dev, tf.ino}
			continue
		}
		var batch []receipts.Receipt
		off, cancelled, perr := parseFile(ctx, e, tf.path, start, func(r receipts.Receipt) { batch = append(batch, r) })
		if len(batch) > 0 {
			if err := st.Append(batch...); err != nil {
				return false, err
			}
		}
		// A transcript we cannot open or read is skipped, not fatal: whatever
		// was read is kept, and the file is retried on the next run because
		// its saved offset stays short of its size.
		if perr == nil || off > start {
			prog.Files[tf.path] = entry{off, tf.dev, tf.ino}
		}
		if err := saveProgress(e.StateDir, prog); err != nil {
			return false, err
		}
		if cancelled {
			return false, nil
		}
	}
	return true, nil
}
