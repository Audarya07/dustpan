// Package clean is the interactive, one-item-at-a-time cleanup. It is the
// only code path that moves or deletes the engineer's files, so every item is
// re-checked right before it is touched, and anything doubtful is skipped.
package clean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Audarya07/dustpan/internal/env"
	"github.com/Audarya07/dustpan/internal/item"
	"github.com/Audarya07/dustpan/internal/report"
	"github.com/Audarya07/dustpan/internal/scan"
	"github.com/Audarya07/dustpan/internal/stats"
)

// ErrNoTerminal is returned when clean is run without a real terminal.
var ErrNoTerminal = fmt.Errorf("clean needs a terminal. Run '%s clean' yourself in a terminal window.", env.AppName)

// UI is the human side of clean.
type UI interface {
	Printf(format string, a ...any)
	// Choose asks until the answer is one of keys and returns it (lower case).
	Choose(prompt string, keys []string) (string, error)
	// Confirm asks the human to type word; it reports whether they did.
	Confirm(prompt, word string) (bool, error)
}

// Deps are the effects clean performs; main wires the real ones.
type Deps struct {
	Scan              func(context.Context) (scan.Result, error)
	Recheck           func(context.Context, item.Item) (item.Item, error)
	Trash             interface{ Move(string) (string, error) }
	DeletePermanently func(path string, expect os.FileInfo) error
	RemoveWorktree    func(repo, path string) error
	StateDir          string
	IsTTY             func() bool
	Now               func() time.Time // nil means time.Now
}

// Summary is what one clean run did.
type Summary struct {
	Trashed, Deleted, Unregistered, Kept, Skipped int
	BytesTrashed, BytesDeleted                    int64
	SecretReal, SecretNotReal, SecretUnsure       int
}

// Run walks the offered items one at a time.
func Run(ctx context.Context, d Deps, ui UI) (Summary, error) {
	var sum Summary
	if d.IsTTY == nil || !d.IsTTY() {
		return sum, ErrNoTerminal
	}
	res, err := d.Scan(ctx)
	if err != nil {
		return sum, err
	}
	items := offered(res.Items)
	if len(items) == 0 {
		ui.Printf("Nothing to clean up.\n")
		return sum, nil
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}

	var runErr error
loop:
	for i, it := range items {
		ui.Printf("\n[%d/%d] %s\n", i+1, len(items), describe(it, now()))
		keys, prompt := choices(it)
		ans, err := ui.Choose(prompt, keys)
		if err != nil {
			runErr = err
			sum.Skipped += len(items) - i
			break
		}
		switch ans {
		case "s":
			sum.Skipped += len(items) - i
			break loop
		case "k":
			sum.Kept++
		default:
			if err := act(ctx, d, ui, it, ans, &sum); err != nil {
				runErr = err
				sum.Skipped += len(items) - i - 1
				break loop
			}
		}
	}

	finish(ui, d, sum)
	return sum, runErr
}

// offered keeps what may be offered, secrets first, then biggest first.
func offered(in []item.Item) []item.Item {
	var out []item.Item
	for _, it := range in {
		if it.Decision.Class == item.Offer && !it.Decision.DetailsOnly {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := len(out[i].Secrets) > 0, len(out[j].Secrets) > 0
		if si != sj {
			return si
		}
		return out[i].Size > out[j].Size
	})
	return out
}

func choices(it item.Item) (keys []string, prompt string) {
	switch {
	case it.Unregister:
		return []string{"u", "k", "s"}, "[u]nregister / [k]eep / [s]kip the rest"
	case it.Decision.Permanent:
		return []string{"p", "k", "s"}, "[p]ermanently delete / [k]eep / [s]kip the rest"
	case len(it.Secrets) > 0:
		return []string{"p", "t", "k", "s"}, "[p]ermanently delete / [t]rash anyway / [k]eep / [s]kip the rest"
	}
	return []string{"d", "k", "s"}, "[d]elete (to Trash) / [k]eep / [s]kip the rest"
}

