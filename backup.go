package main

// Backup and restore: everything you have entered and uploaded, in one zip.
// The zip holds state.json and docs/. The housing cache is left out; it
// rebuilds itself on the next check.
//
// A restore validates the whole zip before touching anything, then sets your
// current data aside as state.before-restore-<time>.json and
// docs.before-restore-<time>/ so a mistaken restore can be undone by hand.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	restoreMaxFiles = 5000
	restoreMaxBytes = 2 << 30 // 2 GiB uncompressed
)

func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	stateJSON, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	name := "retirement-workbench-backup-" + time.Now().Format("2006-01-02") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	if f, err := zw.Create("state.json"); err == nil {
		_, _ = f.Write(stateJSON)
	}
	docs := filepath.Join(s.data, "docs")
	entries, _ := os.ReadDir(docs)
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		src, err := os.Open(filepath.Join(docs, e.Name()))
		if err != nil {
			continue
		}
		hdr := &zip.FileHeader{Name: "docs/" + e.Name(), Method: zip.Deflate}
		if info, err := e.Info(); err == nil {
			hdr.Modified = info.ModTime()
		}
		if f, err := zw.CreateHeader(hdr); err == nil {
			_, _ = io.Copy(f, src)
		}
		src.Close()
	}
}

// readBackup checks a backup zip and returns its state and document entries.
func readBackup(zr *zip.Reader) (State, []*zip.File, error) {
	var st State
	var docs []*zip.File
	var total uint64
	found := false
	if len(zr.File) > restoreMaxFiles {
		return st, nil, fmt.Errorf("the backup holds too many files")
	}
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if total > restoreMaxBytes {
			return st, nil, fmt.Errorf("the backup is too large")
		}
		name := f.Name
		switch {
		case name == "state.json":
			rc, err := f.Open()
			if err != nil {
				return st, nil, err
			}
			b, err := io.ReadAll(io.LimitReader(rc, 256<<20))
			rc.Close()
			if err != nil {
				return st, nil, err
			}
			if err := json.Unmarshal(b, &st); err != nil {
				return st, nil, fmt.Errorf("state.json in the backup is damaged: %v", err)
			}
			found = true
		case strings.HasSuffix(name, "/"):
			// directory entry
		case path.Dir(name) == "docs" && path.Base(name) == name[len("docs/"):] && !strings.Contains(name, "..") && !strings.HasPrefix(path.Base(name), "."):
			docs = append(docs, f)
		default:
			return st, nil, fmt.Errorf("the backup holds an unexpected file: %s", name)
		}
	}
	if !found {
		return st, nil, fmt.Errorf("this is not an ARW backup (no state.json)")
	}
	return st, docs, nil
}

func (s *Server) backupRestore(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		flash(w, "err", "Restore stopped: "+msg+". Nothing was changed.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail("the upload did not arrive")
		return
	}
	file, _, err := r.FormFile("backup")
	if err != nil {
		fail("choose a backup file first")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		fail("the upload did not arrive")
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		fail("that file is not a zip")
		return
	}
	newState, docs, err := readBackup(zr)
	if err != nil {
		fail(err.Error())
		return
	}
	migrateAI(&newState)
	if newState.NextID < 1 {
		newState.NextID = 1
	}

	// Unpack the documents beside the live folder first.
	stamp := time.Now().Format("20060102-150405")
	incoming := filepath.Join(s.data, "docs.incoming-"+stamp)
	if err := os.MkdirAll(incoming, 0o700); err != nil {
		fail(err.Error())
		return
	}
	for _, f := range docs {
		if err := extractTo(f, filepath.Join(incoming, path.Base(f.Name))); err != nil {
			os.RemoveAll(incoming)
			fail("a document would not unpack: " + err.Error())
			return
		}
	}

	// Set the current data aside, then swap the backup in.
	docsDir := filepath.Join(s.data, "docs")
	keptDocs := filepath.Join(s.data, "docs.before-restore-"+stamp)
	keptState := filepath.Join(s.data, "state.before-restore-"+stamp+".json")
	if cur, err := os.ReadFile(s.store.path); err == nil {
		_ = os.WriteFile(keptState, cur, 0o600)
	}
	if err := os.Rename(docsDir, keptDocs); err != nil && !os.IsNotExist(err) {
		os.RemoveAll(incoming)
		fail(err.Error())
		return
	}
	if err := os.Rename(incoming, docsDir); err != nil {
		_ = os.Rename(keptDocs, docsDir)
		fail(err.Error())
		return
	}
	if err := s.store.replace(newState); err != nil {
		_ = os.RemoveAll(docsDir)
		_ = os.Rename(keptDocs, docsDir)
		fail(err.Error())
		return
	}
	s.homes.reset()
	flash(w, "ok", fmt.Sprintf("Restored %d documents and all your data. Your previous data is kept in %s.", len(docs), filepath.Base(keptDocs)))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func extractTo(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(rc, restoreMaxBytes)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// replace swaps in a whole new state, written safely to disk first.
func (s *Store) replace(st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.path, b); err != nil {
		return err
	}
	s.st = st
	return nil
}
