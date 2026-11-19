//go:build darwin

package trash

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"path/filepath"

	"github.com/Audarya07/agent-receipts/internal/env"
)

// freedesktopLayout is false on macOS: Finder owns ~/.Trash and there is no info/ directory.
const freedesktopLayout = false

// ForUser returns the user's macOS Trash (~/.Trash).
func ForUser(e env.Env) Trash {
	return Trash{Dir: filepath.Join(e.Home, ".Trash"), now: e.Now}
}

// renameNoReplace renames from to to, failing with EEXIST instead of
// replacing an existing destination.
func renameNoReplace(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if errors.Is(err, syscall.ENOTSUP) {
		return fmt.Errorf("trash: filesystem does not support exclusive rename: %w", err)
	}
	return err
}

// ctimeOf returns the inode change time.
func ctimeOf(fi os.FileInfo) (time.Time, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(st.Ctimespec.Sec), int64(st.Ctimespec.Nsec)), true
}
