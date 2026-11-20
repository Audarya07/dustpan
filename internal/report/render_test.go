package report_test

import (
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Audarya07/dustpan/internal/item"
	"github.com/Audarya07/dustpan/internal/report"
	"github.com/Audarya07/dustpan/internal/scan"
	"github.com/Audarya07/dustpan/internal/secrets"
)

var update = flag.Bool("update", false, "rewrite golden files")

var fixedTime = time.Date(2026, 11, 19, 10, 30, 0, 0, time.UTC)

const fakeAWS = "AKIAIOSFODNN7EXAMPLE"

func TestMain(m *testing.M) {
	flag.Parse()
	report.HomeDir = "/home/u"
	os.Exit(m.Run())
}

func golden(t *testing.T, name string, render func(io.Writer)) {
	t.Helper()
	var buf bytes.Buffer
	render(&buf)
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Errorf("%s mismatch\n--- got ---\n%s--- want ---\n%s", name, buf.String(), want)
	}
}

func offer(cat item.Category, path string, size int64) item.Item {
	return item.Item{
		ID: item.MakeID(cat, path), Category: cat, Path: path, Size: size, Newest: fixedTime.Add(-40 * 24 * time.Hour),
		Evidence: item.Evidence{Strong: true, SessionName: "cleanup", Agent: "claude"},
		Decision: item.Decision{Class: item.Offer},
	}
}

// specExample mirrors the §8 example: four dead groups (1.6 GB, 1.0 GB,
// 318 MB, 180 MB), secret findings, two worktrees left alone.
func specExample() scan.Result {
	wt1 := offer(item.Worktree, "/home/u/repo/.claude/worktrees/x", 900_000_000)
	wt1.Secrets = []secrets.Finding{
		{Path: ".env", Line: 1, RuleID: "generic-api-key", Masked: "sk…aaaa"},
		{Path: ".env", Line: 2, RuleID: "generic-api-key", Masked: "sk…bbbb"},
		{Path: ".env", Line: 3, RuleID: "generic-api-key", Masked: "sk…cccc"},
	}
	wt2 := offer(item.Worktree, "/home/u/repo/.claude/worktrees/y", 700_000_000)
	items := []item.Item{wt1, wt2, offer(item.Transcript, "/home/u/.claude/projects/p/a.jsonl", 1_000_000_000)}
	for i, d := range []string{"7f3a91c2", "2", "3", "4"} {
		j := offer(item.Job, "/home/u/.claude/jobs/"+d, 79_500_000)
		if i == 0 {
			j.Secrets = []secrets.Finding{{Path: "tmp/acct_prod.json", Line: 4, RuleID: "aws-access-token", Masked: "AKIA…7Q2X"}}
		}
		items = append(items, j)
	}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		items = append(items, offer(item.Scratch, "/tmp/"+n, 22_500_000))
	}
	for _, n := range []string{"live1", "live2"} {
		it := offer(item.Worktree, "/home/u/repo/.claude/worktrees/"+n, 50_000_000)
		it.Branch = "feature-1234"
		it.Decision = item.Decision{Class: item.LeftAlone, Reasons: []string{"session active 4 min ago"}}
		items = append(items, it)
	}
	hidden := offer(item.Worktree, "/home/u/repo/.claude/worktrees/zz", 5)
	hidden.Decision = item.Decision{Class: item.LeftAlone, DetailsOnly: true, Reasons: []string{"only weak evidence an agent made this"}}
	items = append(items, hidden)
	return scan.Result{GeneratedAt: fixedTime, Items: items}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 96_000: "96 KB", 318_000_000: "318 MB", 3_100_000_000: "3.1 GB", 1_600_000_000: "1.6 GB", 9_960_000: "10 MB", 999_600: "1.0 MB"} {
		if got := report.HumanSize(n); got != want {
			t.Errorf("%d: got %q want %q", n, got, want)
		}
	}
}

func TestCompactGolden(t *testing.T) {
	golden(t, "compact.golden", func(w io.Writer) { report.RenderCompact(w, specExample(), false) })
}

func TestEmptyGolden(t *testing.T) {
	golden(t, "empty.golden", func(w io.Writer) { report.RenderCompact(w, scan.Result{GeneratedAt: fixedTime}, false) })
}

func TestCompactColorOnlyWhenAsked(t *testing.T) {
	var plain, col bytes.Buffer
	report.RenderCompact(&plain, specExample(), false)
	report.RenderCompact(&col, specExample(), true)
	if strings.Contains(plain.String(), "\x1b") {
		t.Error("escapes in plain output")
	}
	for _, code := range []string{"\x1b[31m", "\x1b[33m", "\x1b[32m", "\x1b[36m"} {
		if !strings.Contains(col.String(), code) {
			t.Errorf("missing %q", code)
		}
	}
}

func TestIncompleteLine(t *testing.T) {
	r := specExample()
	r.Incomplete = true
	var buf bytes.Buffer
	report.RenderCompact(&buf, r, false)
	lines := strings.Split(buf.String(), "\n")
	if lines[1] != "Still reading past sessions, so this report may be incomplete." {
		t.Errorf("line 2: %q", lines[1])
	}
}

