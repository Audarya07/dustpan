package trash

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	must(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 11, 20, 9, 8, 7, 0, time.Local) }
}

func TestMoveAndCollision(t *testing.T) {
	root := t.TempDir()
	tr := Trash{Dir: filepath.Join(root, "Trash")}.WithClock(fixedClock())
	must(t, os.MkdirAll(tr.Dir, 0o700))
	a := writeFile(t, root, "my-bin", "x")
	d1, err := tr.Move(a)
	must(t, err)
	b := writeFile(t, root, "my-bin", "y")
	d2, err := tr.Move(b)
	must(t, err)
	c := writeFile(t, root, "my-bin", "z")
	d3, err := tr.Move(c)
	must(t, err)
	if d1 == d2 || d2 == d3 || d1 == d3 {
		t.Fatalf("collision overwrote: %s %s %s", d1, d2, d3)
	}
	if !strings.HasSuffix(d2, "my-bin 20261120-090807") {
		t.Fatalf("collision name %q", d2)
	}
	if got, _ := os.ReadFile(d1); string(got) != "x" {
		t.Fatalf("first file clobbered: %q", got)
	}
	if exists(a) {
		t.Fatal("source still present")
	}
}

func TestMoveDoesNotCreateMissingMacTrash(t *testing.T) {
	root := t.TempDir()
	tr := Trash{Dir: filepath.Join(root, ".Trash")}
	if freedesktopLayout {
		t.Skip("macOS layout only")
	}
	if _, err := tr.Move(writeFile(t, root, "f", "x")); err == nil {
		t.Fatal("expected error")
	}
	if exists(tr.Dir) {
		t.Fatal("Trash was created")
	}
}

func TestMoveSymlinkMovesLinkOnly(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	must(t, os.Mkdir(outside, 0o700))
	keep := writeFile(t, outside, "precious", "data")
	tr := Trash{Dir: filepath.Join(root, "Trash")}
	must(t, os.MkdirAll(tr.Dir, 0o700))
	link := filepath.Join(root, "link")
	must(t, os.Symlink(outside, link))
	dest, err := tr.Move(link)
	must(t, err)
	if !exists(keep) {
		t.Fatal("symlink target content was touched")
	}
	fi, err := os.Lstat(dest)
	must(t, err)
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("moved item is not the link")
	}
	if exists(link) {
		t.Fatal("link still at source")
	}
}

func TestMoveFreedesktopWritesInfoAndCollides(t *testing.T) {
	root := t.TempDir()
	tr := Trash{Dir: filepath.Join(root, "Trash"), forceFreedesktop: true}.WithClock(fixedClock())
	src := filepath.Join(root, "dir with space")
	must(t, os.Mkdir(src, 0o700))
	d1, err := tr.Move(src)
	must(t, err)
	if d1 != filepath.Join(tr.Dir, "files", "dir with space") {
		t.Fatalf("dest %s", d1)
	}
	b, err := os.ReadFile(filepath.Join(tr.Dir, "info", "dir with space.trashinfo"))
	must(t, err)
	want := "[Trash Info]\nPath=" + strings.ReplaceAll(src, " ", "%20") + "\nDeletionDate=2026-11-20T09:08:07\n"
	if string(b) != want {
		t.Fatalf("info:\n%q\nwant\n%q", b, want)
	}
	must(t, os.Mkdir(src, 0o700))
	d2, err := tr.Move(src)
	must(t, err)
	if d2 == d1 || !exists(d1) || !exists(d2) {
		t.Fatalf("collision handling: %s %s", d1, d2)
	}
	if !exists(filepath.Join(tr.Dir, "info", filepath.Base(d2)+".trashinfo")) {
		t.Fatal("missing info for collided name")
	}
}

func TestMoveFreedesktopRespectsReservedInfoName(t *testing.T) {
	root := t.TempDir()
	tr := Trash{Dir: filepath.Join(root, "Trash"), forceFreedesktop: true}.WithClock(fixedClock())
	must(t, os.MkdirAll(filepath.Join(tr.Dir, "info"), 0o700))
	// An orphan .trashinfo already reserves the plain name.
	writeFile(t, filepath.Join(tr.Dir, "info"), "f.trashinfo", "old")
	dest, err := tr.Move(writeFile(t, root, "f", "x"))
	must(t, err)
	if filepath.Base(dest) == "f" {
		t.Fatal("reused a reserved name")
	}
	if b, _ := os.ReadFile(filepath.Join(tr.Dir, "info", "f.trashinfo")); string(b) != "old" {
		t.Fatal("existing info overwritten")
	}
}

func TestMoveFreedesktopRemovesInfoWhenRenameFails(t *testing.T) {
	root := t.TempDir()
	tr := Trash{Dir: filepath.Join(root, "Trash"), forceFreedesktop: true}
	// Source's parent is unwritable so rename fails after the info is written.
	parent := filepath.Join(root, "ro")
	must(t, os.Mkdir(parent, 0o700))
	src := writeFile(t, parent, "f", "x")
	must(t, os.Chmod(parent, 0o500))
	defer os.Chmod(parent, 0o700)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if _, err := tr.Move(src); err == nil {
		t.Fatal("expected failure")
	}
	if ents, _ := os.ReadDir(filepath.Join(tr.Dir, "info")); len(ents) != 0 {
		t.Fatalf("leftover info files: %v", ents)
	}
}