const maxSecretLines = 5

func describe(it item.Item, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", it.ID, it.Path)
	if it.Unregister {
		b.WriteString("  the folder is already gone; git still lists this worktree\n")
	} else {
		fmt.Fprintf(&b, "  size: %s   age: %s\n", report.HumanSize(it.Size), age(now, it.Newest))
	}
	fmt.Fprintf(&b, "  made by: %s\n", attribution(it.Evidence))
	if it.DeadReason != "" {
		fmt.Fprintf(&b, "  why it is dead: %s\n", it.DeadReason)
	}
	if it.Decision.Permanent && !it.Unregister {
		b.WriteString("  note: no Trash on this disk, so deleting is permanent\n")
	}
	for i, f := range it.Secrets {
		if i == maxSecretLines {
			fmt.Fprintf(&b, "  ... and %d more possible secrets\n", len(it.Secrets)-maxSecretLines)
			break
		}
		fmt.Fprintf(&b, "  possible secret: %s:%d %s %s\n", report.SecretPath(it, f), f.Line, f.Description, f.Masked)
	}
	if it.SecretsPartial {
		b.WriteString("  (secret check was partial)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func age(now, t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	dur := now.Sub(t)
	switch {
	case dur < time.Minute:
		return "<1m"
	case dur < time.Hour:
		return fmt.Sprintf("%dm", int(dur.Minutes()))
	case dur < 48*time.Hour:
		return fmt.Sprintf("%dh", int(dur.Hours()))
	}
	return fmt.Sprintf("%dd", int(dur.Hours()/24))
}

func attribution(e item.Evidence) string {
	if !e.Strong {
		return "best guess — no receipt"
	}
	who := ""
	if e.Agent != "" {
		who = " (" + e.Agent + ")"
	}
	switch {
	case e.SessionName != "":
		return fmt.Sprintf("session %q%s", e.SessionName, who)
	case e.SessionID != "":
		id := e.SessionID
		if len(id) > 8 {
			id = id[:8]
		}
		return fmt.Sprintf("session %s%s", id, who)
	case e.Source != "":
		return e.Source + who
	}
	return "receipt" + who
}

// recheck re-evaluates it and returns the fresh item, or a skip reason.
func recheck(ctx context.Context, d Deps, it item.Item) (item.Item, string) {
	fresh, err := d.Recheck(ctx, it)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fresh, "skipped: no longer there"
	case err != nil:
		return fresh, fmt.Sprintf("skipped: couldn't re-check (%v)", err)
	case fresh.Decision.Class != item.Offer:
		reason := "no longer safe to offer"
		if len(fresh.Decision.Reasons) > 0 {
			reason = fresh.Decision.Reasons[0]
		}
		return fresh, "skipped: " + reason
	case fresh.Fingerprint != it.Fingerprint, fresh.Path != it.Path,
		fresh.Unregister != it.Unregister, fresh.Category != it.Category:
		return fresh, "skipped: changed since the report"
	}
	return fresh, ""
}

// act performs one chosen action. It returns an error only when the human
// can no longer be asked; every other problem skips the item.
func act(ctx context.Context, d Deps, ui UI, it item.Item, ans string, sum *Summary) error {
	if ans == "p" {
		ok, err := ui.Confirm(fmt.Sprintf("Permanently delete %s? This cannot be undone. Type 'delete' to confirm", it.Path), "delete")
		if err != nil {
			return err
		}
		if !ok {
			ui.Printf("  not confirmed; skipped\n")
			sum.Skipped++
			return nil
		}
	}

	fresh, why := recheck(ctx, d, it)
	if why != "" {
		ui.Printf("  %s\n", why)
		sum.Skipped++
		return nil
	}

	switch ans {
	case "u":
		if !fresh.Unregister {
			ui.Printf("  skipped: changed since the report\n")
			sum.Skipped++
			return nil
		}
		if err := d.RemoveWorktree(fresh.Repo, fresh.Path); err != nil {
			ui.Printf("  skipped: couldn't unregister (%v) — run `git worktree prune`\n", err)
			sum.Skipped++
			return nil
		}
		ui.Printf("  unregistered\n")
		sum.Unregistered++
		return nil
	case "p":
		expect, err := os.Lstat(fresh.Path)
		if errors.Is(err, os.ErrNotExist) {
			ui.Printf("  skipped: no longer there\n")
			sum.Skipped++
			return nil
		} else if err != nil {
			ui.Printf("  skipped: couldn't re-check (%v)\n", err)
			sum.Skipped++
			return nil
		}
		if err := d.DeletePermanently(fresh.Path, expect); err != nil {
			ui.Printf("  skipped: couldn't delete (%v)\n", err)
			sum.Skipped++
			return nil
		}
		ui.Printf("  permanently deleted\n")
		sum.Deleted++
		sum.BytesDeleted += fresh.Size
	case "d", "t":
		if fresh.Unregister {
			ui.Printf("  skipped: changed since the report\n")
			sum.Skipped++
			return nil
		}
		dest, err := d.Trash.Move(fresh.Path)
		if err != nil {
			if errors.Is(err, syscall.EXDEV) {
				ui.Printf("  skipped: this item is on another volume than the Trash; left in place\n")
			} else {
				ui.Printf("  skipped: couldn't move to Trash (%v)\n", err)
			}
			sum.Skipped++
			return nil
		}
		ui.Printf("  moved to Trash (%s)\n", dest)
		sum.Trashed++
		sum.BytesTrashed += fresh.Size
	default:
		sum.Skipped++
		return nil
	}

	if fresh.Category == item.Worktree {
		if err := d.RemoveWorktree(fresh.Repo, fresh.Path); err != nil {
			verb := "moved to Trash"
			if ans == "p" {
				verb = "deleted"
			}
			ui.Printf("  %s, but git still lists it: %v — run `git worktree prune`\n", verb, err)
		}
	}

	if len(fresh.Secrets) > 0 {
		a, err := ui.Choose("Was this a real secret? [y]es / [n]o / [?] not sure", []string{"y", "n", "?"})
		if err != nil {
			return err
		}
		switch a {
		case "y":
			sum.SecretReal++
		case "n":
			sum.SecretNotReal++
		default:
			sum.SecretUnsure++
		}
	}
	return nil
}

func finish(ui UI, d Deps, sum Summary) {
	if sum.Trashed > 0 {
		ui.Printf("\nMoved %d %s (%s) to the Trash — empty it to free the space.\n",
			sum.Trashed, noun(sum.Trashed), report.HumanSize(sum.BytesTrashed))
	}
	if sum.Deleted > 0 {
		ui.Printf("Permanently deleted %d %s (%s).\n", sum.Deleted, noun(sum.Deleted), report.HumanSize(sum.BytesDeleted))
	}
	if sum.Unregistered > 0 {
		ui.Printf("Unregistered %d %s.\n", sum.Unregistered, noun(sum.Unregistered))
	}
	if sum.Trashed+sum.Deleted+sum.Unregistered+sum.Kept+sum.SecretReal+sum.SecretNotReal+sum.SecretUnsure == 0 {
		return
	}
	err := stats.Update(d.StateDir, func(s *stats.Stats) {
		s.ItemsCleaned += sum.Trashed + sum.Deleted + sum.Unregistered
		s.ItemsKept += sum.Kept
		s.BytesTrashed += sum.BytesTrashed
		s.BytesDeleted += sum.BytesDeleted
		s.SecretReal += sum.SecretReal
		s.SecretNotReal += sum.SecretNotReal
		s.SecretUnsure += sum.SecretUnsure
	})
	if err != nil {
		ui.Printf("warning: couldn't update stats: %v\n", err)
	}
}

func noun(n int) string {
	if n == 1 {
		return "item"
	}
	return "items"
}
