// Package report renders scan results for people (compact and details) and
// scripts (JSON), and caches the last result for the hooks.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Audarya07/dustpan/internal/item"
	"github.com/Audarya07/dustpan/internal/scan"
	"github.com/Audarya07/dustpan/internal/secrets"
)

// HomeDir is abbreviated to "~" in printed paths. Tests override it.
var HomeDir = func() string { h, _ := os.UserHomeDir(); return h }()

const (
	headlineIncomplete = "Still reading past sessions, so this report may be incomplete."
	rotateNote         = "Deleting the file doesn't undo a leak. Rotate the key."
	maxSecretLines     = 10
)

// HumanSize formats decimal units like Finder: 0 B, 96 KB, 318 MB, 3.1 GB.
func HumanSize(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		v /= 1000
		if v < 999.5 || u == "TB" {
			if v < 9.95 {
				return fmt.Sprintf("%.1f %s", v, u)
			}
			return fmt.Sprintf("%.0f %s", v, u)
		}
	}
	return ""
}

type painter bool

func (p painter) wrap(code, s string) string {
	if !p {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func (p painter) red(s string) string    { return p.wrap("31", s) }
func (p painter) yellow(s string) string { return p.wrap("33", s) }
func (p painter) green(s string) string  { return p.wrap("32", s) }
func (p painter) cyan(s string) string   { return p.wrap("36", s) }
func (p painter) dim(s string) string    { return p.wrap("2", s) }

func tilde(path string) string {
	if HomeDir != "" && (path == HomeDir || strings.HasPrefix(path, HomeDir+"/")) {
		return "~" + strings.TrimPrefix(path, HomeDir)
	}
	return path
}

// secretLabel maps a gitleaks rule ID to a short human label.
func secretLabel(rule string) string {
	switch {
	case rule == "aws-access-token":
		return "AWS key"
	case rule == "generic-api-key":
		return "API key"
	case rule == "private-key":
		return "private key"
	case strings.HasPrefix(rule, "github-"):
		return "GitHub token"
	case strings.HasPrefix(rule, "slack-"):
		return "Slack token"
	case strings.HasPrefix(rule, "gcp-"):
		return "GCP key"
	case rule == "jwt":
		return "JWT"
	}
	return rule
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func pad(s string, w int) string {
	if n := len([]rune(s)); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func padLeft(s string, w int) string {
	if n := len([]rune(s)); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}

// secretLine is one rendered line of the secrets section.
type secretLine struct{ a, b, c string }

func secretLines(items []item.Item) []secretLine {
	var out []secretLine
	for _, it := range items {
		byFile := map[string][]secrets.Finding{}
		var files []string
		for _, f := range it.Secrets {
			if _, ok := byFile[f.Path]; !ok {
				files = append(files, f.Path)
			}
			byFile[f.Path] = append(byFile[f.Path], f)
		}
		sort.Strings(files)
		for _, file := range files {
			fs := byFile[file]
			full := tilde(filepath.Join(it.Path, file))
			if len(fs) == 1 {
				out = append(out, secretLine{fs[0].Masked, secretLabel(fs[0].RuleID), full})
			} else {
				out = append(out, secretLine{fmt.Sprintf("%s ×%d", filepath.Base(file), len(fs)), "values", full})
			}
		}
	}
	return out
}

func hasSecrets(it item.Item) bool { return len(it.Secrets) > 0 }

// SecretPath is where a finding sits as printed in item-scoped lines: the
// path relative to the item, or the item's own path when the item is the
// scanned file itself (Finding.Path is empty then).
func SecretPath(it item.Item, f secrets.Finding) string {
	if f.Path == "" {
		return tilde(it.Path)
	}
	return f.Path
}

var categoryLabels = map[item.Category]string{
	item.Worktree:   "Abandoned worktrees",
	item.Transcript: "Old transcripts & logs",
	item.Job:        "Finished / stale agent jobs",
	item.Scratch:    "Scratch files in /tmp",
}

func categoryNote(c item.Category, n int) string {
	switch c {
	case item.Worktree:
		return fmt.Sprintf("%d, no unsaved work", n)
	case item.Transcript:
		return "older than 30d"
	case item.Job:
		return plural(n, "folder", "folders")
	case item.Scratch:
		return plural(n, "file", "files")
	}
	return fmt.Sprintf("%d", n)
}

// RenderCompact prints the §8 report.
func RenderCompact(w io.Writer, r scan.Result, color bool) {
	p := painter(color)
	var offers, left, secret []item.Item
	for _, it := range r.Items {
		if it.Decision.DetailsOnly { // --details only, secrets included
			continue
		}
		switch it.Decision.Class {
		case item.Offer:
			offers = append(offers, it)
		case item.LeftAlone:
			left = append(left, it)
		}
		if hasSecrets(it) && (it.Decision.Class == item.Offer || it.Decision.Class == item.LeftAlone) {
			secret = append(secret, it)
		}
	}
	if len(offers) == 0 && len(left) == 0 && len(secret) == 0 {
		fmt.Fprintf(w, "Nothing left behind. Last checked %s.\n", r.GeneratedAt.Format("2006-01-02 15:04"))
		return
	}

	var total int64
	for _, it := range offers {
		total += it.Size
	}
	if len(offers) > 0 {
		fmt.Fprintf(w, "Your agents left %s behind in %s.", HumanSize(total), plural(len(offers), "place", "places"))
	} else {
		fmt.Fprint(w, "Nothing to clean up right now.")
	}
	if len(secret) > 0 {
		fmt.Fprintf(w, "  %d contain secrets.", len(secret))
	}
	fmt.Fprintln(w)
	if r.Incomplete {
		fmt.Fprintln(w, headlineIncomplete)
	}

	if len(secret) > 0 {
		fmt.Fprintf(w, "\n%s Secrets in leftovers (rotate these first)\n", p.red("■"))
		lines := secretLines(secret)
		shown := lines
		if len(shown) > maxSecretLines {
			shown = shown[:maxSecretLines]
		}
		wa, wb := 0, 0
		for _, l := range shown {
			wa = max(wa, len([]rune(l.a)))
			wb = max(wb, len([]rune(l.b)))
		}
		for _, l := range shown {
			fmt.Fprintf(w, "  %s  %s  %s\n", pad(l.a, wa), pad(l.b, wb), l.c)
		}
		if extra := len(lines) - len(shown); extra > 0 {
			fmt.Fprintf(w, "  … and %d more (see %s)\n", extra, p.cyan("report --details"))
		}
		fmt.Fprintf(w, "  %s\n", rotateNote)
	}

	if len(offers) > 0 {
		fmt.Fprintf(w, "\n%s %s\n", p.yellow("■"), pad("Dead agent data", 37)+HumanSize(total))
		type group struct {
			cat  item.Category
			size int64
			n    int
		}
		groups := map[item.Category]*group{}
		for _, it := range offers {
			g := groups[it.Category]
			if g == nil {
				g = &group{cat: it.Category}
				groups[it.Category] = g
			}
			g.size += it.Size
			g.n++
		}
		var gs []*group
		for _, g := range groups {
			gs = append(gs, g)
		}
		sort.Slice(gs, func(i, j int) bool {
			if gs[i].size != gs[j].size {
				return gs[i].size > gs[j].size
			}
			return gs[i].cat < gs[j].cat
		})
		lw := 0
		for _, g := range gs {
			lw = max(lw, len([]rune(labelFor(g.cat))))
		}
		for _, g := range gs {
			fmt.Fprintf(w, "  %s   %s   %s\n", pad(labelFor(g.cat), lw), padLeft(HumanSize(g.size), 6), categoryNote(g.cat, g.n))
		}
	}

	if len(left) > 0 {
		fmt.Fprintf(w, "\n%s Still in use, left alone\n", p.green("■"))
		type group struct {
			reason string
			cat    item.Category
			n      int
			branch string
			mixed  bool
		}
		var order []string
		groups := map[string]*group{}
		for _, it := range left {
			reason := "kept for safety"
			if len(it.Decision.Reasons) > 0 {
				reason = it.Decision.Reasons[0]
			}
			key := string(it.Category) + "\x00" + reason
			g := groups[key]
			if g == nil {
				g = &group{reason: reason, cat: it.Category, branch: it.Branch}
				groups[key] = g
				order = append(order, key)
			} else if g.branch != it.Branch {
				g.mixed = true
			}
			g.n++
		}
		lefts := make([]string, len(order))
		lw := 0
		for i, k := range order {
			g := groups[k]
			s := leftNoun(g.cat, g.n)
			if g.branch != "" && !g.mixed {
				s += " on " + g.branch
			}
			lefts[i] = s
			lw = max(lw, len([]rune(s)))
		}
		for i, k := range order {
			fmt.Fprintf(w, "  %s   %s\n", pad(lefts[i], lw), groups[k].reason)
		}
	}

	fmt.Fprintf(w, "\n→ %s   → %s\n", p.cyan("clean"), p.cyan("report --details"))
}

func labelFor(c item.Category) string {
	if l, ok := categoryLabels[c]; ok {
		return l
	}
	return string(c)
}

func leftNoun(c item.Category, n int) string {
	switch c {
	case item.Worktree:
		return plural(n, "worktree", "worktrees")
	case item.Job:
		return plural(n, "job folder", "job folders")
	case item.Scratch:
		return plural(n, "scratch file", "scratch files")
	case item.Transcript:
		return plural(n, "transcript", "transcripts")
	}
	return plural(n, string(c), string(c)+"s")
}

// age renders the time since t as 5m, 3h or 12d; "unknown" for a zero time.
func age(now, t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func attribution(e item.Evidence) string {
	if !e.Strong {
		return "best guess (no receipt)"
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

// RenderDetails prints every item, including those the compact view hides.
func RenderDetails(w io.Writer, r scan.Result, color bool) {
	p := painter(color)
	if len(r.Items) == 0 {
		fmt.Fprintf(w, "Nothing left behind. Last checked %s.\n", r.GeneratedAt.Format("2006-01-02 15:04"))
		return
	}
	fmt.Fprintf(w, "%s\n", plural(len(r.Items), "item", "items"))
	if r.Incomplete {
		fmt.Fprintln(w, headlineIncomplete)
	}
	for _, it := range r.Items {
		fmt.Fprintf(w, "\n%s  %s  %s  %s\n", p.cyan(it.ID), it.Category, HumanSize(it.Size), age(r.GeneratedAt, it.Newest)+" old")
		fmt.Fprintf(w, "  path:     %s\n", tilde(it.Path))
		if it.Repo != "" || it.Branch != "" {
			fmt.Fprintf(w, "  repo:     %s %s\n", tilde(it.Repo), it.Branch)
		}
		fmt.Fprintf(w, "  made by:  %s\n", attribution(it.Evidence))
		verdict := string(it.Decision.Class)
		switch it.Decision.Class {
		case item.Offer:
			verdict = p.yellow("offer")
		case item.LeftAlone:
			verdict = p.green("left alone")
		}
		if it.Decision.DetailsOnly {
			verdict += " (details only)"
		}
		if len(it.Decision.Reasons) > 0 {
			verdict += ": " + strings.Join(it.Decision.Reasons, "; ")
		}
		fmt.Fprintf(w, "  class:    %s\n", verdict)
		if it.DeadReason != "" {
			fmt.Fprintf(w, "  why dead: %s\n", it.DeadReason)
		}
		for _, f := range it.Secrets {
			fmt.Fprintf(w, "  %s %s  %s  %s:%d\n", p.red("secret:"), f.Masked, secretLabel(f.RuleID), SecretPath(it, f), f.Line)
		}
		if it.SecretsPartial {
			fmt.Fprintln(w, "  partially scanned, more secrets may exist")
		}
		if it.Category == item.Transcript {
			fmt.Fprintln(w, "  you can't resume this session after deleting it")
		}
	}
}

// WriteJSON writes the result as indented JSON. Secrets in it are already
// masked: findings never carry a raw value.
func WriteJSON(w io.Writer, r scan.Result) error {
	if r.Items == nil {
		r.Items = []item.Item{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
