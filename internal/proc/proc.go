// Package proc takes snapshots of running processes and open files so callers
// can refuse to touch anything a live process is using, and find which agent
// process owns a session.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Audarya07/dustpan/internal/receipts"
)

// Proc is one process as reported by ps.
type Proc struct {
	PID, PPID  int
	Start      time.Time
	Comm, Args string
}

// Snapshot is a point-in-time view of processes and open files.
type Snapshot struct {
	Procs map[int]Proc
	// Open is the sorted absolute paths of every open file and process cwd.
	Open []string
	// OpenBy optionally maps an Open path to a pid holding it open. It may be
	// nil; InUse then reports a generic reason.
	OpenBy map[string]int
}

const lstartLayout = "Mon Jan _2 15:04:05 2006"

// agents are the base names that identify a coding-agent process.
var agents = []string{"claude", "codex", "cursor-agent"}

// ParsePS parses `ps -axo pid=,ppid=,lstart=,comm=` output.
func ParsePS(out string) (map[int]Proc, error) {
	procs := map[int]Proc{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 8 {
			return nil, fmt.Errorf("proc: malformed ps line %q", line)
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("proc: malformed ps line %q", line)
		}
		start, err := time.ParseInLocation(lstartLayout, strings.Join(f[2:7], " "), time.Local)
		if err != nil {
			return nil, fmt.Errorf("proc: bad start time in %q: %w", line, err)
		}
		comm := strings.Join(f[7:], " ")
		procs[pid] = Proc{PID: pid, PPID: ppid, Start: start, Comm: comm, Args: comm}
	}
	return procs, nil
}

// ParseLsofF parses `lsof -F pn` output into sorted, de-duplicated paths.
func ParseLsofF(out string) []string {
	paths, _ := ParseLsofPIDs(out)
	return paths
}

// ParseLsofPIDs is ParseLsofF plus a path-to-pid map (first pid seen wins).
func ParseLsofPIDs(out string) ([]string, map[string]int) {
	by := map[string]int{}
	pid := 0
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			if n, err := strconv.Atoi(line[1:]); err == nil {
				pid = n
			}
		case 'n':
			p := line[1:]
			if !strings.HasPrefix(p, "/") {
				continue // sockets, pipes etc.
			}
			if _, seen := by[p]; !seen {
				by[p] = pid
			}
		}
	}
	paths := make([]string, 0, len(by))
	for p := range by {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, by
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	if err != nil {
		var ee *exec.ExitError
		// lsof exits 1 when some files are unreadable; partial output is fine.
		if name == "lsof" && errors.As(err, &ee) && ee.ExitCode() == 1 && stdout.Len() > 0 {
			return stdout.Bytes(), nil
		}
		return nil, fmt.Errorf("proc: %s failed: %w", name, err)
	}
	return stdout.Bytes(), nil
}

// TakeProcsOnly snapshots processes via ps only (cheap enough for hooks).
func TakeProcsOnly(ctx context.Context) (*Snapshot, error) {
	out, err := run(ctx, "ps", "-axo", "pid=,ppid=,lstart=,comm=")
	if err != nil {
		return nil, err
	}
	procs, err := ParsePS(string(out))
	if err != nil {
		return nil, err
	}
	return &Snapshot{Procs: procs}, nil
}

// Take snapshots processes and open files. Any command failure is an error.
func Take(ctx context.Context) (*Snapshot, error) {
	s, err := TakeProcsOnly(ctx)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, "lsof", "-n", "-P", "-w", "-F", "pn")
	if err != nil {
		return nil, err
	}
	s.Open, s.OpenBy = ParseLsofPIDs(string(out))
	return s, nil
}

// Alive reports whether pid exists and started within 1s of start, which
// guards against PID reuse.
func (s *Snapshot) Alive(pid int, start time.Time) bool {
	p, ok := s.Procs[pid]
	if !ok {
		return false
	}
	d := p.Start.Sub(start)
	if d < 0 {
		d = -d
	}
	return d <= time.Second
}

// InUse reports whether any open file or cwd lies within path, ignoring case
// on macOS (receipts.WithinFold, the same fold as safety.NeverTouch).
func (s *Snapshot) InUse(path string) (bool, string) {
	for _, o := range s.Open {
		if !receipts.WithinFold(o, path) {
			continue
		}
		if pid, ok := s.OpenBy[o]; ok && pid > 0 {
			if p, ok := s.Procs[pid]; ok {
				return true, fmt.Sprintf("process %s (%d) has files open here", filepath.Base(p.Comm), pid)
			}
			return true, fmt.Sprintf("process %d has files open here", pid)
		}
		return true, "a process has files open here"
	}
	return false, ""
}

func agentName(p Proc) string {
	for _, c := range []string{p.Comm, p.Args} {
		if strings.Contains(c, "/claude/versions/") {
			return "claude"
		}
		f := strings.Fields(c)
		if len(f) == 0 {
			continue
		}
		b := filepath.Base(f[0])
		for _, a := range agents {
			if b == a {
				return a
			}
		}
	}
	return ""
}

// AgentAncestor walks from pid up through parents (at most 32 hops) and
// returns the first process that is a known coding agent.
func (s *Snapshot) AgentAncestor(pid int) (Proc, string, bool) {
	for i := 0; i < 32; i++ {
		p, ok := s.Procs[pid]
		if !ok {
			break
		}
		if a := agentName(p); a != "" {
			return p, a, true
		}
		if p.PPID == pid || p.PPID <= 0 {
			break
		}
		pid = p.PPID
	}
	return Proc{}, "", false
}
