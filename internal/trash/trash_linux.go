//go:build linux

package trash

import (
	"errors"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"os"
	"path/filepath"

	"github.com/Audarya07/dustpan/internal/env"
)

// freedesktopLayout is true on Linux: files/ and info/ under the Trash dir.
const freedesktopLayout = true

// ForUser returns the freedesktop Trash: $XDG_DATA_HOME/Trash, default ~/.local/share/Trash.
func ForUser(e env.Env) Trash {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" || !filepath.IsAbs(data) {
		data = filepath.Join(e.Home, ".local", "share")
	}
	return Trash{Dir: filepath.Join(data, "Trash"), now: e.Now}
}

// renameNoReplace renames from to to, failing with EEXIST instead of
// replacing an existing destination.
func renameNoReplace(from, to string) error {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS) {
		// Kernel/filesystem lacks RENAME_NOREPLACE; the O_EXCL .trashinfo
		// reservation still protects against compliant writers.
		return os.Rename(from, to)
	}
	return err
}

// ctimeOf returns the inode change time.
func ctimeOf(fi os.FileInfo) (time.Time, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)), true
}
