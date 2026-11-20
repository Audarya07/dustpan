// Package safety decides whether an item may be offered for deletion. Every
// check that fails or cannot run keeps the item. When unsure, keep.
package safety

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/gitwt"
	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/proc"
	"github.com/Audarya07/agent-receipts/internal/receipts"
)

// RecentWindow is how recently something may have changed before we keep it.
const RecentWindow = 24 * time.Hour

// Inputs is everything Evaluate needs. A nil Proc with nil ProcErr (and a nil
// Git with nil GitErr for worktrees) means "not checked", never "all clear".
type Inputs struct {
	Item         item.Item
	Env          env.Env
	Proc         *proc.Snapshot
	ProcErr      error
	Git          *gitwt.State
	GitErr       error
	SessionAlive func(id string) (alive bool, lastSeen time.Time)
	// GitCheckout is required for non-worktree items: it returns a reason
	// when path contains or sits inside a git checkout, or an error when it
	// couldn't tell. Nil means not checked, which keeps the item.
	GitCheckout     func(path string) (reason string, err error)
	TrashAvailable  func(path string) bool
	MainCheckouts   []string
	ExtraNeverTouch []string
}

// roots returns p and its canonical form (deduplicated, empties dropped).
func variants(e env.Env, p string) []string {
	if p == "" {
		return nil
	}
	out := []string{fold(filepath.Clean(p))}
	if c := fold(e.Canon(p)); c != out[0] {
		out = append(out, c)
	}
	return out
}

// fold lowercases paths on macOS, whose default filesystem is case-insensitive.
// Folding only adds matches, so it fails closed. Every comparison in NeverTouch
// goes through variants, so paths and roots are always folded alike.
func fold(p string) string {
	if runtime.GOOS == "darwin" {
		return strings.ToLower(p)
	}
	return p
}

// agentContainers are agent-owned folders under home: each one and every
// ancestor is never-touch; entries strictly inside are judged by other rules.
var agentContainers = []string{
	".claude", ".codex", ".cursor",
	".claude/jobs", ".claude/projects",
	".codex/worktrees", ".codex/sessions", ".cursor/worktrees",
}

// NeverTouch reports whether path must never be offered, and why.
func NeverTouch(path string, e env.Env, mainCheckouts, extra []string) (bool, string) {
	if path == "" {
		return true, "empty path"
	}
	paths := variants(e, path)
	for _, p := range paths {
		if p == "/" {
			return true, "filesystem root"
		}
	}
	for _, p := range paths {
		// Home and anything above it.
		for _, h := range variants(e, e.Home) {
			if receipts.Within(h, p) {
				if p == h {
					return true, "your home folder"
				}
				return true, "contains your home folder"
			}
			if filepath.Dir(p) == h && filepath.Base(p)[0] == '.' {
				return true, "a dotfile in your home folder"
			}
			for _, c := range agentContainers {
				if receipts.Within(filepath.Join(h, c), p) {
					return true, "an agent data folder"
				}
			}
			if filepath.Dir(p) == filepath.Join(h, ".claude", "projects") {
				return true, "an agent project folder (holds memory)"
			}
		}
		// Tmp roots (and anything above them).
		for _, t := range e.TmpDirs {
			for _, tv := range variants(e, t) {
				if receipts.Within(tv, p) {
					return true, "a temp folder itself"
				}
				if receipts.Within(p, tv) && p != tv {
					rel, _ := filepath.Rel(tv, p)
					first := rel
					for i := 0; i < len(rel); i++ {
						if rel[i] == '/' {
							first = rel[:i]
							break
						}
					}
					for _, pat := range []string{"claude-*", "cc-daemon-*", "cc-socks"} {
						if ok, _ := filepath.Match(pat, first); ok {
							return true, "agent runtime folder"
						}
					}
				}
			}
		}
		// Job dir: itself, inside, or above.
		for _, j := range variants(e, e.JobDir) {
			if receipts.Within(j, p) || receipts.Within(p, j) {
				return true, "the running agent job folder"
			}
		}
		// Main checkouts.
		for _, c := range mainCheckouts {
			for _, cv := range variants(e, c) {
				if receipts.Within(cv, p) {
					return true, "contains a main checkout"
				}
				if receipts.Within(p, cv) {
					wt := filepath.Join(cv, ".claude", "worktrees")
					if p != wt && receipts.Within(p, wt) {
						continue
					}
					return true, "inside a main checkout"
				}
			}
		}
		for _, x := range extra {
			for _, xv := range variants(e, x) {
				if receipts.Within(p, xv) || receipts.Within(xv, p) {
					return true, "on your never-touch list"
				}
			}
		}
		if why := configFile(p, e); why != "" {
			return true, why
		}
	}
	return false, ""
}

