package receipts_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Audarya07/agent-receipts/internal/receipts"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func appendRaw(t *testing.T, dir, line string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "receipts.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	must(t, err)
	defer f.Close()
	_, err = f.WriteString(line)
	must(t, err)
}

func TestAppendLoadSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	s := receipts.Open(dir)
	must(t, s.Append(receipts.Receipt{Kind: receipts.KindCreated, Path: "/tmp/my-bin", SessionID: "s1"}))
	appendRaw(t, dir, `{"kind":"created","path":"/tmp/x"`+"\n") // truncated line
	must(t, s.Append(receipts.Receipt{Kind: receipts.KindSessionEnd, SessionID: "s1"}))
	appendRaw(t, dir, `{"kind":"created","path":"/tmp/y"`) // truncated last line, no newline
	rs, err := s.Load()
	must(t, err)
	if len(rs) != 2 {
		t.Fatalf("got %d", len(rs))
	}
}

func TestLoadMissingFile(t *testing.T) {
	rs, err := receipts.Open(filepath.Join(t.TempDir(), "nope")).Load()
	must(t, err)
	if len(rs) != 0 {
		t.Fatalf("got %d", len(rs))
	}
}

func TestAppendModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s := receipts.Open(dir)
	must(t, s.Append(receipts.Receipt{Kind: receipts.KindCreated, Path: "/tmp/a"}))
	di, err := os.Stat(dir)
	must(t, err)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
	fi, err := os.Stat(filepath.Join(dir, "receipts.jsonl"))
	must(t, err)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode().Perm())
	}
}

func TestConcurrentAppendsStayParseable(t *testing.T) {
	s := receipts.Open(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			must(t, s.Append(receipts.Receipt{Kind: receipts.KindCreated, Path: fmt.Sprintf("/tmp/f%d", i)}))
		}(i)
	}
	wg.Wait()
	rs, err := s.Load()
	must(t, err)
	if len(rs) != 50 {
		t.Fatalf("got %d", len(rs))
	}
}

func TestAppendAfterTruncatedLineKeepsNewReceipt(t *testing.T) {
	dir := t.TempDir()
	s := receipts.Open(dir)
	must(t, s.Append(receipts.Receipt{Kind: receipts.KindCreated, Path: "/tmp/a", SessionID: "s1"}))
	appendRaw(t, dir, `{"kind":"created","path":"/tmp/trunc"`) // crash: no trailing newline
	must(t, s.Append(receipts.Receipt{Kind: receipts.KindCreated, Path: "/tmp/b", SessionID: "s2"}))
	rs, err := s.Load()
	must(t, err)
	if len(rs) != 2 || rs[1].Path != "/tmp/b" {
		t.Fatalf("got %+v", rs)
	}
}
