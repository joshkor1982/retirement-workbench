package main

// Notes: a plain notebook. Each note is a date, a topic, and free text. The
// page lists them newest first as closed cards showing date and topic; a card
// opens to read or edit and closes again on save.

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Note struct {
	ID    int    `json:"id"`
	Date  string `json:"date"` // YYYY-MM-DD
	Topic string `json:"topic"`
	Body  string `json:"body"`
}

func (s *Server) notes(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	notes := st.Notes
	sort.SliceStable(notes, func(i, j int) bool {
		if notes[i].Date != notes[j].Date {
			return notes[i].Date > notes[j].Date
		}
		return notes[i].ID > notes[j].ID
	})
	open, _ := strconv.Atoi(r.URL.Query().Get("open"))
	s.page(w, "notes", map[string]any{
		"Notes": notes, "Today": time.Now().Format("2006-01-02"), "Open": open,
	})
}

func (s *Server) noteAdd(w http.ResponseWriter, r *http.Request) {
	topic := strings.TrimSpace(r.FormValue("topic"))
	date := strings.TrimSpace(r.FormValue("date"))
	if _, err := time.Parse("2006-01-02", date); err != nil {
		date = time.Now().Format("2006-01-02")
	}
	if topic == "" {
		topic = "Untitled"
	}
	id := 0
	_ = s.store.mutate(func(st *State) {
		id = st.id()
		st.Notes = append(st.Notes, Note{ID: id, Date: date, Topic: topic, Body: strings.TrimSpace(r.FormValue("body"))})
	})
	// Land on the new note, open and ready to type in.
	http.Redirect(w, r, "/notes?open="+strconv.Itoa(id)+"#note-"+strconv.Itoa(id), http.StatusSeeOther)
}

// noteUpdate saves with fetch (204, no reload); without JS it redirects back.
func (s *Server) noteUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		for i := range st.Notes {
			n := &st.Notes[i]
			if n.ID != id {
				continue
			}
			if t := strings.TrimSpace(r.FormValue("topic")); t != "" {
				n.Topic = t
			}
			if d := strings.TrimSpace(r.FormValue("date")); d != "" {
				if _, err := time.Parse("2006-01-02", d); err == nil {
					n.Date = d
				}
			}
			n.Body = strings.TrimRight(r.FormValue("body"), " \t\r\n")
		}
	})
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/notes#note-"+strconv.Itoa(id), http.StatusSeeOther)
}

func (s *Server) noteDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		st.Notes = deleteByID(st.Notes, id, func(n Note) int { return n.ID })
	})
	http.Redirect(w, r, "/notes", http.StatusSeeOther)
}
