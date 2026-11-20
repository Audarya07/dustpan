package scan

import (
	"time"

	"golang.org/x/sys/unix"
)

// fileBirthTime returns when path itself (never a symlink's target) was
// created, via statx(STATX_BTIME). Filesystems without a birth time report
// false.
func fileBirthTime(path string) (time.Time, bool) {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &stx); err != nil {
		return time.Time{}, false
	}
	if stx.Mask&unix.STATX_BTIME == 0 {
		return time.Time{}, false
	}
	return time.Unix(stx.Btime.Sec, int64(stx.Btime.Nsec)), true
}
