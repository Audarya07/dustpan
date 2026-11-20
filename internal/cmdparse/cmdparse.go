// Package cmdparse extracts the filesystem paths a shell command creates.
//
// Paths are returned literally as resolved from the command text and the
// tracked working directory (filepath.Clean/Join); symlinks are not resolved.
package cmdparse

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/Audarya07/dustpan/internal/receipts"
)

// Output is a path a command creates.
type Output struct {
	Path  string
	IsDir bool
	Via   string
}

// OutputPaths returns the paths created by command, run from cwd. It returns
// nil for anything it cannot parse and never panics.
func OutputPaths(command, cwd string) (out []Output) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	w := &walker{cwd: cwd}
	for _, st := range f.Stmts {
		w.stmt(st)
	}
	return w.out
}

type walker struct {
	cwd string // "" means unknown (relative paths are skipped)
	out []Output
}

func (w *walker) add(p string, isDir bool, via string) {
	if r, ok := w.resolve(p); ok {
		w.out = append(w.out, Output{r, isDir, via})
	}
}

func (w *walker) resolve(p string) (string, bool) {
	if p == "" || strings.HasPrefix(p, "/dev/") || p == "/dev" {
		return "", false
	}
	if strings.HasPrefix(p, "~") || receipts.HasDotDot(p) {
		return "", false
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), true
	}
	if w.cwd == "" || receipts.HasDotDot(w.cwd) {
		return "", false
	}
	return filepath.Join(w.cwd, p), true
}

func (w *walker) stmt(st *syntax.Stmt) {
	if st == nil {
		return
	}
	w.cmd(st.Cmd)
	for _, r := range st.Redirs {
		// Only truncating redirects create; an append (>>, &>>) usually
		// targets a file that already exists, possibly the user's own.
		switch r.Op {
		case syntax.RdrOut, syntax.RdrAll:
			if s, ok := literal(r.Word); ok {
				w.add(s, false, ">")
			}
		}
	}
}

func (w *walker) cmd(c syntax.Command) {
	switch c := c.(type) {
	case *syntax.CallExpr:
		w.call(c)
	case *syntax.BinaryCmd:
		w.stmt(c.X)
		w.stmt(c.Y)
	case *syntax.Block:
		for _, s := range c.Stmts {
			w.stmt(s)
		}
	case *syntax.Subshell:
		saved := w.cwd // cd inside a subshell does not leak
		for _, s := range c.Stmts {
			w.stmt(s)
		}
		w.cwd = saved
	}
}

// literal returns the word's text if it is fully static: no expansion,
// command substitution, glob or escape.
func literal(wd *syntax.Word) (string, bool) {
	if wd == nil {
		return "", false
	}
	var sb strings.Builder
	for _, p := range wd.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(p.Value, "*?[\\") {
				return "", false
			}
			sb.WriteString(p.Value)
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			sb.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return "", false
			}
			for _, q := range p.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok || strings.Contains(l.Value, "\\") {
					return "", false
				}
				sb.WriteString(l.Value)
			}
		default:
			return "", false
		}
	}
	return sb.String(), true
}

// args holds the command's words; unresolvable ones are ok=false.
type arg struct {
	s  string
	ok bool
}

func (w *walker) call(c *syntax.CallExpr) {
	if len(c.Args) == 0 {
		return
	}
	args := make([]arg, len(c.Args))
	for i, a := range c.Args {
		s, ok := literal(a)
		args[i] = arg{s, ok}
	}
	name := args[0]
	if !name.ok {
		return
	}
	rest := args[1:]
	switch filepath.Base(name.s) {
	case "cd":
		w.cd(rest)
		return
	case "git":
		w.gitWorktree(rest)
	case "tee":
		if teeAppends(rest) {
			break
		}
		for _, a := range positional(rest) {
			if a.ok {
				w.add(a.s, false, "tee")
			}
		}
	case "cp", "mv":
		if pos := positional(rest); len(pos) >= 2 {
			if l := pos[len(pos)-1]; l.ok {
				w.add(l.s, strings.HasSuffix(l.s, "/"), filepath.Base(name.s))
			}
		}
	case "mkdir":
		for _, a := range positional(rest) {
			if a.ok {
				w.add(a.s, true, "mkdir")
			}
		}
	}
	w.dashO(rest)
}

