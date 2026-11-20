// Package secrets scans leftover files for credentials with the gitleaks
// engine. No struct, output or file produced here ever holds a raw secret:
// values are reduced with Mask before they leave this package.
package secrets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zricethezav/gitleaks/v8/detect"
)

const (
	defaultMaxFileBytes = 5_000_000
	defaultMaxFiles     = 5000
	binarySniffBytes    = 8 * 1024
)

// Finding locates a secret without ever holding its value.
type Finding struct {
	Path        string // relative to the scan root; "" when the root is the file itself
	Line        int
	RuleID      string
	Description string
	Masked      string
}

// Scanner scans a directory tree for secrets.
type Scanner struct {
	MaxFileBytes int64
	MaxFiles     int

	detector *detect.Detector
}

// New builds a Scanner with gitleaks' default rules and default limits.
func New() (*Scanner, error) {
	d, err := detect.NewDetectorDefaultConfig()
	if err != nil {
		return nil, err
	}
	return &Scanner{MaxFileBytes: defaultMaxFileBytes, MaxFiles: defaultMaxFiles, detector: d}, nil
}

var errFileCap = errors.New("file cap reached")

// Scan walks root and reports findings. It never follows symlinks (a symlink
// root and symlinked files are skipped unread), skips .git, files over
// MaxFileBytes and files that look binary. A regular-file root is scanned
// itself; its findings have an empty Path. partial is true when the MaxFiles
// cap stopped the scan or when the root or any entry could not be read, so a
// clean result never means "couldn't look". skip, if non-nil, may exclude
// files or directories by root-relative path.
func (s *Scanner) Scan(ctx context.Context, root string, skip func(rel string, d fs.DirEntry) bool) (findings []Finding, partial bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	info, lerr := os.Lstat(root)
	switch {
	case lerr != nil:
		return nil, true, nil
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, false, nil // the link itself is the item; its target is never read
	case info.Mode().IsRegular():
		found, ok := s.scanFile(root, "")
		return found, !ok, nil
	case !info.IsDir():
		return nil, false, nil // fifo, socket, device: nothing to read
	}
	files := 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if werr != nil {
			partial = true // unreadable entry: skipped, so the scan is incomplete
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || (skip != nil && skip(rel, d)) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		if skip != nil && skip(rel, d) {
			return nil
		}
		if files >= s.MaxFiles {
			return errFileCap
		}
		files++
		found, ok := s.scanFile(path, rel)
		findings = append(findings, found...)
		if !ok {
			partial = true
		}
		return nil
	})
	switch {
	case errors.Is(walkErr, errFileCap):
		return findings, true, nil
	case walkErr != nil:
		return findings, partial, walkErr
	}
	return findings, partial, nil
}

// scanFile scans one file. ok is false when the file could not be read;
// files skipped on purpose (too large, binary, not regular) are ok.
func (s *Scanner) scanFile(path, rel string) (out []Finding, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false
	}
	if !info.Mode().IsRegular() || info.Size() > s.MaxFileBytes {
		return nil, true
	}
	data, err := io.ReadAll(io.LimitReader(f, s.MaxFileBytes+1))
	if err != nil {
		return nil, false
	}
	if int64(len(data)) > s.MaxFileBytes {
		return nil, true
	}
	if bytes.IndexByte(data[:min(len(data), binarySniffBytes)], 0) >= 0 {
		return nil, true
	}
	for _, rf := range s.detector.DetectBytes(data) {
		out = append(out, Finding{
			Path:        rel,
			Line:        rf.StartLine + 1, // gitleaks lines are 0-based
			RuleID:      rf.RuleID,
			Description: rf.Description,
			Masked:      Mask(rf.Secret),
		})
	}
	return out, true
}

// Mask reduces a secret to a form that is safe to display.
func Mask(s string) string {
	r := []rune(s)
	switch {
	case len(r) >= 12:
		return string(r[:4]) + "…" + string(r[len(r)-4:])
	case len(r) >= 6:
		return string(r[:2]) + "…"
	default:
		return "…"
	}
}
