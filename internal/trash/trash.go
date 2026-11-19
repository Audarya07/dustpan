// Package trash moves items to the user's Trash and performs guarded
// permanent deletion. It is the only code that moves or deletes the
// engineer's files, so it is deliberately conservative: rename only (never
// copy-then-delete), never follow symlinks, and permanent deletion refuses
// unless the path is still exactly the thing that was checked.
package trash

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Trash is a Trash directory. On macOS Dir is ~/.Trash; on Linux it is the
// freedesktop Trash root containing files/ and info/.
type Trash struct {
	Dir string

	// now overrides the clock (tests); nil means time.Now.
	now func() time.Time
	// beforeRename is a test hook called just before the exclusive rename.
	beforeRename func(dest string)
	// forceFreedesktop enables the files/+info/ layout on any OS (tests).
	forceFreedesktop bool
}

// WithClock returns a copy of t that uses now for collision suffixes and
// DeletionDate.
func (t Trash) WithClock(now func() time.Time) Trash {
	t.now = now
	return t
}

func (t Trash) freedesktop() bool { return freedesktopLayout || t.forceFreedesktop }

func (t Trash) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t Trash) filesDir() string {
	if t.freedesktop() {
		return filepath.Join(t.Dir, "files")
	}
	return t.Dir
}

func devOf(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true //nolint:unconvert // Dev is int32 on darwin, uint64 on linux
}

// Available reports whether path can be moved to this Trash by rename: the
// Trash exists (or, with the freedesktop layout, can be created) and lives on
// the same device as path's parent directory.
func (t Trash) Available(path string) bool {
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return false
	}
	pdev, ok := devOf(parent)
	if !ok {
		return false
	}
	probe := t.Dir
	if t.freedesktop() {
		// Walk up to the nearest existing ancestor; MkdirAll would create the rest.
		for {
			if _, err := os.Stat(probe); err == nil {
				break
			}
			next := filepath.Dir(probe)
			if next == probe {
				return false
			}
			probe = next
		}
	}
	tfi, err := os.Stat(probe)
	if err != nil || !tfi.IsDir() {
		return false
	}
	tdev, ok := devOf(tfi)
	return ok && tdev == pdev
}

// Move renames path into the Trash and returns its new location. A symlink is
// moved as the link itself. A cross-device move fails with an error wrapping
// syscall.EXDEV; nothing is ever copied.
func (t Trash) Move(path string) (string, error) {
	if path == "" || filepath.Base(path) == "/" || filepath.Base(path) == "." {
		return "", fmt.Errorf("trash: refusing to move %q", path)
	}
	if _, err := os.Lstat(path); err != nil {
		return "", err
	}
	if t.freedesktop() {
		return t.moveFreedesktop(path)
	}
	// macOS: Finder owns ~/.Trash; never create it.
	if fi, err := os.Stat(t.Dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("trash: %s does not exist", t.Dir)
	}
	name := filepath.Base(path)
	for i := 0; ; i++ {
		dest := filepath.Join(t.Dir, t.candidate(name, i))
		if _, err := os.Lstat(dest); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if t.beforeRename != nil {
			t.beforeRename(dest)
		}
		if err := renameNoReplace(path, dest); err != nil {
			if errors.Is(err, syscall.EEXIST) {
				continue // lost a race for this name; try the next one
			}
			return "", fmt.Errorf("trash: move %s: %w", path, err)
		}
		return dest, nil
	}
}

// candidate returns the name to try on attempt i: plain, then with a
// timestamp, then with a timestamp and counter.
func (t Trash) candidate(name string, i int) string {
	switch i {
	case 0:
		return name
	case 1:
		return name + " " + t.clock().Format("20060102-150405")
	default:
		return fmt.Sprintf("%s %s-%d", name, t.clock().Format("20060102-150405"), i-1)
	}
}

func (t Trash) moveFreedesktop(path string) (string, error) {
	files, info := filepath.Join(t.Dir, "files"), filepath.Join(t.Dir, "info")
	for _, d := range []string{files, info} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	u := url.URL{Path: abs}
	body := fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n", u.EscapedPath(), t.clock().Format("2006-01-02T15:04:05"))
	name := filepath.Base(path)
	for i := 0; ; i++ {
		cand := t.candidate(name, i)
		dest := filepath.Join(files, cand)
		if _, err := os.Lstat(dest); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// O_EXCL on the .trashinfo reserves the name (freedesktop spec).
		infoPath := filepath.Join(info, cand+".trashinfo")
		f, err := os.OpenFile(infoPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", err
		}
		_, werr := f.WriteString(body)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			os.Remove(infoPath)
			return "", errors.Join(werr, cerr)
		}
		if t.beforeRename != nil {
			t.beforeRename(dest)
		}
		if err := renameNoReplace(path, dest); err != nil {
			os.Remove(infoPath)
			if errors.Is(err, syscall.EEXIST) {
				continue // someone took the name; try the next one
			}
			return "", fmt.Errorf("trash: move %s: %w", path, err)
		}
		return dest, nil
	}
}

// DeletePermanently removes path only if it is still the same file that was
// inspected (expect). If path is a symlink only the link is removed; symlinks
// inside a directory tree are removed, never followed.
func DeletePermanently(path string, expect os.FileInfo) error {
	if expect == nil {
		return errors.New("trash: no expected file info; refusing permanent delete")
	}
	now, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(now, expect) {
		return fmt.Errorf("trash: %s changed since it was checked; refusing to delete", path)
	}
	// SameFile alone is unreliable under inode reuse, so also require the same
	// file type and ctime. The Lstat -> remove window below is not atomic;
	// this narrows it, it cannot close it.
	if now.Mode().Type() != expect.Mode().Type() {
		return fmt.Errorf("trash: %s changed type since it was checked; refusing to delete", path)
	}
	nc, ok1 := ctimeOf(now)
	ec, ok2 := ctimeOf(expect)
	if !ok1 || !ok2 || !nc.Equal(ec) {
		return fmt.Errorf("trash: %s was modified since it was checked; refusing to delete", path)
	}
	if now.Mode()&os.ModeSymlink != 0 {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}
