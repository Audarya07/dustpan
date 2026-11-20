package scan

import (
	"context"
	"os"
	"sort"
	"time"

	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/receipts"
	"github.com/Audarya07/agent-receipts/internal/safety"
)

// birthTime reports when a path itself was created; tests replace it.
var birthTime = fileBirthTime

// receiptClockSlack is how far a path's mtime may predate the receipt that
// supposedly created it before the receipt stops counting as strong evidence.
const receiptClockSlack = 2 * time.Minute

// CollectScratch returns the receipted agent scratch under the temp folders
// as unevaluated items. Run is the full pipeline.
func CollectScratch(ctx context.Context, c Context) []item.Item {
	return itemsOf(collectScratchCandidates(ctx, c, &runState{}))
}

func collectScratchCandidates(ctx context.Context, c Context, st *runState) []candidate {
	if c.Index == nil {
		return nil
	}
	e := c.Env
	var tmps []string
	for _, d := range e.TmpDirs {
		tmps = append(tmps, e.Canon(d))
	}
	// usable keeps receipts for existing paths strictly inside a tmp dir,
	// dropping anything that must never be touched BEFORE nesting is
	// considered: a receipt for "/tmp" itself must not absorb (and hide) the
	// real scratch inside it.
	usable := func(r receipts.Receipt) bool {
		if r.Path == "" || !withinAny(r.Path, tmps) || containsPath(tmps, r.Path) {
			return false
		}
		if never, _ := safety.NeverTouch(r.Path, e, st.checkoutsFor(item.Scratch), c.Config.ExtraNeverTouch); never {
			return false
		}
		_, err := os.Lstat(r.Path)
		return err == nil
	}
	// Latest receipt per canonical path.
	latest := map[string]receipts.Receipt{}
	for _, r := range CanonReceipts(e, c.Index.CreatedUnder(tmps)) {
		if usable(r) {
			latest[r.Path] = r
		}
	}
	paths := make([]string, 0, len(latest))
	for p := range latest {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []candidate
	for _, p := range paths {
		if ctx.Err() != nil {
			break
		}
		nested := false
		for _, q := range paths {
			if q != p && latest[q].IsDir && receipts.Within(p, q) {
				nested = true
				break
			}
		}
		if nested {
			continue
		}
		r := latest[p]
		ev := scratchEvidence(c, r)
		// Sessions of every receipt merged into this item count too: a live
		// session that wrote inside keeps the whole folder.
		for _, q := range paths {
			if q != p && receipts.Within(q, p) {
				if id := latest[q].SessionID; id != "" && id != ev.SessionID && !containsPath(ev.Sessions, id) {
					ev.Sessions = append(ev.Sessions, id)
				}
			}
		}
		out = append(out, candidate{it: item.Item{
			Category: item.Scratch, Path: p, Evidence: ev,
		}})
	}
	return append(out, weakScratchCandidates(c, latest, usable)...)
}

// weakScratchCandidates lists /tmp entries known only from session snapshots
// ("created while sessions X, Y ran"). They are shown in --details and the
// safety engine never offers them. Paths covered by a strong receipt (the
// path itself or a receipted folder around it) are skipped.
func weakScratchCandidates(c Context, strong map[string]receipts.Receipt, usable func(receipts.Receipt) bool) []candidate {
	var tmps []string
	for _, d := range c.Env.TmpDirs {
		tmps = append(tmps, c.Env.Canon(d))
	}
	byPath := map[string]*item.Evidence{}
	var order []string
	for _, r := range CanonReceipts(c.Env, c.Index.WeakCreatedUnder(tmps)) {
		if !usable(r) || coveredByStrong(r.Path, strong) {
			continue
		}
		ev := byPath[r.Path]
		if ev == nil {
			ev = &item.Evidence{Source: "snapshot", Agent: r.Agent}
			byPath[r.Path] = ev
			order = append(order, r.Path)
		}
		for _, id := range append([]string{r.SessionID}, r.Sessions...) {
			if id != "" && !containsPath(ev.Weak, id) {
				ev.Weak = append(ev.Weak, id)
			}
		}
	}
	sort.Strings(order)
	var out []candidate
	for _, p := range order {
		ev := *byPath[p]
		if len(ev.Weak) == 0 {
			ev.Weak = []string{"unknown"}
		}
		out = append(out, candidate{it: item.Item{Category: item.Scratch, Path: p, Evidence: ev}})
	}
	return out
}

func coveredByStrong(path string, strong map[string]receipts.Receipt) bool {
	for p, r := range strong {
		if p == path || (r.IsDir && receipts.Within(path, p)) {
			return true
		}
	}
	return false
}

// scratchEvidence turns a receipt into evidence. A path that already existed
// before the command that supposedly created it is weak at best: `cp f
// /tmp/olddir` or `echo >> /tmp/old.log` bump an old path's mtime, so the
// birth time decides when the filesystem has one. Without it a directory
// counts only when the receipt says mkdir or git worktree add made it, and a
// file keeps the mtime rule (which also applies with a birth time).
func scratchEvidence(c Context, r receipts.Receipt) item.Evidence {
	ev := item.Evidence{
		Strong: true, Source: r.Source,
		SessionID: r.SessionID, Agent: r.Agent, SessionName: r.SessionName,
	}
	if ev.Source == "" || ev.Source == "hook" {
		ev.Source = "receipt"
	}
	if s, ok := c.Index.Sessions()[r.SessionID]; ok {
		if ev.Agent == "" {
			ev.Agent = s.Agent
		}
		if ev.SessionName == "" {
			ev.SessionName = s.Name
		}
	}
	if predates(r) {
		ev.Strong = false
		if r.SessionID != "" {
			ev.Weak = []string{r.SessionID}
		} else {
			ev.Weak = []string{"unknown"}
		}
	}
	return ev
}

// predates reports whether r.Path may have existed before r was written, or
// that we cannot tell.
func predates(r receipts.Receipt) bool {
	fi, err := os.Lstat(r.Path)
	if err != nil {
		return true
	}
	if r.At.IsZero() {
		return true // no time: nothing shows the path is newer than the receipt
	}
	cutoff := r.At.Add(-receiptClockSlack)
	if fi.ModTime().Before(cutoff) {
		return true
	}
	if born, ok := birthTime(r.Path); ok {
		return born.Before(cutoff)
	}
	return fi.IsDir() && r.Via != "mkdir" && r.Via != "git worktree add"
}

func withinAny(path string, dirs []string) bool {
	for _, d := range dirs {
		if receipts.Within(path, d) {
			return true
		}
	}
	return false
}

func containsPath(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
