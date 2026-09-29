package main

import "net/http"

// quitApp stops ARW from its own Settings page: the only off switch a
// double-clicked Mac app has.
func (s *Server) quitApp(w http.ResponseWriter, r *http.Request) {
	if s.quit == nil {
		http.Error(w, "quit is not available here", http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>ARW stopped</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>body{font-family:system-ui,sans-serif;background:#09090b;color:#e4e4e7;display:grid;place-items:center;height:100vh;margin:0}
div{text-align:center}b{color:#f4c542;font-size:28px;letter-spacing:.06em}p{color:#a1a1aa}</style></head>
<body><div><b>ARW</b><h1>ARW has stopped</h1><p>Your data is saved. You can close this tab.<br>Open ARW again whenever you need it.</p></div></body></html>`))
	s.quit()
}
