package nudge_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Audarya07/agent-receipts/internal/item"
	"github.com/Audarya07/agent-receipts/internal/nudge"
	"github.com/Audarya07/agent-receipts/internal/scan"
	"github.com/Audarya07/agent-receipts/internal/secrets"
)

func fixed() time.Time { return time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC) }

func cacheWith(bytes int64, ids ...string) scan.Result {
	r := scan.Result{GeneratedAt: fixed()}
	r.Items = append(r.Items, item.Item{ID: "big", Size: bytes, Decision: item.Decision{Class: item.Offer}})
	for _, id := range ids {
		r.Items = append(r.Items, item.Item{ID: id, Size: 0,
			Decision: item.Decision{Class: item.LeftAlone}, Secrets: []secrets.Finding{{}}})
	}
	return r
}

func expect(t *testing.T, got, want bool, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: show = %v, want %v", what, got, want)
	}
}

func TestDecide(t *testing.T) {
	now := fixed()
	_, show, st := nudge.Decide(now, cacheWith(2_000_000_000, "s1"), nudge.State{}, time.Time{})
	expect(t, show, true, "first")
	_, show, _ = nudge.Decide(now.Add(2*time.Hour), cacheWith(2_000_000_000, "s1"), st, time.Time{})
	expect(t, show, false, "within 24h")
	_, show, _ = nudge.Decide(now.Add(25*time.Hour), cacheWith(2_000_000_000, "s1"), st, time.Time{})
	expect(t, show, false, "nothing new")
	_, show, _ = nudge.Decide(now.Add(25*time.Hour), cacheWith(2_000_000_000, "s1", "s2"), st, time.Time{})
	expect(t, show, true, "new secret")
	_, show, _ = nudge.Decide(now.Add(25*time.Hour), cacheWith(3_500_000_000, "s1"), st, time.Time{})
	expect(t, show, true, "more than 1 GB new data")
	_, show, _ = nudge.Decide(now.Add(25*time.Hour), cacheWith(2_900_000_000, "s1"), st, time.Time{})
	expect(t, show, false, "under 1 GB new data")
}

func TestDecideBacksOffAfterThreeIgnored(t *testing.T) {
	now := fixed()
	st := nudge.State{}
	var show bool
	for i := 0; i < 3; i++ { // three nudges, never followed by a report
		ids := []string{"a", "b", "c"}[:i+1]
		_, show, st = nudge.Decide(now.Add(time.Duration(i)*25*time.Hour), cacheWith(0, ids...), st, time.Time{})
		expect(t, show, true, "nudge while still counting")
	}
	last := st.LastShown
	// the third nudge is ignored too: streak reaches 3 when judged
	_, show, st2 := nudge.Decide(last.Add(2*24*time.Hour), cacheWith(0, "a", "b", "c", "d"), st, time.Time{})
	expect(t, show, false, "2-day gap after 3 ignored")
	if st2.IgnoredStreak != 3 {
		t.Fatalf("streak = %d, want 3", st2.IgnoredStreak)
	}
	// repeated calls must not inflate the streak
	_, _, st3 := nudge.Decide(last.Add(3*24*time.Hour), cacheWith(0, "a", "b", "c", "d"), st2, time.Time{})
	if st3.IgnoredStreak != 3 {
		t.Errorf("streak inflated to %d", st3.IgnoredStreak)
	}
	_, show, _ = nudge.Decide(last.Add(8*24*time.Hour), cacheWith(0, "a", "b", "c", "d"), st3, time.Time{})
	expect(t, show, true, "8-day gap")
}

