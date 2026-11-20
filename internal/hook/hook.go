// Package hook handles Claude Code hook events. Hooks must be inert: fast,
// silent, and limited to this tool's state dir and stdout. Handle never
// reports problems to the caller beyond a returned error that main ignores.
package hook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Audarya07/agent-receipts/internal/cmdparse"
	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/nudge"
	"github.com/Audarya07/agent-receipts/internal/proc"
	"github.com/Audarya07/agent-receipts/internal/receipts"
	"github.com/Audarya07/agent-receipts/internal/refreshlock"
	"github.com/Audarya07/agent-receipts/internal/report"
	"github.com/Audarya07/agent-receipts/internal/stats"
)

// Input is Claude Code's hook payload (only the fields we need).
type Input struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	Event          string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Source         string          `json:"source"`
}

// Deps are the hook's collaborators; tests replace Procs and SpawnRefresh.
type Deps struct {
	Env          env.Env
	Store        *receipts.Store
	Procs        func(context.Context) (*proc.Snapshot, error)
	ParentPID    int
	SpawnRefresh func() error
}

const (
	tailBytes   = 256 * 1024
	receiptFile = "receipts.jsonl"
)

// Handle processes one hook event. Unknown events and bad input are ignored.
func Handle(ctx context.Context, d Deps, event string, in io.Reader, out io.Writer) error {
	if !filepath.IsAbs(d.Env.Home) || !filepath.IsAbs(d.Env.StateDir) {
		return nil
	}
	var p Input
	if err := json.NewDecoder(io.LimitReader(in, 4<<20)).Decode(&p); err != nil {
		return nil
	}
	switch event {
	case "SessionStart":
		return sessionStart(ctx, d, p, out)
	case "PostToolUse":
		return postToolUse(d, p)
	case "SessionEnd":
		return sessionEnd(ctx, d, p)
	}
	return nil // includes SubagentStop: subagents share the parent's session id
}

func (d Deps) now() time.Time {
	if d.Env.Now != nil {
		return d.Env.Now()
	}
	return time.Now()
}

func sessionStart(ctx context.Context, d Deps, p Input, out io.Writer) error {
	if p.SessionID == "" {
		return nil
	}
	r := receipts.Receipt{
		At: d.now(), Kind: receipts.KindSessionStart, Agent: "claude", SessionID: p.SessionID,
		Cwd: d.Env.Canon(p.Cwd), TranscriptPath: p.TranscriptPath, Source: "hook",
	}
	if p.Cwd == "" {
		r.Cwd = ""
	}
	if d.Procs != nil {
		if snap, err := d.Procs(ctx); err == nil && snap != nil {
			if pr, _, ok := snap.AgentAncestor(d.ParentPID); ok {
				r.PID, r.PIDStart = pr.PID, pr.Start
			}
		}
	}
	err := d.Store.Append(r)
	if err2 := writeSnapshot(d, p.SessionID, p.Source == "compact"); err == nil {
		err = err2
	}
	if err2 := maybeNudge(d, out); err == nil {
		err = err2
	}
	return err
}

func maybeNudge(d Deps, out io.Writer) error {
	cache, err := report.LoadCache(d.Env.StateDir)
	if err != nil {
		return nil // no cache, no nudge
	}
	st, _ := nudge.Load(d.Env.StateDir)
	now := d.now()
	msg, show, next := nudge.Decide(now, cache, st, stats.Load(d.Env.StateDir).LastReport)
	err = nudge.Save(d.Env.StateDir, next) // even when not shown: judged-ignored counts
	if !show {
		return err
	}
	b, merr := json.Marshal(struct {
		SystemMessage string `json:"systemMessage"`
	}{msg})
	if merr != nil {
		return merr
	}
	if _, werr := out.Write(append(b, '\n')); werr != nil {
		return werr
	}
	return errors.Join(err, stats.Update(d.Env.StateDir, func(s *stats.Stats) {
		s.NudgesShown++
		s.LastNudge = now
	}))
}

type toolFields struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Command      string `json:"command"`
}

func postToolUse(d Deps, p Input) error {
	if p.SessionID == "" || len(p.ToolInput) == 0 {
		return nil
	}
	var tf toolFields // decodes only these fields, never "content"
	if json.Unmarshal(p.ToolInput, &tf) != nil {
		return nil
	}
	type created struct {
		path, via string
		isDir     bool
	}
	var found []created
	switch p.ToolName {
	case "Write":
		if tf.FilePath != "" {
			found = append(found, created{path: tf.FilePath, via: "Write"})
		}
	case "NotebookEdit":
		if tf.NotebookPath != "" {
			found = append(found, created{path: tf.NotebookPath, via: "NotebookEdit"})
		}
	case "Bash":
		for _, o := range cmdparse.OutputPaths(tf.Command, p.Cwd) {
			found = append(found, created{o.Path, o.Via, o.IsDir})
		}
	default:
		return nil
	}
	cwd := d.Env.Canon(p.Cwd)
	var rs []receipts.Receipt
	for _, c := range found {
		path := c.path
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.Cwd, path)
		}
		path = d.Env.CanonParent(path)
		if !receipts.Interesting(path, cwd, d.Env.TmpDirs, c.via) {
			continue
		}
		rs = append(rs, receipts.Receipt{
			At: d.now(), Kind: receipts.KindCreated, Agent: "claude", SessionID: p.SessionID,
			Path: path, IsDir: c.isDir, Cwd: cwd, Via: c.via, Source: "hook",
		})
	}
	if len(rs) == 0 {
		return nil
	}
	return d.Store.Append(rs...)
}

