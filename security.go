package main

// Request hardening for a loopback app.
//
// Binding to 127.0.0.1 keeps the network out, but not the browser: any web
// page you visit can make your browser send requests to 127.0.0.1:5252. Two
// attacks follow, and this file closes both.
//
//   - Cross-site request forgery: a page auto-submits a form to
//     /settings/erase. Every state-changing request must come from this app's
//     own pages, which browsers prove with Sec-Fetch-Site and Origin.
//   - DNS rebinding: a hostile domain re-points itself at 127.0.0.1 so the
//     browser treats the app as same-origin with the attacker. The Host
//     header still carries the attacker's name, so only loopback names pass.

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const maxBodyBytes = 512 << 20 // bulk scans and packet PDFs are large; anything bigger is a mistake

func hostOnly(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return strings.Trim(strings.ToLower(host), "[]")
}

func loopbackHost(hostport string) bool {
	host := hostOnly(hostport)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// extraHosts are hostnames you deliberately serve the app under, such as an
// ingress behind a login proxy: RW_ALLOWED_HOSTS=workbench.example.com,other.
func extraHosts() []string {
	var out []string
	for _, h := range strings.Split(os.Getenv("RW_ALLOWED_HOSTS"), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func allowedHost(hostport string) bool {
	if loopbackHost(hostport) {
		return true
	}
	host := hostOnly(hostport)
	for _, h := range extraHosts() {
		if host == h {
			return true
		}
	}
	return false
}

// sameOriginWrite reports whether a state-changing request came from this
// app's own pages. Browsers send Sec-Fetch-Site on every request; Origin is
// the fallback for older ones. Non-browser clients (curl, scripts on this
// machine) send neither and are allowed: they already run as you.
func sameOriginWrite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "cross-site", "same-site":
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return origin == ""
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && allowedHost(u.Host)
}

func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Liveness probes arrive addressed to the pod IP and carry no data.
		if r.URL.Path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "ok\n")
			return
		}
		if !allowedHost(r.Host) {
			http.Error(w, "this app only answers on localhost", http.StatusMisdirectedRequest)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !sameOriginWrite(r) {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