func TestDetails(t *testing.T) {
	r := specExample()
	r.Items[2].Evidence = item.Evidence{} // transcript, no receipt
	r.Items[0].SecretsPartial = true
	var buf bytes.Buffer
	report.RenderDetails(&buf, r, false)
	out := buf.String()
	for _, want := range []string{
		`session "cleanup" (claude)`, "best guess (no receipt)", "only weak evidence an agent made this",
		"(details only)", "partially scanned", "you can't resume this session after deleting it",
		"sk…aaaa", "~/.claude/jobs/7f3a91c2", "40d old",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("details missing %q", want)
		}
	}
	if strings.Count(out, "you can't resume") != 1 {
		t.Error("resume note only for the transcript")
	}
}

func TestJSONHasNoRawSecret(t *testing.T) {
	r := scan.Result{GeneratedAt: fixedTime, Items: []item.Item{offer(item.Job, "/home/u/.claude/jobs/1", 10)}}
	r.Items[0].Secrets = []secrets.Finding{{Path: "a.json", Line: 1, RuleID: "aws-access-token", Masked: secrets.Mask(fakeAWS)}}
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), fakeAWS) || !strings.Contains(buf.String(), "aws-access-token") {
		t.Errorf("json: %s", buf.String())
	}
	var comp, det bytes.Buffer
	report.RenderCompact(&comp, r, false)
	report.RenderDetails(&det, r, false)
	if strings.Contains(comp.String()+det.String(), fakeAWS) {
		t.Error("raw secret rendered")
	}
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := specExample()
	if err := report.SaveCache(dir, r); err != nil {
		t.Fatal(err)
	}
	got, err := report.LoadCache(dir)
	if err != nil || len(got.Items) != len(r.Items) || !got.GeneratedAt.Equal(r.GeneratedAt) {
		t.Fatalf("%v %+v", err, got)
	}
	fi, _ := os.Stat(filepath.Join(dir, "last-report.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
}

func TestCompactNoOffers(t *testing.T) {
	left := offer(item.Worktree, "/home/u/repo/.claude/worktrees/live", 5)
	left.Decision = item.Decision{Class: item.LeftAlone, Reasons: []string{"session active 4 min ago"}}
	left.Secrets = []secrets.Finding{{Path: ".env", Line: 1, RuleID: "github-pat", Masked: "ghp…1234"}}
	var buf bytes.Buffer
	report.RenderCompact(&buf, scan.Result{GeneratedAt: fixedTime, Items: []item.Item{left}}, false)
	out := buf.String()
	if !strings.HasPrefix(out, "Nothing to clean up right now.  1 contains secrets.\n") {
		t.Errorf("headline: %q", out)
	}
	for _, want := range []string{"ghp…1234  GitHub token", "Still in use, left alone", "1 worktree   session active 4 min ago"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "Dead agent data") {
		t.Error("no dead section without offers")
	}
}

// A secret found in a single-file item has an empty Path: every view must
// print the item's own path instead.
func TestSingleFileSecretShowsItemPath(t *testing.T) {
	it := offer(item.Scratch, "/tmp/notes.txt", 10)
	it.Secrets = []secrets.Finding{{Path: "", Line: 3, RuleID: "aws-access-token", Masked: "AKIA…7Q2X"}}
	r := scan.Result{GeneratedAt: fixedTime, Items: []item.Item{it}}
	var comp, det bytes.Buffer
	report.RenderCompact(&comp, r, false)
	report.RenderDetails(&det, r, false)
	if !strings.Contains(comp.String(), "AKIA…7Q2X  AWS key  /tmp/notes.txt\n") {
		t.Errorf("compact:\n%s", comp.String())
	}
	if !strings.Contains(det.String(), "AWS key  /tmp/notes.txt:3") {
		t.Errorf("details:\n%s", det.String())
	}
}

// Details-only items never reach the compact view, secrets included.
func TestCompactSkipsDetailsOnlySecrets(t *testing.T) {
	weak := offer(item.Scratch, "/tmp/maybe.json", 10)
	weak.Evidence = item.Evidence{Source: "snapshot", Weak: []string{"s1"}}
	weak.Decision = item.Decision{Class: item.LeftAlone, DetailsOnly: true, Reasons: []string{"only weak evidence an agent made this"}}
	weak.Secrets = []secrets.Finding{{Line: 1, RuleID: "aws-access-token", Masked: "AKIA…7Q2X"}}
	var buf bytes.Buffer
	report.RenderCompact(&buf, scan.Result{GeneratedAt: fixedTime, Items: []item.Item{weak}}, false)
	if !strings.HasPrefix(buf.String(), "Nothing left behind.") {
		t.Fatalf("compact:\n%s", buf.String())
	}
}

func TestHeadlineSecretGrammar(t *testing.T) {
	one := item.Item{ID: "a", Category: item.Job, Path: "/x/a", Size: 10, Decision: item.Decision{Class: item.Offer},
		Secrets: []secrets.Finding{{Path: "k", Line: 1, RuleID: "aws-access-token", Masked: "AKIA…QQQQ"}}}
	two := one
	two.ID, two.Path = "b", "/x/b"
	for _, c := range []struct {
		items []item.Item
		want  string
	}{{[]item.Item{one}, "1 contains secrets."}, {[]item.Item{one, two}, "2 contain secrets."}} {
		var buf bytes.Buffer
		report.RenderCompact(&buf, scan.Result{GeneratedAt: fixedTime, Items: c.items}, false)
		if first := strings.SplitN(buf.String(), "\n", 2)[0]; !strings.HasSuffix(first, c.want) {
			t.Errorf("headline %q, want suffix %q", first, c.want)
		}
	}
	if got := report.SecretLabel(""); got != "secret" {
		t.Errorf("SecretLabel(\"\") = %q", got)
	}
}