func sessionEnd(ctx context.Context, d Deps, p Input) error {
	if p.SessionID == "" {
		return nil
	}
	err := d.Store.Append(receipts.Receipt{
		At: d.now(), Kind: receipts.KindSessionEnd, Agent: "claude", SessionID: p.SessionID, Source: "hook",
	})
	err = errors.Join(err, weakAttribution(ctx, d, p.SessionID))
	if d.SpawnRefresh != nil && !refreshlock.Held(d.Env.StateDir) {
		err = errors.Join(err, d.SpawnRefresh())
	}
	return err
}

func weakAttribution(ctx context.Context, d Deps, session string) error {
	sp := snapshotPath(d.Env.StateDir, session)
	defer os.Remove(sp)
	data, err := os.ReadFile(sp)
	if err != nil {
		return nil
	}
	var before map[string]int64
	if json.Unmarshal(data, &before) != nil {
		return nil
	}
	now, err := scanTmp(d.Env.TmpDirs)
	if err != nil {
		return err
	}
	var fresh []string
	for path := range now {
		if _, ok := before[path]; !ok {
			fresh = append(fresh, path)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	ix := receipts.BuildIndex(loadTail(d.Env.StateDir))
	sessions := []string{session}
	if d.Procs == nil {
		return nil
	}
	snap, perr := d.Procs(ctx)
	if perr != nil || snap == nil {
		return nil // liveness unknown: do not guess at attribution
	}
	for id, s := range ix.Sessions() {
		if id != session && s.PID != 0 && !s.PIDStart.IsZero() && snap.Alive(s.PID, s.PIDStart) {
			sessions = append(sessions, id)
		}
	}
	var rs []receipts.Receipt
	for _, path := range fresh {
		path = d.Env.CanonParent(path)
		if _, ok := ix.StrongCreator(path); ok {
			continue
		}
		rs = append(rs, receipts.Receipt{
			At: d.now(), Kind: receipts.KindWeakCreated, Agent: "claude", SessionID: session,
			Path: path, Source: "snapshot", Sessions: sessions,
		})
	}
	if len(rs) == 0 {
		return nil
	}
	return d.Store.Append(rs...)
}

// loadTail parses the last tailBytes of receipts.jsonl.
func loadTail(stateDir string) []receipts.Receipt {
	f, err := os.Open(filepath.Join(stateDir, receiptFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	off := int64(0)
	if fi.Size() > tailBytes {
		off = fi.Size() - tailBytes
	}
	buf := make([]byte, fi.Size()-off)
	n, _ := f.ReadAt(buf, off)
	buf = buf[:n]
	if off > 0 { // drop the partial first line
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			return nil
		}
	}
	var out []receipts.Receipt
	for _, line := range bytes.Split(buf, []byte{'\n'}) {
		var r receipts.Receipt
		if len(line) > 0 && json.Unmarshal(line, &r) == nil && r.Kind != "" {
			out = append(out, r)
		}
	}
	return out
}

// scanTmp maps each top-level entry of every tmp dir (full path) to its mtime.
func scanTmp(tmpDirs []string) (map[string]int64, error) {
	m := map[string]int64{}
	for _, dir := range tmpDirs {
		f, err := os.Open(dir)
		if err != nil {
			continue
		}
		ents, _ := f.ReadDir(-1)
		f.Close()
		for _, e := range ents {
			if fi, err := e.Info(); err == nil {
				m[filepath.Join(dir, e.Name())] = fi.ModTime().UnixNano()
			}
		}
	}
	return m, nil
}

func writeSnapshot(d Deps, session string, keepExisting bool) error {
	if keepExisting { // compaction re-fires SessionStart; keep the original baseline
		if _, err := os.Stat(snapshotPath(d.Env.StateDir, session)); err == nil {
			return nil
		}
	}
	m, _ := scanTmp(d.Env.TmpDirs)
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dir := filepath.Join(d.Env.StateDir, "snapshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(snapshotPath(d.Env.StateDir, session), data, 0o600)
}

// snapshotPath maps a session id to a file inside <state>/snapshots; ids with
// characters outside [A-Za-z0-9._-] (or dot-only names) are hashed.
func snapshotPath(stateDir, session string) string {
	ok := session != "" && len(session) <= 128 && strings.Trim(session, ".") != ""
	for _, r := range session {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			ok = false
		}
	}
	name := session
	if !ok {
		h := sha256.Sum256([]byte(session))
		name = hex.EncodeToString(h[:16])
	}
	return filepath.Join(stateDir, "snapshots", name+".json")
}

// SpawnRefresh starts a detached `agent-receipts refresh` and does not wait.
func SpawnRefresh() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "refresh")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// PruneSnapshots removes snapshot files older than maxAge (sessions that
// crashed without a SessionEnd).
func PruneSnapshots(stateDir string, maxAge time.Duration, now time.Time) {
	dir := filepath.Join(stateDir, "snapshots")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && !e.IsDir() && now.Sub(fi.ModTime()) > maxAge {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
