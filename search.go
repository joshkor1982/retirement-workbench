package main

// Quick search (Cmd+K or Ctrl+K on any page): one box that finds pages, tasks,
// notes, documents, appointments, contacts, prospects, conditions, bills,
// debts, and saved links. Matching is plain case-insensitive substring on
// every word, ranked title-first.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type searchHit struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Sub   string `json:"sub,omitempty"`
	Href  string `json:"href"`
	Ext   bool   `json:"ext,omitempty"` // opens outside the app
	score int
}

// matchScore: 0 = no match; higher is better. Every word must appear.
func matchScore(words []string, title, rest string) int {
	t, r := strings.ToLower(title), strings.ToLower(rest)
	score := 1
	for _, w := range words {
		switch {
		case strings.HasPrefix(t, w):
			score += 4
		case strings.Contains(t, w):
			score += 3
		case strings.Contains(r, w):
			score++
		default:
			return 0
		}
	}
	return score
}

func quickSearch(st State, q string) []searchHit {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return nil
	}
	var hits []searchHit
	add := func(kind, title, sub, href, rest string, ext bool) {
		if sc := matchScore(words, title, sub+" "+rest); sc > 0 {
			hits = append(hits, searchHit{Kind: kind, Title: title, Sub: sub, Href: href, Ext: ext, score: sc})
		}
	}
	id := strconv.Itoa
	for _, g := range sideNav {
		for _, it := range g.Items {
			add("Page", it.Label, g.Label, it.Href, "", false)
		}
	}
	add("Page", "Settings", "", "/settings", "backup restore advisor leave", false)
	for _, t := range st.Todos {
		sub := t.Due
		if t.Done {
			sub += " · done"
		}
		add("To-Do", t.Title, sub, "/todos#todo-"+id(t.ID), t.Notes+" "+t.Phase, false)
	}
	for _, n := range st.Notes {
		add("Note", n.Topic, n.Date, "/notes?open="+id(n.ID)+"#note-"+id(n.ID), n.Body, false)
	}
	for _, d := range st.Docs {
		add("Document", d.Name, d.Kind, "/docs/"+id(d.ID)+"/file", d.Notes, true)
	}
	for _, a := range st.Appointments {
		add("Appointment", a.Title, a.At, "/appointments#appt-"+id(a.ID), a.Place+" "+a.Notes, false)
	}
	for _, c := range st.Contacts {
		add("Contact", c.Name, strings.Trim(c.Org+" · "+c.Role, " ·"), "/jobs", c.Info+" "+c.Notes, false)
	}
	for _, p := range st.Prospects {
		add("Job", p.Title, p.Org, "/jobs", p.Notes, false)
	}
	for _, c := range st.Conditions {
		add("Condition", c.Name, "Medical / VA", "/medical", c.Notes, false)
	}
	for _, b := range st.Bills {
		add("Bill", b.Name, money(b.Amount)+" a month", "/budget", "", false)
	}
	for _, d := range st.Debts {
		add("Debt", d.Name, money(d.Balance)+" at "+d.APR, "/debt", "", false)
	}
	for _, l := range st.Links {
		add("Link", l.Title, l.Category, l.URL, l.Notes, true)
	}
	coName := map[int]string{}
	for _, c := range st.Companies {
		coName[c.ID] = c.Name
		add("Company", c.Name, c.Role, "/learning#co-"+id(c.ID), c.Notes, false)
	}
	for _, sk := range st.Skills {
		add("Skill", sk.Name, strings.Trim(coName[sk.CompanyID]+" · "+sk.Area, " ·"), "/learning#co-"+id(sk.CompanyID), sk.Notes, false)
	}
	for _, r := range st.Resources {
		add("Contact", r.Name, r.Org, "/resources", r.Info+" "+r.Notes, false)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > 12 {
		hits = hits[:12]
	}
	return hits
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	hits := quickSearch(s.store.snapshot(), r.URL.Query().Get("q"))
	if hits == nil {
		hits = []searchHit{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(hits)
}