func TestAvailable(t *testing.T) {
	root := t.TempDir()
	tr := Trash{Dir: filepath.Join(root, "Trash")}
	f := writeFile(t, root, "f", "x")
	if !freedesktopLayout && tr.Available(f) {
		t.Fatal("missing mac trash must be unavailable")
	}
	must(t, os.MkdirAll(tr.Dir, 0o700))
	if !tr.Available(f) {
		t.Fatal("same-device trash should be available")
	}
	if tr.Available(filepath.Join(root, "nodir", "f")) {
		t.Fatal("missing parent must be unavailable")
	}
	fd := Trash{Dir: filepath.Join(root, "new", "Trash"), forceFreedesktop: true}
	if !fd.Available(f) {
		t.Fatal("creatable freedesktop trash should be available")
	}
}

func TestDeletePermanentlyRefusesReplacedPath(t *testing.T) {
	root := t.TempDir()
	p := writeFile(t, root, "victim", "a")
	fi, err := os.Lstat(p)
	must(t, err)
	must(t, os.Remove(p))
	time.Sleep(20 * time.Millisecond)
	writeFile(t, root, "victim", "different and longer")
	if err := DeletePermanently(p, fi); err == nil {
		t.Fatal("expected refusal")
	}
	if !exists(p) {
		t.Fatal("replacement file was deleted")
	}
}

func TestDeletePermanentlyRemovesTreeWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	must(t, os.Mkdir(outside, 0o700))
	keep := writeFile(t, outside, "precious", "data")
	tree := filepath.Join(root, "tree")
	must(t, os.Mkdir(tree, 0o700))
	writeFile(t, tree, "a", "x")
	must(t, os.Symlink(outside, filepath.Join(tree, "lnk")))
	fi, err := os.Lstat(tree)
	must(t, err)
	must(t, DeletePermanently(tree, fi))
	if exists(tree) {
		t.Fatal("tree not removed")
	}
	if !exists(keep) {
		t.Fatal("followed symlink out of tree")
	}
}

func TestDeletePermanentlySymlinkRemovesLinkOnly(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	must(t, os.Mkdir(outside, 0o700))
	keep := writeFile(t, outside, "precious", "data")
	link := filepath.Join(root, "link")
	must(t, os.Symlink(outside, link))
	fi, err := os.Lstat(link)
	must(t, err)
	must(t, DeletePermanently(link, fi))
	if exists(link) || !exists(keep) {
		t.Fatal("link/target state wrong")
	}
}

func TestDeletePermanentlyRefusesNilExpect(t *testing.T) {
	p := writeFile(t, t.TempDir(), "f", "x")
	if err := DeletePermanently(p, nil); err == nil || !exists(p) {
		t.Fatal("nil expect must refuse")
	}
}

func TestForUserDoesNotTouchDisk(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	tr := ForUser(envWithHome(home))
	if !strings.HasPrefix(tr.Dir, home) || exists(tr.Dir) {
		t.Fatalf("unexpected %s", tr.Dir)
	}
}

func TestRenameNoReplaceKeepsExistingDestination(t *testing.T) {
	root := t.TempDir()
	from := writeFile(t, root, "from", "new")
	to := writeFile(t, root, "to", "precious")
	if err := renameNoReplace(from, to); !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("want EEXIST, got %v", err)
	}
	if b, _ := os.ReadFile(to); string(b) != "precious" {
		t.Fatal("destination replaced")
	}
}

func TestMoveRaceDoesNotClobber(t *testing.T) {
	for _, fd := range []bool{false, true} {
		root := t.TempDir()
		tr := Trash{Dir: filepath.Join(root, "Trash"), forceFreedesktop: fd}.WithClock(fixedClock())
		must(t, os.MkdirAll(tr.Dir, 0o700))
		planted := false
		tr.beforeRename = func(dest string) {
			if !planted {
				planted = true
				must(t, os.WriteFile(dest, []byte("finder"), 0o600))
			}
		}
		src := writeFile(t, root, "item", "mine")
		dest, err := tr.Move(src)
		must(t, err)
		if filepath.Base(dest) == "item" {
			t.Fatal("landed on the raced name")
		}
		if b, _ := os.ReadFile(filepath.Join(tr.filesDir(), "item")); string(b) != "finder" {
			t.Fatalf("raced item clobbered: %q", b)
		}
		if b, _ := os.ReadFile(dest); string(b) != "mine" {
			t.Fatal("item content wrong")
		}
		if fd {
			if exists(filepath.Join(tr.Dir, "info", "item.trashinfo")) {
				t.Fatal("orphan info for raced name not cleaned")
			}
		}
	}
}

func TestForUserWiresClock(t *testing.T) {
	e := envWithHome(t.TempDir())
	e.Now = fixedClock()
	if ForUser(e).clock() != fixedClock()() {
		t.Fatal("ForUser did not wire e.Now")
	}
}

func TestDeletePermanentlyRefusesModifiedSameInode(t *testing.T) {
	root := t.TempDir()
	p := writeFile(t, root, "f", "a")
	fi, err := os.Lstat(p)
	must(t, err)
	time.Sleep(20 * time.Millisecond)
	must(t, os.WriteFile(p, []byte("changed"), 0o600)) // same inode, new ctime
	if err := DeletePermanently(p, fi); err == nil || !exists(p) {
		t.Fatal("expected refusal on ctime change")
	}
}

func TestDeletePermanentlyRefusesTypeChange(t *testing.T) {
	root := t.TempDir()
	p := writeFile(t, root, "f", "a")
	fi, err := os.Lstat(p)
	must(t, err)
	must(t, os.Remove(p))
	must(t, os.Mkdir(p, 0o700))
	if err := DeletePermanently(p, fi); err == nil || !exists(p) {
		t.Fatal("expected refusal on type change")
	}
}
