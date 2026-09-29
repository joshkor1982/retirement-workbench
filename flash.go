package main

// Flash messages: one-shot notices that survive the redirect after a form
// post. The server sets a short-lived cookie; shell.js shows it as a toast
// and deletes it. Plain text only; the page escapes it.

import (
	"net/http"
	"net/url"
	"strings"
)

const flashCookie = "rw_flash"

// flash queues a toast for the next page. kind is "ok" or "err".
func flash(w http.ResponseWriter, kind, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: url.QueryEscape(kind + "|" + msg),
		Path: "/", MaxAge: 30, SameSite: http.SameSiteStrictMode,
	})
}

// optionalMoney parses an amount that may be left blank. Blank is zero and
// fine; anything else must be a real dollar amount.
func optionalMoney(s string) (int64, bool) {
	if strings.TrimSpace(s) == "" {
		return 0, true
	}
	v, err := parseMoney(s)
	return v, err == nil
}
