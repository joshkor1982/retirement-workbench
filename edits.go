package main

// Edit handlers: anything you can add, you can change. Each handler takes
// the same fields as its add form, prefilled on the page, so a blank
// required field (a name, a title) keeps the old value instead of wiping it.

import (
	"net/http"
	"slices"
	"strings"
	"time"
)

// editByID applies change to the item with the given ID.
func editByID[T any](items []T, id int, idOf func(T) int, change func(*T)) {
	for i := range items {
		if idOf(items[i]) == id {
			change(&items[i])
			return
		}
	}
}

func field(r *http.Request, k string) string { return strings.TrimSpace(r.FormValue(k)) }

// keep sets *dst to the form value, unless the form left it blank.
func keep(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func (s *Server) routeEdits(mux *http.ServeMux) {
	mux.HandleFunc("POST /medical/conditions/{id}/update", s.conditionUpdate)
	mux.HandleFunc("POST /medical/meds/{id}/update", s.medUpdate)
	mux.HandleFunc("POST /medical/symptoms/{id}/update", s.symptomUpdate)
	mux.HandleFunc("POST /jobs/prospects/{id}/update", s.prospectUpdate)
	mux.HandleFunc("POST /jobs/contacts/{id}/update", s.contactUpdate)
	mux.HandleFunc("POST /jobs/searches/{id}/update", s.jobSearchUpdate)
	mux.HandleFunc("POST /resources/contacts/{id}/update", s.resourceUpdate)
	mux.HandleFunc("POST /resources/links/{id}/update", s.linkUpdate)
	mux.HandleFunc("POST /skillbridge/leads/{id}/update", s.sbLeadUpdate)
	mux.HandleFunc("POST /savings/{id}/update", s.savingsUpdate)
	mux.HandleFunc("POST /budget/{id}/update", s.txnUpdate)
}

func (s *Server) conditionUpdate(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		editByID(st.Conditions, pathID(r), func(c Condition) int { return c.ID }, func(c *Condition) {
			keep(&c.Name, field(r, "name"))
			c.Notes = field(r, "notes")
		})
	})
	http.Redirect(w, r, "/medical", http.StatusSeeOther)
}

func (s *Server) medUpdate(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		editByID(st.Meds, pathID(r), func(m Med) int { return m.ID }, func(m *Med) {
			keep(&m.Name, field(r, "name"))
			m.Dose, m.For, m.Prescriber, m.Notes = field(r, "dose"), field(r, "for"), field(r, "prescriber"), field(r, "notes")
		})
	})
	http.Redirect(w, r, "/medical", http.StatusSeeOther)
}

func (s *Server) symptomUpdate(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		editByID(st.Symptoms, pathID(r), func(x Symptom) int { return x.ID }, func(x *Symptom) {
			keep(&x.Note, field(r, "note"))
			keep(&x.Date, field(r, "date"))
			x.Condition = field(r, "condition")
		})
	})
	http.Redirect(w, r, "/medical", http.StatusSeeOther)
}

func (s *Server) prospectUpdate(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		editByID(st.Prospects, pathID(r), func(p Prospect) int { return p.ID }, func(p *Prospect) {
			keep(&p.Title, field(r, "title"))
			p.Org, p.URL, p.Notes = field(r, "org"), field(r, "url"), field(r, "notes")
			if v := field(r, "status"); slices.Contains(prospectFlow, v) {
				p.Status = v
			}
		})
	})
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (s *Server) contactUpdate(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		editByID(st.Contacts, pathID(r), func(c Contact) int { return c.ID }, func(c *Contact) {
			keep(&c.Name, field(r, "name"))
			c.Org, c.Role, c.Info, c.Notes = field(r, "org"), field(r, "role"), field(r, "info"), field(r, "notes")
		})
	})
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (s *Server) jobSearchUpdate(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		editByID(st.JobSearches, pathID(r), func(j JobSearch) int { return j.ID }, func(j *JobSearch) {
			keep(&j.Keyword, field(r, "keyword"))
			jq := parseJobQuery(j.Keyword, r.FormValue("zip"), r.FormValue("radius"), r.FormValue("mode"), r.FormValue("location"))
			j.Location, j.Zip, j.Radius, j.Mode = jq.Where, jq.Zip, jq.Radius, jq.Mode
		})
	})
	http.Redirect(w, r, "/jobs", http.StatusSeeOther)
}

