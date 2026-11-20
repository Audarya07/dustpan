package cmdparse_test

import (
	"reflect"
	"testing"

	"github.com/Audarya07/dustpan/internal/cmdparse"
)

func TestOutputPaths(t *testing.T) {
	cases := []struct {
		cmd, cwd string
		want     []cmdparse.Output
	}{
		{"go build -o /tmp/my-bin ./cmd/x", "/r", []cmdparse.Output{{"/tmp/my-bin", false, "-o"}}},
		{"cd /tmp && go build -o my-main-bin ./...", "/r", []cmdparse.Output{{"/tmp/my-main-bin", false, "-o"}}},
		{"git -C /r/demo-app worktree add -b feature-1234 /tmp/wt-931 origin/main", "/x", []cmdparse.Output{{"/tmp/wt-931", true, "git worktree add"}}},
		// ".." is never resolved lexically (a symlinked component could make
		// it mean somewhere else), so such paths and cds are dropped (R30).
		{"git worktree add ../wt", "/r/repo", nil},
		{"mkdir /tmp/a/../b", "/r", nil},
		{"cd .. && mkdir x", "/r/repo", nil},
		{"mkdir x", "/r/../s", nil},
		{"git -C ../o worktree add wt", "/r/repo", nil},
		{"echo hi > /tmp/a.txt 2>/dev/null", "/r", []cmdparse.Output{{"/tmp/a.txt", false, ">"}}},
		{"cp alpha.go /tmp/alpha.go", "/r", []cmdparse.Output{{"/tmp/alpha.go", false, "cp"}}},
		{"mkdir -p /tmp/imgs-snap", "/r", []cmdparse.Output{{"/tmp/imgs-snap", true, "mkdir"}}},
		{"gh pr view 485 | tee /tmp/pr101.md", "/r", []cmdparse.Output{{"/tmp/pr101.md", false, "tee"}}},
		{"go build -o $OUT ./...", "/r", nil},
		{"mktemp -d /tmp/x.XXXX", "/r", nil},
		{"echo 'unterminated", "/r", nil},
		// extra forms
		{"curl --output=/tmp/o.bin u", "/r", []cmdparse.Output{{"/tmp/o.bin", false, "-o"}}},
		{"curl --output /tmp/o2.bin u", "/r", []cmdparse.Output{{"/tmp/o2.bin", false, "-o"}}},
		{"gcc -o/tmp/o3 a.c", "/r", []cmdparse.Output{{"/tmp/o3", false, "-o"}}},
		// Appends never create: the file may well be the user's own (R26).
		{"echo hi >> /tmp/b.txt", "/r", nil},
		{"echo hi &>> /tmp/b2.txt", "/r", nil},
		{"echo hi | tee -a /tmp/d2.txt", "/r", nil},
		{"echo hi | tee -ia /tmp/d3.txt", "/r", nil},
		{"echo hi | tee --append /tmp/d4.txt", "/r", nil},
		{"echo hi &> /tmp/c.txt", "/r", []cmdparse.Output{{"/tmp/c.txt", false, ">"}}},
		{"echo hi | tee -i /tmp/d.txt /dev/null", "/r", []cmdparse.Output{{"/tmp/d.txt", false, "tee"}}},
		{"mv a /tmp/dir/", "/r", []cmdparse.Output{{"/tmp/dir", true, "mv"}}},
		{"echo hi > /tmp/*.txt", "/r", nil},
		{"echo hi > $(mktemp)", "/r", nil},
	}
	for _, c := range cases {
		got := cmdparse.OutputPaths(c.cmd, c.cwd)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v want %+v", c.cmd, got, c.want)
		}
	}
}

func TestOutputPathsNeverPanics(t *testing.T) {
	for _, cmd := range []string{"", "   \n\t ", "echo \"a", "cat <<EOF\nhello > /tmp/x\nEOF", "(((", "&&", "> ", "cp", "tee", "git -C", "go build -o", "cd"} {
		if got := cmdparse.OutputPaths(cmd, "/r"); got != nil {
			t.Errorf("%q: got %+v want nil", cmd, got)
		}
	}
}
