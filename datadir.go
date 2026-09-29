package main

// Where your data lives. The app's code and your data never share a folder by
// default: the source tree is safe to fork, publish, or delete, and your
// records stay on this machine in your own user folder.
//
// Resolution order, first match wins:
//
//  1. -data <dir> on the command line
//  2. RW_DATA=<dir> in the environment
//  3. ./data, if it already holds a state.json (installs from before this change)
//  4. the per-user app data folder:
//     macOS   ~/Library/Application Support/RetirementWorkbench
//     Linux   $XDG_CONFIG_HOME/RetirementWorkbench (usually ~/.config/...)
//     Windows %AppData%\RetirementWorkbench

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

func resolveDataDir(flagVal string) (dir, why string) {
	if flagVal != "" {
		return flagVal, "-data flag"
	}
	if env := strings.TrimSpace(os.Getenv("RW_DATA")); env != "" {
		return env, "RW_DATA"
	}
	if fileExists(filepath.Join("data", "state.json")) {
		return "data", "legacy ./data folder"
	}
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "RetirementWorkbench"), "per-user app data folder"
	}
	return "data", "fallback ./data folder"
}

// relDoc turns an absolute or cwd-relative upload path into one relative to
// the data folder, so the folder can move without breaking a single document.
func (s *Server) relDoc(p string) string {
	if rel, err := filepath.Rel(s.data, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

// docPath resolves a stored document path. New records are relative to the
// data folder ("docs/x.pdf"); older ones carry the folder name ("data/docs/x.pdf"),
// so anything under a docs/ segment is re-anchored to wherever the data lives now.
func (s *Server) docPath(stored string) string {
	if filepath.IsAbs(stored) && fileExists(stored) {
		return stored
	}
	slash := filepath.ToSlash(stored)
	if i := strings.LastIndex(slash, "docs/"); i >= 0 && (i == 0 || slash[i-1] == '/') {
		return filepath.Join(s.data, filepath.FromSlash(slash[i:]))
	}
	return filepath.Join(s.data, stored)
}

// safeFilename makes a name safe for a Content-Disposition header.
func safeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "download"
	}
	return b.String()
}

// tightenPerms makes the data folder private to you: folders 0700, files
// 0600. Older builds created them world-readable; medical and money records
// should not be visible to other accounts on a shared computer.
func tightenPerms(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			_ = os.Chmod(p, 0o700)
		} else if d.Type().IsRegular() {
			_ = os.Chmod(p, 0o600)
		}
		return nil
	})
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// Dates are calendar days in YOUR time zone. time.Parse would put them at
// UTC midnight, which makes "due today" read as overdue after 7 PM in the US.
func parseDay(s string) (time.Time, error) { return time.ParseInLocation("2006-01-02", s, time.Local) }

func localDay(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

// daysBetween counts calendar days from a to b, immune to DST's 23 and 25 hour days.
func daysBetween(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return int(time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC).Sub(time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)).Hours() / 24)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