func (s *Server) resourceUpdate(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		editByID(st.Resources, pathID(r), func(x Resource) int { return x.ID }, func(x *Resource) {
			keep(&x.Name, field(r, "name"))
			x.Org, x.Info, x.Notes = field(r, "org"), field(r, "info"), field(r, "notes")
		})
	})
	http.Redirect(w, r, "/resources", http.StatusSeeOther)
}

func (s *Server) linkUpdate(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		editByID(st.Links, pathID(r), func(l Link) int { return l.ID }, func(l *Link) {
			keep(&l.Title, field(r, "title"))
			if u := field(r, "url"); u != "" {
				if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
					u = "https://" + u
				}
				l.URL = u
			}
			keep(&l.Category, field(r, "category"))
			l.Notes = field(r, "notes")
		})
	})
	http.Redirect(w, r, "/resources", http.StatusSeeOther)
}

func (s *Server) sbLeadUpdate(w http.ResponseWriter, r *http.Request) {
	_ = s.store.mutate(func(st *State) {
		editByID(st.SBLeads, pathID(r), func(l SBLead) int { return l.ID }, func(l *SBLead) {
			keep(&l.Company, field(r, "company"))
			l.Program, l.Location, l.URL, l.Notes = field(r, "program"), field(r, "location"), field(r, "url"), field(r, "notes")
			if v := field(r, "status"); slices.Contains(sbFlow, v) {
				l.Status = v
			}
		})
	})
	http.Redirect(w, r, "/skillbridge#leads", http.StatusSeeOther)
}

// signedAmount reads an amount and applies the sign from the kind picker:
// withdrawals and spending are negative.
func signedAmount(r *http.Request, negKind string) (int64, bool) {
	amt, err := parseMoney(r.FormValue("amount"))
	if err != nil || amt == 0 {
		return 0, false
	}
	if amt < 0 {
		amt = -amt
	}
	if r.FormValue("kind") == negKind {
		amt = -amt
	}
	return amt, true
}

func validDay(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func (s *Server) savingsUpdate(w http.ResponseWriter, r *http.Request) {
	amt, ok := signedAmount(r, "withdraw")
	if !ok {
		flash(w, "err", "That amount is not a dollar amount. Try something like 500.")
		http.Redirect(w, r, "/savings", http.StatusSeeOther)
		return
	}
	_ = s.store.mutate(func(st *State) {
		editByID(st.Savings, pathID(r), func(e SavingsEntry) int { return e.ID }, func(e *SavingsEntry) {
			e.Amount, e.Note = amt, field(r, "note")
			if d := field(r, "date"); validDay(d) {
				e.Date = d
			}
		})
	})
	http.Redirect(w, r, "/savings", http.StatusSeeOther)
}

func (s *Server) txnUpdate(w http.ResponseWriter, r *http.Request) {
	amt, ok := signedAmount(r, "spend")
	if !ok {
		flash(w, "err", "That amount is not a dollar amount. Try something like 84.99.")
		http.Redirect(w, r, "/budget", http.StatusSeeOther)
		return
	}
	_ = s.store.mutate(func(st *State) {
		editByID(st.Txns, pathID(r), func(t Txn) int { return t.ID }, func(t *Txn) {
			t.Amount, t.Category, t.Note = amt, field(r, "category"), field(r, "note")
			if d := field(r, "date"); validDay(d) {
				t.Date = d
			}
		})
	})
	http.Redirect(w, r, "/budget", http.StatusSeeOther)
}
