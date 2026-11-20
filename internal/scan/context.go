package scan

import (
	"context"
	"fmt"

	"github.com/Audarya07/agent-receipts/internal/config"
	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/proc"
	"github.com/Audarya07/agent-receipts/internal/receipts"
	"github.com/Audarya07/agent-receipts/internal/secrets"
	"github.com/Audarya07/agent-receipts/internal/trash"
)

// NewContext loads everything a scan needs from the machine: config, the
// receipt index (built from canonicalized receipts, so symlinked paths such
// as macOS /tmp still match), a process snapshot, the Trash and the secret
// scanner. A failed process snapshot is kept in ProcErr, not returned: the
// safety engine treats it as "unknown" and keeps items.
func NewContext(ctx context.Context, e env.Env) (Context, error) {
	cfg, err := config.Load(e.ConfigDir)
	if err != nil {
		return Context{}, fmt.Errorf("load config: %w", err)
	}
	rs, err := receipts.Open(e.StateDir).Load()
	if err != nil {
		return Context{}, fmt.Errorf("load receipts: %w", err)
	}
	sc, err := secrets.New()
	if err != nil {
		return Context{}, fmt.Errorf("secret scanner: %w", err)
	}
	snap, procErr := proc.Take(ctx)
	return Context{
		Env:     e,
		Config:  cfg,
		Index:   receipts.BuildIndex(CanonReceipts(e, rs)),
		Proc:    snap,
		ProcErr: procErr,
		Trash:   trash.ForUser(e),
		Secrets: sc,
	}, nil
}
