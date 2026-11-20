package transcripts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Audarya07/agent-receipts/internal/env"
	"github.com/Audarya07/agent-receipts/internal/receipts"
	"github.com/Audarya07/agent-receipts/internal/transcripts"
)

const fakeAWS = "AKIAZ7Q2XJ4K3M5N6Q2X"

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fixture renders testdata/session.jsonl with machine-specific paths.
func fixture(t *testing.T) (e env.Env, file, tmp, cwd string) {
	t.Helper()
	root := t.TempDir()
	tmp = filepath.Join(root, "tmp")
	cwd = filepath.Join(root, "proj")
	must(t, os.MkdirAll(tmp, 0o755))
	must(t, os.MkdirAll(cwd, 0o755))
	e = env.Env{Home: filepath.Join(root, "home"), StateDir: filepath.Join(root, "state")}
	e.TmpDirs = []string{e.Canon(tmp)}
	tmp, cwd = e.Canon(tmp), e.Canon(cwd)
	raw, err := os.ReadFile("testdata/session.jsonl")
	must(t, err)
	s := strings.NewReplacer("@TMP@", tmp, "@CWD@", cwd).Replace(string(raw))
	dir := filepath.Join(e.Home, ".claude", "projects", "-proj")
	must(t, os.MkdirAll(dir, 0o755))
	file = filepath.Join(dir, "sess-1.jsonl")
	must(t, os.WriteFile(file, []byte(s), 0o644))
	return
}

func created(rs []receipts.Receipt) (paths []string) {
	for _, r := range rs {
		if r.Kind == receipts.KindCreated {
			paths = append(paths, r.Path)
		}
	}
	slices.Sort(paths)
	return
}

func TestParseFileEmitsOnlyInterestingAndNoContent(t *testing.T) {
	e, file, tmp, _ := fixture(t)
	var got []receipts.Receipt
	off, err := transcripts.ParseFile(e, file, 0, func(r receipts.Receipt) { got = append(got, r) })
	must(t, err)
	fi, _ := os.Stat(file)
	if off != fi.Size() {
		t.Fatalf("offset %d, size %d", off, fi.Size())
	}
	want := []string{tmp + "/alpha.go", tmp + "/creds.txt", tmp + "/my-bin"}
	if p := created(got); !slices.Equal(p, want) {
		t.Fatalf("created = %v, want %v", p, want)
	}
	var starts, ends int
	for _, r := range got {
		if r.Agent != "claude" || r.Source != "transcript" || r.SessionID != "sess-1" || r.SessionName != "nightly db backup" || r.PID != 0 {
			t.Fatalf("bad receipt %+v", r)
		}
		switch r.Kind {
		case receipts.KindSessionStart:
			starts++
			if r.TranscriptPath != file || !r.At.Equal(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)) {
				t.Fatalf("bad start %+v", r)
			}
		case receipts.KindSessionEnd:
			ends++
			if !r.At.Equal(time.Date(2026, 11, 10, 9, 0, 40, 0, time.UTC)) {
				t.Fatalf("bad end %+v", r)
			}
		case receipts.KindCreated:
			if r.Path == tmp+"/my-bin" && r.Via == "" {
				t.Fatalf("missing via %+v", r)
			}
			if r.Path == tmp+"/alpha.go" && r.Via != "Write" {
				t.Fatalf("via %q", r.Via)
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("starts=%d ends=%d", starts, ends)
	}
	b, _ := json.Marshal(got)
	if bytes.Contains(b, []byte(fakeAWS)) {
		t.Fatal("content leaked into receipts")
	}
}

func TestReconstructResumesFromOffset(t *testing.T) {
	e, _, tmp, _ := fixture(t)
	st := receipts.Open(e.StateDir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done, err := transcripts.Reconstruct(ctx, e, st)
	must(t, err)
	if done {
		t.Fatal("cancelled run reported done")
	}
	if rs, _ := st.Load(); len(rs) != 0 {
		t.Fatalf("cancelled run wrote %d receipts", len(rs))
	}
	for i := 0; i < 2; i++ {
		done, err = transcripts.Reconstruct(context.Background(), e, st)
		must(t, err)
		if !done {
			t.Fatal("expected done")
		}
	}
	rs, _ := st.Load()
	if p := created(rs); len(p) != 3 || p[0] != tmp+"/alpha.go" {
		t.Fatalf("created = %v", p)
	}
	if len(rs) != 5 {
		t.Fatalf("receipts duplicated: %d", len(rs))
	}
	fi, err := os.Stat(filepath.Join(e.StateDir, "reconstruct.json"))
	must(t, err)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("progress mode %v", fi.Mode())
	}
}

func TestParseFileResumeDoesNotRestartSession(t *testing.T) {
	e, file, _, _ := fixture(t)
	off, err := transcripts.ParseFile(e, file, 0, func(receipts.Receipt) {})
	must(t, err)
	var got []receipts.Receipt
	_, err = transcripts.ParseFile(e, file, off, func(r receipts.Receipt) { got = append(got, r) })
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("re-parse at end emitted %+v", got)
	}
}