func TestDecideFollowedResetsStreak(t *testing.T) {
	now := fixed()
	st := nudge.State{LastShown: now, IgnoredStreak: 2, LastSecretIDs: []string{"a"}}
	_, show, next := nudge.Decide(now.Add(25*time.Hour), cacheWith(0, "a", "b"), st, now.Add(time.Hour))
	expect(t, show, true, "followed then new secret")
	if next.IgnoredStreak != 0 {
		t.Errorf("streak = %d, want 0", next.IgnoredStreak)
	}
	// a report 30h after the nudge is not a follow-up
	_, _, next = nudge.Decide(now.Add(31*time.Hour), cacheWith(0, "a"), st, now.Add(30*time.Hour))
	if next.IgnoredStreak != 3 {
		t.Errorf("streak = %d, want 3", next.IgnoredStreak)
	}
}

func TestDecideSkipsDetailsOnly(t *testing.T) {
	c := cacheWith(0, "s1")
	c.Items[1].Decision.DetailsOnly = true
	_, show, _ := nudge.Decide(fixed(), c, nudge.State{}, time.Time{})
	expect(t, show, false, "details-only finding")
}

func TestDecideMessageCopy(t *testing.T) {
	tests := []struct {
		c    scan.Result
		want string
	}{
		{cacheWith(3_100_000_000, "a", "b"), "agent-receipts: agents left 3.1 GB on this machine, 2 items look like secrets. run 'agent-receipts report'"},
		{cacheWith(3_100_000_000, "a"), "agent-receipts: agents left 3.1 GB on this machine, 1 item looks like a secret. run 'agent-receipts report'"},
		{cacheWith(3_100_000_000), "agent-receipts: agents left 3.1 GB on this machine. run 'agent-receipts report'"},
	}
	for _, tc := range tests {
		msg, show, _ := nudge.Decide(fixed(), tc.c, nudge.State{}, time.Time{})
		if !show || msg != tc.want {
			t.Errorf("got %q (show=%v), want %q", msg, show, tc.want)
		}
	}
}

func TestLoadSave(t *testing.T) {
	dir := t.TempDir()
	st, err := nudge.Load(dir)
	if err != nil || st.IgnoredStreak != 0 || !st.LastShown.IsZero() {
		t.Fatalf("missing: %+v %v", st, err)
	}
	want := nudge.State{LastShown: fixed(), IgnoredStreak: 2, LastBytes: 5, LastSecretIDs: []string{"x"}}
	if err := nudge.Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := nudge.Load(dir)
	if err != nil || !got.LastShown.Equal(want.LastShown) || got.IgnoredStreak != 2 || got.LastBytes != 5 || len(got.LastSecretIDs) != 1 {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	fi, _ := os.Stat(filepath.Join(dir, "nudge.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	os.WriteFile(filepath.Join(dir, "nudge.json"), []byte("{"), 0o600)
	if st, err := nudge.Load(dir); err != nil || st.IgnoredStreak != 0 {
		t.Errorf("invalid: %+v %v", st, err)
	}
}

func TestDecideJudgesOnlyAfterWindowOrReport(t *testing.T) {
	t0 := fixed()
	_, show, st := nudge.Decide(t0, cacheWith(0, "a"), nudge.State{}, time.Time{})
	expect(t, show, true, "first")
	// a later session inside the window, no report yet: not judged
	_, _, mid := nudge.Decide(t0.Add(2*time.Hour), cacheWith(0, "a"), st, time.Time{})
	if mid.IgnoredStreak != 0 || !mid.Judged.IsZero() {
		t.Fatalf("judged inside window: %+v", mid)
	}
	// the report at t0+3h is credited at t0+25h
	_, _, after := nudge.Decide(t0.Add(25*time.Hour), cacheWith(0, "a"), mid, t0.Add(3*time.Hour))
	if after.IgnoredStreak != 0 || !after.Judged.Equal(t0) {
		t.Fatalf("want followed, got %+v", after)
	}
	// no report at all: ignored once the window has passed
	_, _, ign := nudge.Decide(t0.Add(25*time.Hour), cacheWith(0, "a"), mid, time.Time{})
	if ign.IgnoredStreak != 1 {
		t.Fatalf("want streak 1, got %+v", ign)
	}
}
