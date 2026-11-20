package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/Audarya07/agent-receipts/internal/clean"
	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/gitwt"
	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/scan"
	"github.com/Audarya07/agent-receipts/internal/trash"
)

// termUI is the real terminal: prompts on out, answers from in.
type termUI struct {
	in  *bufio.Reader
	out io.Writer
}

func (u termUI) Printf(format string, a ...any) { fmt.Fprintf(u.out, format, a...) }

func (u termUI) readLine() (string, error) {
	line, err := u.in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (u termUI) Choose(prompt string, keys []string) (string, error) {
	for {
		fmt.Fprintf(u.out, "  %s: ", prompt)
		line, err := u.readLine()
		if err != nil {
			return "", err
		}
		line = strings.ToLower(line)
		for _, k := range keys {
			if line == k {
				return k, nil
			}
		}
		fmt.Fprintf(u.out, "  please answer one of: %s\n", strings.Join(keys, ", "))
	}
}

func (u termUI) Confirm(prompt, word string) (bool, error) {
	fmt.Fprintf(u.out, "  %s: ", prompt)
	line, err := u.readLine()
	if err != nil {
		return false, err
	}
	return line == word, nil
}

func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func runClean(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "%s: clean takes no arguments\n", env.AppName)
		return 2
	}
	e := env.FromOS()
	if err := checkEnv(e); err != nil { // before anything is scanned or touched
		fmt.Fprintf(stderr, "%s: clean: %v\n", env.AppName, err)
		return 2
	}
	if !isTTY() { // before anything is scanned or touched
		fmt.Fprintln(stderr, clean.ErrNoTerminal)
		return 2
	}
	if err := e.EnsureStateDir(); err != nil {
		fmt.Fprintf(stderr, "%s: clean: %v\n", env.AppName, err)
		return 1
	}
	newCtx := func(ctx context.Context) (scan.Context, error) { return scan.NewContext(ctx, e) }
	d := clean.Deps{
		Scan: func(ctx context.Context) (scan.Result, error) {
			sctx, cancel := context.WithTimeout(ctx, scanBudget)
			defer cancel()
			c, err := newCtx(sctx)
			if err != nil {
				return scan.Result{}, err
			}
			return scan.Run(sctx, c)
		},
		// A fresh context per re-check: the process snapshot must be current.
		Recheck: func(ctx context.Context, it item.Item) (item.Item, error) {
			c, err := newCtx(ctx)
			if err != nil {
				return item.Item{}, err
			}
			return scan.Recheck(ctx, c, it)
		},
		Trash:             trash.ForUser(e),
		DeletePermanently: trash.DeletePermanently,
		RemoveWorktree: func(repo, path string) error {
			return gitwt.Remove(context.Background(), repo, path) // per-call timeout inside
		},
		StateDir: e.StateDir,
		IsTTY:    isTTY,
		Now:      e.Now,
	}
	_, err := clean.Run(context.Background(), d, termUI{in: bufio.NewReader(os.Stdin), out: stdout})
	switch {
	case errors.Is(err, clean.ErrNoTerminal):
		fmt.Fprintln(stderr, err)
		return 2
	case errors.Is(err, io.EOF):
		fmt.Fprintf(stderr, "%s: input closed; stopped\n", env.AppName)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "%s: clean: %v\n", env.AppName, err)
		return 1
	}
	return 0
}
