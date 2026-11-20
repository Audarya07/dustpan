package scan

import (
	"os"
	"syscall"
	"time"
)

// fileBirthTime returns when path itself (never a symlink's target) was
// created, from the Birthtimespec of lstat.
func fileBirthTime(path string) (time.Time, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || (st.Birthtimespec.Sec == 0 && st.Birthtimespec.Nsec == 0) {
		return time.Time{}, false
	}
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec), true
}
