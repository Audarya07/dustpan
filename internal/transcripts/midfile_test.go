package transcripts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Audarya07/dustpan/internal/env"
	"github.com/Audarya07/dustpan/internal/receipts"
)

func TestParseFileCancelsMidFileAtLineBoundary(t *testing.T) {
	line := `{"type":"api-request","sessionId":"s","timestamp":"2026-11-10T09:00:00.000Z"}` + "\n"
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Repeat(line, 2500)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var n int
	off, cancelled, err := parseFile(ctx, env.Env{}, p, 0, func(receipts.Receipt) { n++ })
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	if want := int64(ctxCheckEvery * len(line)); off != want {
		t.Fatalf("offset %d, want %d", off, want)
	}
	// Resuming from the saved offset finishes the file without a second session-start.
	n = 0
	off2, cancelled, _ := parseFile(context.Background(), env.Env{}, p, off, func(r receipts.Receipt) {
		if r.Kind == receipts.KindSessionStart {
			t.Fatal("resume restarted session")
		}
		n++
	})
	if cancelled || off2 != int64(2500*len(line)) {
		t.Fatalf("resume off=%d cancelled=%v", off2, cancelled)
	}
}