func configFile(p string, e env.Env) string {
	base := filepath.Base(p)
	parent := filepath.Base(filepath.Dir(p))
	switch {
	case strings.EqualFold(base, "CLAUDE.md"):
		return "agent instructions (CLAUDE.md)"
	case base == "mcp.json":
		return "agent MCP config"
	case base == ".claude.json":
		return "agent config"
	case base == ".credentials.json":
		return "agent credentials"
	case parent == ".claude":
		if ok, _ := filepath.Match("settings*.json", base); ok {
			return "agent settings"
		}
	case parent == ".codex" && (base == "auth.json" || base == "config.toml"):
		return "agent login or config"
	}
	for d := p; d != "/" && d != "."; d = filepath.Dir(d) {
		if filepath.Base(d) == "memory" {
			return "agent memory"
		}
	}
	return ""
}

func keep(reasons ...string) item.Decision {
	return item.Decision{Class: item.LeftAlone, Reasons: reasons}
}

// Evaluate runs the safety rules in order; the first failing rule wins.
func Evaluate(in Inputs) item.Decision {
	it := in.Item
	if ok, why := NeverTouch(it.Path, in.Env, in.MainCheckouts, in.ExtraNeverTouch); ok {
		return item.Decision{Class: item.Hidden, Reasons: []string{why}}
	}
	ev := it.Evidence
	if !ev.Strong && len(ev.Weak) == 0 {
		return item.Decision{Class: item.Hidden, Reasons: []string{"no evidence an agent made this"}}
	}
	if !ev.Strong {
		return item.Decision{Class: item.LeftAlone, DetailsOnly: true,
			Reasons: []string{"only weak evidence an agent made this"}}
	}
	if it.Category != item.Worktree {
		if in.GitCheckout == nil {
			return keep("couldn't check for git checkouts")
		}
		why, err := in.GitCheckout(it.Path)
		if err != nil {
			return keep("couldn't check for git checkouts")
		}
		if why != "" {
			return keep(why)
		}
	}
	if in.ProcErr != nil || in.Proc == nil {
		return keep("couldn't check running processes")
	}
	if used, why := in.Proc.InUse(it.Path); used {
		return keep(why)
	}
	var ids []string
	if ev.SessionID != "" {
		ids = append(ids, ev.SessionID)
	}
	ids = append(ids, ev.Sessions...)
	if len(ids) > 0 && in.SessionAlive == nil {
		return keep("couldn't check whether the session is still running")
	}
	for _, id := range ids {
		if alive, seen := in.SessionAlive(id); alive {
			mins := int(in.Env.Now().Sub(seen) / time.Minute)
			if mins < 1 {
				return keep("session active just now")
			}
			return keep(fmt.Sprintf("session active %d min ago", mins))
		}
	}
	if it.Unregister && it.Category == item.Worktree {
		if in.Git != nil && in.Git.Locked {
			return keep("worktree is locked")
		}
		// Directory already gone: nothing on disk to lose, the branch remains,
		// and nothing is moved or deleted, so Permanent stays false.
		return item.Decision{Class: item.Offer}
	}
	if it.Newest.IsZero() {
		return keep("couldn't tell when it last changed")
	} else if age := in.Env.Now().Sub(it.Newest); age < RecentWindow {
		if age < 0 {
			age = 0
		}
		return keep(fmt.Sprintf("changed %dh ago", int(age/time.Hour)))
	}
	if it.Category == item.Worktree {
		if in.GitErr != nil || in.Git == nil {
			return keep("couldn't check git state")
		}
		if r := in.Git.Unsaved(); len(r) > 0 {
			return keep(r...)
		}
	}
	return item.Decision{Class: item.Offer, Permanent: noTrash(in)}
}

// noTrash is true when there is no same-filesystem Trash (or we can't tell).
func noTrash(in Inputs) bool {
	return in.TrashAvailable == nil || !in.TrashAvailable(in.Item.Path)
}
