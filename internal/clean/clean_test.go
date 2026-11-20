package clean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/scan"
	"github.com/Audarya07/agent-receipts/internal/secrets"
	"github.com/Audarya07/agent-receipts/internal/stats"
)

type fakeUI struct {
	answers  []string // consumed by Choose
	confirms []bool
	out      strings.Builder
	prompts  []string
}

func (u *fakeUI) Printf(f string, a ...any) { fmt.Fprintf(&u.out, f, a...) }
func (u *fakeUI) Choose(p string, keys []string) (string, error) {
	u.prompts = append(u.prompts, p)
	if len(u.answers) == 0 {
		return "", errors.New("no scripted answer")
	}
	a := u.answers[0]
	u.answers = u.answers[1:]
	return a, nil
}
func (u *fakeUI) Confirm(p, w string) (bool, error) {
	if len(u.confirms) == 0 {
		return false, errors.New("no scripted confirm")
	}
	c := u.confirms[0]
	u.confirms = u.confirms[1:]
	return c, nil
}

type fakes struct {
	items     []item.Item
	recheck   func(item.Item) (item.Item, error)
	moveErr   error
	calls     []string
	expects   []os.FileInfo
	removeErr error
}

func (f *fakes) Move(p string) (string, error) {
	f.calls = append(f.calls, "move "+p)
	return "/trash/x", f.moveErr
}

func (f *fakes) deps(t *testing.T) Deps {
	return Deps{
		Scan: func(context.Context) (scan.Result, error) { return scan.Result{Items: f.items}, nil },
		Recheck: func(_ context.Context, it item.Item) (item.Item, error) {
			if f.recheck != nil {
				return f.recheck(it)
			}
			return it, nil
		},
		Trash: f,
		DeletePermanently: func(p string, e os.FileInfo) error {
			f.calls = append(f.calls, "delete "+p)
			f.expects = append(f.expects, e)
			return nil
		},
		RemoveWorktree: func(repo, p string) error {
			f.calls = append(f.calls, "unregister "+repo+" "+p)
			return f.removeErr
		},
		StateDir: t.TempDir(),
		IsTTY:    func() bool { return true },
	}
}

func mk(t *testing.T, name string, size int64) item.Item {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return item.Item{ID: name, Category: item.Scratch, Path: p, Size: size,
		Decision: item.Decision{Class: item.Offer}, Fingerprint: "fp-" + name}
}

func withSecret(it item.Item) item.Item {
	it.Secrets = []secrets.Finding{{Path: "a.env", Line: 3, Description: "key", Masked: "AKIA****"}}
	return it
}

func TestCleanRefusesWithoutTTY(t *testing.T) {
	f := &fakes{items: []item.Item{mk(t, "a", 1)}}
	d := f.deps(t)
	d.IsTTY = func() bool { return false }
	scanned := false
	d.Scan = func(context.Context) (scan.Result, error) { scanned = true; return scan.Result{}, nil }
	_, err := Run(context.Background(), d, &fakeUI{})
	if err == nil || !strings.Contains(err.Error(), "needs a terminal") {
		t.Fatalf("err = %v", err)
	}
	if scanned || len(f.calls) != 0 {
		t.Fatalf("scanned=%v calls=%v", scanned, f.calls)
	}
}