func TestReconstructShrunkFileRestarts(t *testing.T) {
	e, file, _, _ := fixture(t)
	st := receipts.Open(e.StateDir)
	must(t, os.MkdirAll(e.StateDir, 0o700))
	must(t, os.WriteFile(filepath.Join(e.StateDir, "reconstruct.json"), []byte(`{"files":{"`+file+`":99999999}}`), 0o600))
	done, err := transcripts.Reconstruct(context.Background(), e, st)
	must(t, err)
	rs, _ := st.Load()
	if !done || len(created(rs)) != 3 {
		t.Fatalf("done=%v receipts=%d", done, len(rs))
	}
}

func TestReconstructSecondRunnerSkips(t *testing.T) {
	e, _, _, _ := fixture(t)
	must(t, os.MkdirAll(e.StateDir, 0o700))
	lf, err := os.OpenFile(filepath.Join(e.StateDir, "reconstruct.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	must(t, err)
	defer lf.Close()
	must(t, unix.Flock(int(lf.Fd()), unix.LOCK_EX|unix.LOCK_NB))
	st := receipts.Open(e.StateDir)
	done, err := transcripts.Reconstruct(context.Background(), e, st)
	must(t, err)
	if done {
		t.Fatal("done while locked")
	}
	if rs, _ := st.Load(); len(rs) != 0 {
		t.Fatal("wrote while locked")
	}
	if _, err := os.Stat(filepath.Join(e.StateDir, "reconstruct.json")); err == nil {
		t.Fatal("progress written while locked")
	}
}

func TestReconstructSkipsUnreadableTranscript(t *testing.T) {
	e, _, _, _ := fixture(t)
	bad := filepath.Join(e.Home, ".claude", "projects", "-proj", "locked.jsonl")
	must(t, os.WriteFile(bad, []byte("{}\n"), 0o000))
	// Make the locked file newest so it is visited first.
	now := time.Now().Add(time.Hour)
	must(t, os.Chtimes(bad, now, now))
	st := receipts.Open(e.StateDir)
	done, err := transcripts.Reconstruct(context.Background(), e, st)
	must(t, err)
	rs, _ := st.Load()
	if !done || len(created(rs)) != 3 {
		t.Fatalf("done=%v created=%v", done, created(rs))
	}
}

func TestReconstructReplacedFileRestartsFromZero(t *testing.T) {
	e, file, tmp, cwd := fixture(t)
	st := receipts.Open(e.StateDir)
	_, err := transcripts.Reconstruct(context.Background(), e, st)
	must(t, err)
	old, _ := os.ReadFile(file)
	repl := strings.ReplaceAll(string(old), "alpha.go", "omega.go") // same length
	if len(repl) != len(old) {
		t.Fatal("test bug: lengths differ")
	}
	must(t, os.Remove(file))
	must(t, os.WriteFile(file, []byte(repl), 0o644))
	done, err := transcripts.Reconstruct(context.Background(), e, st)
	must(t, err)
	rs, _ := st.Load()
	found := false
	for _, p := range created(rs) {
		found = found || p == tmp+"/omega.go"
	}
	if !done || !found {
		t.Fatalf("done=%v created=%v", done, created(rs))
	}
	_ = cwd
}

func TestSessionReceiptsCarryCanonicalCwd(t *testing.T) {
	e, file, _, cwd := fixture(t)
	var got []receipts.Receipt
	_, err := transcripts.ParseFile(e, file, 0, func(r receipts.Receipt) { got = append(got, r) })
	must(t, err)
	n := 0
	for _, r := range got {
		if r.Kind == receipts.KindSessionStart || r.Kind == receipts.KindSessionEnd {
			n++
			if r.Cwd != cwd {
				t.Fatalf("%s cwd %q, want %q", r.Kind, r.Cwd, cwd)
			}
		}
	}
	if n != 2 {
		t.Fatalf("session receipts: %d", n)
	}
}
