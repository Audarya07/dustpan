package receipts_test

import (
	"testing"
	"time"

	"github.com/Audarya07/agent-receipts/internal/receipts"
)

func TestStrongCreatorMatchesDirAncestor(t *testing.T) {
	ix := receipts.BuildIndex([]receipts.Receipt{{Kind: receipts.KindCreated, Path: "/tmp/wt-931", IsDir: true, SessionID: "s1"}})
	if r, ok := ix.StrongCreator("/tmp/wt-931/go.mod"); !ok || r.SessionID != "s1" {
		t.Fatal("want match")
	}
	if _, ok := ix.StrongCreator("/tmp/wt-9310"); ok {
		t.Fatal("prefix must not match")
	}
	if _, ok := ix.StrongCreator("/tmp/wt-931"); !ok {
		t.Fatal("exact match wanted")
	}
}

func TestStrongCreatorFileDoesNotCoverChildren(t *testing.T) {
	ix := receipts.BuildIndex([]receipts.Receipt{{Kind: receipts.KindCreated, Path: "/tmp/f", SessionID: "s1"}})
	if _, ok := ix.StrongCreator("/tmp/f/x"); ok {
		t.Fatal("non-dir must not cover children")
	}
}

func TestWeakCreatorsAndCwds(t *testing.T) {
	ix := receipts.BuildIndex([]receipts.Receipt{
		{Kind: receipts.KindSessionStart, SessionID: "s1", Cwd: "/repo"},
		{Kind: receipts.KindSessionStart, SessionID: "s2", Cwd: "/repo"},
		{Kind: receipts.KindSessionStart, SessionID: "s3", Cwd: "/other"},
		{Kind: receipts.KindWeakCreated, Path: "/tmp/w", SessionID: "s1"},
		{Kind: receipts.KindWeakCreated, Path: "/tmp/w", SessionID: "s2"},
		{Kind: receipts.KindWeakCreated, Path: "/tmp/w", SessionID: "s1"},
	})
	if got := ix.WeakCreators("/tmp/w"); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got := ix.Cwds(); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestCreatedUnder(t *testing.T) {
	ix := receipts.BuildIndex([]receipts.Receipt{
		{Kind: receipts.KindCreated, Path: "/tmp/a/b", SessionID: "s1"},
		{Kind: receipts.KindCreated, Path: "/tmp/ab", SessionID: "s1"},
		{Kind: receipts.KindWeakCreated, Path: "/tmp/a/w", SessionID: "s1"},
	})
	got := ix.CreatedUnder([]string{"/tmp/a"})
	if len(got) != 1 || got[0].Path != "/tmp/a/b" {
		t.Fatalf("got %v", got)
	}
}

func TestResumedSessionClearsEnded(t *testing.T) {
	t0 := time.Unix(1000, 0)
	ix := receipts.BuildIndex([]receipts.Receipt{
		{Kind: receipts.KindSessionStart, SessionID: "s1", Agent: "claude", At: t0, SessionName: "a"},
		{Kind: receipts.KindSessionEnd, SessionID: "s1", At: t0.Add(time.Minute)},
	})
	if ix.Sessions()["s1"].Ended.IsZero() {
		t.Fatal("want ended")
	}
	ix = receipts.BuildIndex([]receipts.Receipt{
		{Kind: receipts.KindSessionStart, SessionID: "s1", Agent: "claude", At: t0, SessionName: "a"},
		{Kind: receipts.KindSessionEnd, SessionID: "s1", At: t0.Add(time.Minute)},
		{Kind: receipts.KindSessionStart, SessionID: "s1", At: t0.Add(2 * time.Minute), SessionName: "b"},
	})
	s := ix.Sessions()["s1"]
	if !s.Ended.IsZero() {
		t.Fatal("resumed session must clear Ended")
	}
	if s.Name != "b" || s.Agent != "claude" || !s.Started.Equal(t0) {
		t.Fatalf("got %+v", s)
	}
}