func TestCleanTrashesKeepsAndSkipsRest(t *testing.T) {
	f := &fakes{items: []item.Item{mk(t, "a", 400), mk(t, "b", 300), mk(t, "c", 200), mk(t, "d", 100)}}
	ui := &fakeUI{answers: []string{"d", "k", "s"}}
	sum, err := Run(context.Background(), f.deps(t), ui)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Trashed != 1 || sum.Kept != 1 || sum.Skipped != 2 || sum.BytesTrashed != 400 {
		t.Fatalf("sum = %+v", sum)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
	st := stats.Load(f.deps(t).StateDir)
	_ = st
}

func TestCleanStatsRecorded(t *testing.T) {
	f := &fakes{items: []item.Item{withSecret(mk(t, "a", 400)), mk(t, "b", 300)}}
	d := f.deps(t)
	ui := &fakeUI{answers: []string{"t", "y", "k"}}
	if _, err := Run(context.Background(), d, ui); err != nil {
		t.Fatal(err)
	}
	s := stats.Load(d.StateDir)
	if s.ItemsCleaned != 1 || s.ItemsKept != 1 || s.BytesTrashed != 400 || s.SecretReal != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestCleanOrdersSecretsFirstThenSize(t *testing.T) {
	f := &fakes{items: []item.Item{mk(t, "small", 1), mk(t, "big", 9), withSecret(mk(t, "sec", 2))}}
	ui := &fakeUI{answers: []string{"k", "k", "k"}}
	if _, err := Run(context.Background(), f.deps(t), ui); err != nil {
		t.Fatal(err)
	}
	out := ui.out.String()
	if !(strings.Index(out, "sec") < strings.Index(out, "big") && strings.Index(out, "big") < strings.Index(out, "small")) {
		t.Fatalf("order wrong:\n%s", out)
	}
}

func TestCleanSecretNeedsTypedConfirm(t *testing.T) {
	f := &fakes{items: []item.Item{withSecret(mk(t, "a", 5))}}
	ui := &fakeUI{answers: []string{"p"}, confirms: []bool{false}}
	if _, err := Run(context.Background(), f.deps(t), ui); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("deleted without confirmation: %v", f.calls)
	}
	if strings.Contains(ui.out.String(), "AKIA") == false {
		t.Fatal("masked secret should be shown")
	}

	f = &fakes{items: []item.Item{withSecret(mk(t, "a", 5))}}
	ui = &fakeUI{answers: []string{"p", "n"}, confirms: []bool{true}}
	sum, err := Run(context.Background(), f.deps(t), ui)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "delete ") || f.expects[0] == nil {
		t.Fatalf("calls=%v expects=%v", f.calls, f.expects)
	}
	if sum.Deleted != 1 || sum.SecretNotReal != 1 {
		t.Fatalf("sum = %+v", sum)
	}
}

func TestCleanChoiceSets(t *testing.T) {
	perm := mk(t, "p", 1)
	perm.Decision.Permanent = true
	permSec := withSecret(perm)
	unreg := mk(t, "u", 1)
	unreg.Unregister = true
	cases := []struct {
		it   item.Item
		want string
	}{
		{mk(t, "n", 1), "[d]elete (to Trash) / [k]eep / [s]kip the rest"},
		{withSecret(mk(t, "s", 1)), "[p]ermanently delete / [t]rash anyway / [k]eep / [s]kip the rest"},
		{perm, "[p]ermanently delete / [k]eep / [s]kip the rest"},
		{permSec, "[p]ermanently delete / [k]eep / [s]kip the rest"},
		{unreg, "[u]nregister / [k]eep / [s]kip the rest"},
	}
	for _, c := range cases {
		if _, got := choices(c.it); got != c.want {
			t.Errorf("%s: %q", c.it.ID, got)
		}
	}
}

func TestCleanSkipsChangedSinceReport(t *testing.T) {
	for name, rc := range map[string]func(item.Item) (item.Item, error){
		"active": func(it item.Item) (item.Item, error) {
			it.Decision = item.Decision{Class: item.LeftAlone, Reasons: []string{"session active 1 min ago"}}
			return it, nil
		},
		"fingerprint": func(it item.Item) (item.Item, error) { it.Fingerprint = "other"; return it, nil },
		"gone":        func(it item.Item) (item.Item, error) { return item.Item{}, fmt.Errorf("x: %w", os.ErrNotExist) },
		"other-error": func(it item.Item) (item.Item, error) { return item.Item{}, errors.New("boom") },
		"other-path":  func(it item.Item) (item.Item, error) { it.Path = "/elsewhere"; return it, nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakes{items: []item.Item{mk(t, "a", 5)}, recheck: rc}
			ui := &fakeUI{answers: []string{"d"}}
			sum, err := Run(context.Background(), f.deps(t), ui)
			if err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != 0 || sum.Skipped != 1 || !strings.Contains(ui.out.String(), "skipped: ") {
				t.Fatalf("calls=%v sum=%+v out=%s", f.calls, sum, ui.out.String())
			}
		})
	}
}

