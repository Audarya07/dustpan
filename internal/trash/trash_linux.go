//go:build linux

package trash

import (
	"os"
	"path/filepath"

	"github.com/Audarya07/agent-receipts/internal/env"
)

// freedesktopLayout is true on Linux: files/ and info/ under the Trash dir.
const freedesktopLayout = true

// ForUser returns the freedesktop Trash: $XDG_DATA_HOME/Trash, default ~/.local/share/Trash.
func ForUser(e env.Env) Trash {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" || !filepath.IsAbs(data) {
		data = filepath.Join(e.Home, ".local", "share")
	}
	return Trash{Dir: filepath.Join(data, "Trash")}
}
