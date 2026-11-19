//go:build darwin

package trash

import (
	"path/filepath"

	"github.com/Audarya07/agent-receipts/internal/env"
)

// freedesktopLayout is false on macOS: Finder owns ~/.Trash and there is no info/ directory.
const freedesktopLayout = false

// ForUser returns the user's macOS Trash (~/.Trash).
func ForUser(e env.Env) Trash {
	return Trash{Dir: filepath.Join(e.Home, ".Trash")}
}
