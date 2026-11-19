package receipts

import (
	"sort"
	"time"
)

type Session struct {
	ID, Agent, Name, Cwd, TranscriptPath string
	Started, Ended                       time.Time
	PID                                  int
	PIDStart                             time.Time
}

type Index struct {
	sessions map[string]Session
	strong   []Receipt
	weak     []Receipt
}

// BuildIndex folds receipts (in file order) into lookup structures.
func BuildIndex(rs []Receipt) *Index {
	ix := &Index{sessions: map[string]Session{}}
	for _, r := range rs {
		switch r.Kind {
		case KindSessionStart, KindSessionEnd:
			s := ix.sessions[r.SessionID]
			s.ID = r.SessionID
			if r.Agent != "" {
				s.Agent = r.Agent
			}
			if r.SessionName != "" {
				s.Name = r.SessionName
			}
			if r.Cwd != "" {
				s.Cwd = r.Cwd
			}
			if r.TranscriptPath != "" {
				s.TranscriptPath = r.TranscriptPath
			}
			if r.Kind == KindSessionStart {
				if s.Started.IsZero() {
					s.Started = r.At
				}
				s.Ended = time.Time{} // resumed
				if r.PID != 0 {
					s.PID, s.PIDStart = r.PID, r.PIDStart
				}
			} else {
				s.Ended = r.At
			}
			ix.sessions[r.SessionID] = s
		case KindCreated:
			ix.strong = append(ix.strong, r)
		case KindWeakCreated:
			ix.weak = append(ix.weak, r)
		}
	}
	return ix
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (ix *Index) Sessions() map[string]Session { return ix.sessions }

// StrongCreator returns a created receipt whose path equals path or is a
// directory ancestor of it. The latest matching receipt wins.
func (ix *Index) StrongCreator(path string) (Receipt, bool) {
	for i := len(ix.strong) - 1; i >= 0; i-- {
		r := ix.strong[i]
		if r.Path == path || (r.IsDir && Within(path, r.Path)) {
			return r, true
		}
	}
	return Receipt{}, false
}

// WeakCreators returns the de-duplicated session IDs (SessionID plus Sessions)
// of weak receipts whose path equals path or is a directory ancestor of it.
func (ix *Index) WeakCreators(path string) []string {
	var out []string
	add := func(id string) {
		if id != "" && !contains(out, id) {
			out = append(out, id)
		}
	}
	for _, r := range ix.weak {
		if r.Path == path || (r.IsDir && Within(path, r.Path)) {
			add(r.SessionID)
			for _, id := range r.Sessions {
				add(id)
			}
		}
	}
	return out
}

// Cwds returns the distinct, sorted session working directories.
func (ix *Index) Cwds() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ix.sessions {
		if s.Cwd != "" && !seen[s.Cwd] {
			seen[s.Cwd] = true
			out = append(out, s.Cwd)
		}
	}
	sort.Strings(out)
	return out
}

// CreatedUnder returns strong receipts whose path is inside any of dirs.
func (ix *Index) CreatedUnder(dirs []string) []Receipt {
	var out []Receipt
	for _, r := range ix.strong {
		for _, d := range dirs {
			if Within(r.Path, d) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// CreatorExact returns the latest strong receipt whose Path is exactly path.
// Unlike StrongCreator it never matches an ancestor directory receipt.
func (ix *Index) CreatorExact(path string) (Receipt, bool) {
	for i := len(ix.strong) - 1; i >= 0; i-- {
		if ix.strong[i].Path == path {
			return ix.strong[i], true
		}
	}
	return Receipt{}, false
}

// WeakCreatorsExact is WeakCreators restricted to receipts whose Path is
// exactly path.
func (ix *Index) WeakCreatorsExact(path string) []string {
	var out []string
	add := func(id string) {
		if id != "" && !contains(out, id) {
			out = append(out, id)
		}
	}
	for _, r := range ix.weak {
		if r.Path == path {
			add(r.SessionID)
			for _, id := range r.Sessions {
				add(id)
			}
		}
	}
	return out
}
