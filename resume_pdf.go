package main

// Resume as PDF. The print page renders the resume in its chosen design at
// US Letter size, colors included. For a one-click download the app asks a
// Chromium browser on this computer (Chrome, Edge, Brave, or Chromium) to
// print that page to PDF in the background. With no such browser, the print
// page opens in yours and you choose Save as PDF.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// findBrowser returns a Chromium-based browser that can print to PDF, or "".
func findBrowser() string {
	if p := os.Getenv("RW_BROWSER"); p != "" && fileExists(p) {
		return p
	}
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		for _, app := range []string{"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"Brave Browser.app/Contents/MacOS/Brave Browser",
			"Chromium.app/Contents/MacOS/Chromium"} {
			cands = append(cands, filepath.Join("/Applications", app))
			if home, err := os.UserHomeDir(); err == nil {
				cands = append(cands, filepath.Join(home, "Applications", app))
			}
		}
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if base == "" {
				continue
			}
			cands = append(cands,
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `BraveSoftware\Brave-Browser\Application\brave.exe`))
		}
	default:
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
			if p, err := exec.LookPath(name); err == nil {
				cands = append(cands, p)
			}
		}
	}
	for _, c := range cands {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func (s *Server) resumeView(r *http.Request) (map[string]any, *ResumeTarget, bool) {
	st := s.store.snapshot()
	id, _ := strconv.Atoi(r.URL.Query().Get("t"))
	t := st.findTarget(id)
	if t == nil && len(st.ResumeTargets) > 0 {
		t = &st.ResumeTargets[0]
	}
	if t == nil {
		return nil, nil, false
	}
	title := strings.TrimSpace(st.Settings.Name + " Resume")
	if t.Position != "" {
		title += " - " + t.Position
	}
	return map[string]any{"Settings": st.Settings, "Active": t, "Title": title}, t, true
}

func resumeFilename(name, position string) string {
	parts := []string{}
	for _, p := range []string{name, position, "Resume"} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return safeFilename(strings.Join(parts, " ")) + ".pdf"
}

// resumePrint serves the print page. ?manual=1 opens the print dialog on
// load, for computers without a Chromium browser.
func (s *Server) resumePrint(w http.ResponseWriter, r *http.Request) {
	data, _, ok := s.resumeView(r)
	if !ok {
		http.Redirect(w, r, "/resume", http.StatusSeeOther)
		return
	}
	data["Manual"] = r.URL.Query().Get("manual") == "1"
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "resume_print.html", data); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// resumePDF prints the print page to a PDF and downloads it.
func (s *Server) resumePDF(w http.ResponseWriter, r *http.Request) {
	data, t, ok := s.resumeView(r)
	if !ok {
		http.Redirect(w, r, "/resume", http.StatusSeeOther)
		return
	}
	printURL := fmt.Sprintf("http://%s/resume/print?t=%d", r.Host, t.ID)
	pdf, err := printToPDF(r.Context(), printURL)
	if err != nil {
		// No browser here (or it failed): let the user's own browser save it.
		http.Redirect(w, r, fmt.Sprintf("/resume/print?t=%d&manual=1", t.ID), http.StatusSeeOther)
		return
	}
	st := data["Settings"].(Settings)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+resumeFilename(st.Name, t.Position)+`"`)
	_, _ = w.Write(pdf)
}

func printToPDF(ctx context.Context, pageURL string) ([]byte, error) {
	bin := findBrowser()
	if bin == "" {
		return nil, fmt.Errorf("no Chromium browser found")
	}
	work, err := os.MkdirTemp("", "rw-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	out := filepath.Join(work, "resume.pdf")
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+filepath.Join(work, "profile"), // never touches your own browser profile
		"--no-pdf-header-footer", "--print-to-pdf-no-header",
		"--virtual-time-budget=4000", // let the fonts load before printing
		"--print-to-pdf="+out, pageURL)
	ownProcessGroup(cmd)
	// Chrome leaves helpers (the crash reporter) running that inherit its
	// output. A pipe would make Wait block until they exit, so output goes
	// to a file, and whatever is left of the group is killed afterwards.
	logf, err := os.Create(filepath.Join(work, "browser.log"))
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Headless Chrome can linger after writing the file, so stop it as soon
	// as a complete PDF (one that ends in %%EOF) is on disk.
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	var runErr error
wait:
	for {
		select {
		case runErr = <-done:
			break wait
		case <-ctx.Done():
			runErr = ctx.Err()
			break wait
		case <-tick.C:
			if b, err := os.ReadFile(out); err == nil && bytes.Contains(b[max(0, len(b)-64):], []byte("%%EOF")) {
				break wait
			}
		}
	}
	killGroup(cmd)
	if runErr != nil && !fileExists(out) {
		msg, _ := os.ReadFile(filepath.Join(work, "browser.log"))
		return nil, fmt.Errorf("browser print failed: %v %s", runErr, firstN(string(msg), 200))
	}
	b, err := os.ReadFile(out)
	if err != nil || !bytes.HasPrefix(b, []byte("%PDF")) {
		return nil, fmt.Errorf("browser produced no PDF")
	}
	return b, nil
}
