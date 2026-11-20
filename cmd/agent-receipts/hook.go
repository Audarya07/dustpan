package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/hook"
	"github.com/Audarya07/agent-receipts/internal/proc"
	"github.com/Audarya07/agent-receipts/internal/receipts"
)

// runHook never writes to stderr and always returns 0.
func runHook(args []string, stdin io.Reader, stdout io.Writer) (code int) {
	defer func() { _ = recover(); code = 0 }()
	time.AfterFunc(450*time.Millisecond, func() { os.Exit(0) })
	if len(args) == 0 {
		return 0
	}
	e := env.FromOS()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_ = hook.Handle(ctx, hook.Deps{
		Env: e, Store: receipts.Open(e.StateDir), Procs: proc.TakeProcsOnly,
		ParentPID: os.Getppid(), SpawnRefresh: hook.SpawnRefresh,
	}, args[0], stdin, stdout)
	return 0
}
