// Package item defines the model shared by collectors, the safety engine and
// the UI. It holds types and two small helpers only.
package item

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Audarya07/agent-receipts/internal/secrets"
)

// Category is the kind of leftover.
type Category string

const (
	Worktree   Category = "worktree"
	Job        Category = "job"
	Scratch    Category = "scratch"
	Transcript Category = "transcript"
)

// Evidence says how we know an agent created an item.
type Evidence struct {
	Strong      bool
	Source      string // "agent-folder" | "receipt" | "transcript" | "snapshot" | ""
	SessionID   string
	SessionName string
	Sessions    []string // other sessions whose receipts were merged into this item
	Agent       string
	Weak        []string
}

// Class is the safety verdict.
type Class string

const (
	Offer     Class = "offer"
	LeftAlone Class = "left-alone"
	Hidden    Class = "hidden"
)

// Decision is the outcome of the safety engine for one item.
type Decision struct {
	Class       Class
	Reasons     []string
	DetailsOnly bool
	Permanent   bool
}

// Item is one leftover found by a collector. Path is canonical.
type Item struct {
	ID             string
	Category       Category
	Path           string
	Repo, Branch   string
	Size           int64
	Newest         time.Time
	Evidence       Evidence
	DeadReason     string
	Unregister     bool // worktree whose directory is already gone
	Decision       Decision
	Secrets        []secrets.Finding
	SecretsPartial bool
	Fingerprint    string
}

// MakeID returns the first 8 hex chars of sha256(category + NUL + path).
func MakeID(c Category, path string) string {
	sum := sha256.Sum256([]byte(string(c) + "\x00" + path))
	return hex.EncodeToString(sum[:])[:8]
}

// Fingerprint summarizes the state of an item so a later run can tell whether
// it changed. headSHA is empty for non-worktrees.
func Fingerprint(size int64, newest time.Time, headSHA string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d\x00%s", size, newest.UnixNano(), headSHA)))
	return hex.EncodeToString(sum[:])
}