// teeAppends reports whether tee was given -a/--append (possibly combined,
// e.g. -ia): appending creates nothing new. An unresolvable word before "--"
// might expand to -a, so it counts as appending (fail closed).
func teeAppends(rest []arg) bool {
	for _, a := range rest {
		if !a.ok {
			return true
		}
		if a.s == "--" {
			return false
		}
		if a.s == "--append" || (strings.HasPrefix(a.s, "-") && !strings.HasPrefix(a.s, "--") && strings.Contains(a.s, "a")) {
			return true
		}
	}
	return false
}

func (w *walker) cd(rest []arg) {
	pos := positional(rest)
	if len(pos) == 0 || !pos[0].ok || pos[0].s == "-" {
		w.cwd = ""
		return
	}
	if r, ok := w.resolve(pos[0].s); ok {
		w.cwd = r
	} else {
		w.cwd = ""
	}
}

// positional returns the non-flag arguments (after "--" everything counts).
func positional(rest []arg) []arg {
	var pos []arg
	dd := false
	for _, a := range rest {
		if !dd && a.ok && a.s == "--" {
			dd = true
			continue
		}
		if !dd && a.ok && strings.HasPrefix(a.s, "-") && a.s != "-" {
			continue
		}
		pos = append(pos, a)
	}
	return pos
}

func (w *walker) gitWorktree(rest []arg) {
	saved := w.cwd
	i := 0
	// global options before the subcommand
loop:
	for i < len(rest) {
		a := rest[i]
		if !a.ok {
			return
		}
		switch {
		case a.s == "-C":
			if i+1 >= len(rest) || !rest[i+1].ok {
				return
			}
			if r, ok := w.resolve(rest[i+1].s); ok {
				w.cwd = r
			} else {
				return
			}
			i += 2
		case a.s == "-c":
			i += 2
		case strings.HasPrefix(a.s, "-"):
			i++
		default:
			break loop
		}
	}
	defer func() { w.cwd = saved }()
	if i+1 >= len(rest) || !rest[i].ok || rest[i].s != "worktree" || !rest[i+1].ok || rest[i+1].s != "add" {
		return
	}
	for i += 2; i < len(rest); i++ {
		a := rest[i]
		if !a.ok {
			return
		}
		switch {
		case a.s == "-b" || a.s == "-B" || a.s == "--reason":
			i++
		case strings.HasPrefix(a.s, "-"):
		default:
			w.add(a.s, true, "git worktree add")
			return
		}
	}
}

// dashO handles -o <path>, -o<path>, --output <path>, --output=<path>.
// Values that look like key=value options (e.g. ssh -o Foo=bar) are skipped.
func (w *walker) dashO(rest []arg) {
	for i, a := range rest {
		if !a.ok {
			continue
		}
		var v string
		var have bool
		switch {
		case a.s == "-o" || a.s == "--output":
			if i+1 < len(rest) && rest[i+1].ok {
				v, have = rest[i+1].s, true
			}
		case strings.HasPrefix(a.s, "--output="):
			v, have = strings.TrimPrefix(a.s, "--output="), true
		case strings.HasPrefix(a.s, "-o") && !strings.HasPrefix(a.s, "--") && strings.Contains(a.s, "/"):
			v, have = a.s[2:], true
		}
		if !have || v == "" || strings.HasPrefix(v, "-") {
			continue
		}
		if strings.Contains(v, "=") && !strings.Contains(v, "/") {
			continue
		}
		w.add(v, false, "-o")
	}
}