func TestCleanSkipMessages(t *testing.T) {
	f := &fakes{items: []item.Item{mk(t, "a", 5)}, recheck: func(it item.Item) (item.Item, error) {
		it.Decision = item.Decision{Class: item.LeftAlone, Reasons: []string{"session active 1 min ago"}}
		return it, nil
	}}
	ui := &fakeUI{answers: []string{"d"}}
	Run(context.Background(), f.deps(t), ui)
	if !strings.Contains(ui.out.String(), "skipped: session active 1 min ago") {
		t.Fatal(ui.out.String())
	}
}

func TestCleanWorktreeMovesThenUnregisters(t *testing.T) {
	w := mk(t, "wt", 5)
	w.Category, w.Repo = item.Worktree, "/repo"
	f := &fakes{items: []item.Item{w}}
	if _, err := Run(context.Background(), f.deps(t), &fakeUI{answers: []string{"d"}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"move " + w.Path, "unregister /repo " + w.Path}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestCleanMoveFailsNoUnregister(t *testing.T) {
	w := mk(t, "wt", 5)
	w.Category, w.Repo = item.Worktree, "/repo"
	f := &fakes{items: []item.Item{w}, moveErr: errors.New("nope")}
	sum, err := Run(context.Background(), f.deps(t), &fakeUI{answers: []string{"d"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || sum.Skipped != 1 || sum.Trashed != 0 {
		t.Fatalf("calls=%v sum=%+v", f.calls, sum)
	}
}

func TestCleanUnregisterFailureAfterMoveContinues(t *testing.T) {
	w := mk(t, "wt", 5)
	w.Category, w.Repo = item.Worktree, "/repo"
	f := &fakes{items: []item.Item{w, mk(t, "b", 1)}, removeErr: errors.New("locked")}
	ui := &fakeUI{answers: []string{"d", "d"}}
	sum, err := Run(context.Background(), f.deps(t), ui)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Trashed != 2 || !strings.Contains(ui.out.String(), "git still lists it: locked") || !strings.Contains(ui.out.String(), "git worktree prune") {
		t.Fatalf("sum=%+v out=%s", sum, ui.out.String())
	}
}

func TestCleanUnregisterItem(t *testing.T) {
	w := item.Item{ID: "u", Category: item.Worktree, Path: "/gone/wt", Repo: "/repo", Unregister: true, Size: 99,
		Decision: item.Decision{Class: item.Offer}, Fingerprint: "f"}
	f := &fakes{items: []item.Item{w}}
	d := f.deps(t)
	ui := &fakeUI{answers: []string{"u"}}
	sum, err := Run(context.Background(), d, ui)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, "|") != "unregister /repo /gone/wt" || sum.Unregistered != 1 || sum.BytesTrashed != 0 || sum.BytesDeleted != 0 {
		t.Fatalf("calls=%v sum=%+v", f.calls, sum)
	}
	if s := stats.Load(d.StateDir); s.ItemsCleaned != 1 || s.BytesTrashed != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestCleanEXDEVSkips(t *testing.T) {
	f := &fakes{items: []item.Item{mk(t, "a", 5)}, moveErr: fmt.Errorf("trash: %w", syscall.EXDEV)}
	ui := &fakeUI{answers: []string{"d"}}
	sum, err := Run(context.Background(), f.deps(t), ui)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Skipped != 1 || len(f.calls) != 1 || !strings.Contains(ui.out.String(), "another volume") {
		t.Fatalf("calls=%v sum=%+v out=%s", f.calls, sum, ui.out.String())
	}
}

func TestCleanEndsWithTrashReminder(t *testing.T) {
	f := &fakes{items: []item.Item{mk(t, "a", 2048)}}
	ui := &fakeUI{answers: []string{"d"}}
	if _, err := Run(context.Background(), f.deps(t), ui); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ui.out.String(), "empty it to free the space") {
		t.Fatal(ui.out.String())
	}
}
