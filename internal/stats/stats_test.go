package stats_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Audarya07/agent-receipts/internal/stats"
)

func TestStatsSurvivesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stats.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stats.Update(dir, func(s *stats.Stats) { s.ReportsRun++ }); err != nil {
		t.Fatal(err)
	}
	if got := stats.Load(dir).ReportsRun; got != 1 {
		t.Fatalf("ReportsRun = %d", got)
	}
	fi, _ := os.Stat(filepath.Join(dir, "stats.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
}

func TestStatsConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := stats.Update(dir, func(s *stats.Stats) { s.ReportsRun++ }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := stats.Load(dir).ReportsRun; got != 20 {
		t.Fatalf("ReportsRun = %d, lost updates", got)
	}
}
