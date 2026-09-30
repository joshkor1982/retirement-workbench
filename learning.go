package main

// Learning: what to master for each company you want to work for. You add
// the companies and the skills; each skill moves To Learn, Learning,
// Mastered, and each company shows how far along you are. Skills group by
// area (protocols, platform, tools) so a long list stays readable.

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type Company struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Role  string `json:"role"` // the job you are aiming for there
	URL   string `json:"url"`
	Notes string `json:"notes"`
	// TargetID is the tailored resume for this company, 0 when there is none.
	// Adding a company starts one, and starting a resume adds a company.
	TargetID int    `json:"target_id,omitempty"`
	Fit      string `json:"fit,omitempty"` // the last Generate's read on what you bring and what is missing
	FitAt    string `json:"fit_at,omitempty"`
}

type Skill struct {
	ID        int    `json:"id"`
	CompanyID int    `json:"company_id"`
	Name      string `json:"name"`
	Area      string `json:"area"` // grouping, e.g. "Protocols"
	URL       string `json:"url"`  // where to learn it
	Notes     string `json:"notes"`
	Status    string `json:"status"` // learn | learning | mastered
}

var skillFlow = []string{"learn", "learning", "mastered"}

var skillLabels = map[string]string{"learn": "To Learn", "learning": "Learning", "mastered": "Mastered"}

type skillArea struct {
	Name   string
	Skills []Skill
}

type companyView struct {
	Company
	Target   *ResumeTarget
	Areas    []skillArea
	Total    int
	Learning int
	Mastered int
	Pct      int
}

// learningView groups each company's skills by area, in the order the areas
// were first used, with a progress count.
func learningView(st State) []companyView {
	var out []companyView
	for _, c := range st.Companies {
		v := companyView{Company: c, Target: st.findTarget(c.TargetID)}
		idx := map[string]int{}
		for _, sk := range st.Skills {
			if sk.CompanyID != c.ID {
				continue
			}
			area := cmpOr(sk.Area, "General")
			i, ok := idx[area]
			if !ok {
				i = len(v.Areas)
				idx[area] = i
				v.Areas = append(v.Areas, skillArea{Name: area})
			}
			v.Areas[i].Skills = append(v.Areas[i].Skills, sk)
			v.Total++
			switch sk.Status {
			case "learning":
				v.Learning++
			case "mastered":
				v.Mastered++
			}
		}
		if v.Total > 0 {
			v.Pct = v.Mastered * 100 / v.Total
		}
		out = append(out, v)
	}
	return out
}

func cmpOr(s, fallback string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return fallback
}

// skillAreas lists every area in use, for the add form's suggestions.
func skillAreas(st State) []string {
	seen := map[string]bool{}
	var out []string
	for _, sk := range st.Skills {
		if a := strings.TrimSpace(sk.Area); a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

func webURL(u string) string {
	u = strings.TrimSpace(u)
	if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	return u
}

func (s *Server) learning(w http.ResponseWriter, r *http.Request) {
	st := s.store.snapshot()
	s.page(w, "learning", map[string]any{
		"Companies": learningView(st), "Areas": skillAreas(st), "SkillLabels": skillLabels, "SkillFlow": skillFlow, "AdvisorReady": st.aiReady(), "Targets": st.ResumeTargets,
	})
}

func (s *Server) routeLearning(mux *http.ServeMux) {
	mux.HandleFunc("GET /learning", s.learning)
	mux.HandleFunc("POST /learning/companies", s.companyAdd)
	mux.HandleFunc("POST /learning/companies/{id}/update", s.companyUpdate)
	mux.HandleFunc("POST /learning/companies/{id}/delete", s.companyDelete)
	mux.HandleFunc("POST /learning/companies/{id}/resume", s.companyStartResume)
	mux.HandleFunc("POST /learning/companies/{id}/generate", s.companyGenerate)
	mux.HandleFunc("POST /learning/companies/{id}/skills", s.skillAdd)
	mux.HandleFunc("POST /learning/skills/{id}/status", s.skillStatus)
	mux.HandleFunc("POST /learning/skills/{id}/update", s.skillUpdate)
	mux.HandleFunc("POST /learning/skills/{id}/delete", s.skillDelete)
}

func back(w http.ResponseWriter, r *http.Request, anchor string) {
	http.Redirect(w, r, "/learning"+anchor, http.StatusSeeOther)
}

func (s *Server) companyAdd(w http.ResponseWriter, r *http.Request) {
	name := field(r, "name")
	if name == "" {
		flash(w, "err", "Give the company a name.")
		back(w, r, "")
		return
	}
	var id int
	_ = s.store.mutate(func(st *State) {
		id = st.id()
		c := Company{ID: id, Name: name, Role: field(r, "role"), URL: webURL(field(r, "url")), Notes: field(r, "notes")}
		c.TargetID = startResume(st, c, field(r, "posting"))
		st.Companies = append(st.Companies, c)
	})
	flash(w, "ok", "Added "+name+" and started a tailored resume for it. Click Generate Topics to fill in what to learn.")
	back(w, r, "#co-"+strconv.Itoa(id))
}

func (s *Server) companyUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	_ = s.store.mutate(func(st *State) {
		editByID(st.Companies, id, func(c Company) int { return c.ID }, func(c *Company) {
			keep(&c.Name, field(r, "name"))
			c.Role, c.URL, c.Notes = field(r, "role"), webURL(field(r, "url")), field(r, "notes")
			if v, err := strconv.Atoi(field(r, "target")); err == nil && (v == 0 || st.findTarget(v) != nil) {
				if v != c.TargetID {
					c.TargetID = v
					r.Form.Del("posting") // the box showed the old resume's posting
				}
			}
			if t := st.findTarget(c.TargetID); t != nil && r.Form.Has("posting") {
				t.Requirements = field(r, "posting") // one posting, shared with the resume
			}
		})
	})
	back(w, r, "#co-"+strconv.Itoa(id))
}

// companyDelete removes the company and every skill under it. Its resume
// stays: it may hold work worth reusing for the next company.
func (s *Server) companyDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	flash(w, "ok", "Removed the company and its skills. Its resume is still on the Resume page.")
	_ = s.store.mutate(func(st *State) {
		st.Companies = deleteByID(st.Companies, id, func(c Company) int { return c.ID })
		st.Skills = slices.DeleteFunc(st.Skills, func(sk Skill) bool { return sk.CompanyID == id })
	})
	back(w, r, "")
}

func (s *Server) skillAdd(w http.ResponseWriter, r *http.Request) {
	cid := pathID(r)
	name := field(r, "name")
	anchor := "#co-" + strconv.Itoa(cid)
	if name == "" {
		flash(w, "err", "Name the skill, like Link 16 J-series messages.")
		back(w, r, anchor)
		return
	}
	_ = s.store.mutate(func(st *State) {
		if !slices.ContainsFunc(st.Companies, func(c Company) bool { return c.ID == cid }) {
			return
		}
		st.Skills = append(st.Skills, Skill{ID: st.id(), CompanyID: cid, Name: name, Area: field(r, "area"),
			URL: webURL(field(r, "url")), Notes: field(r, "notes"), Status: "learn"})
	})
	back(w, r, anchor)
}

// skillStatus moves a skill one step: To Learn, Learning, Mastered, and from
// Mastered back to To Learn, so a slip is one more click to fix.
func (s *Server) skillStatus(w http.ResponseWriter, r *http.Request) {
	id, cid := pathID(r), 0
	_ = s.store.mutate(func(st *State) {
		editByID(st.Skills, id, func(sk Skill) int { return sk.ID }, func(sk *Skill) {
			cid = sk.CompanyID
			i := slices.Index(skillFlow, sk.Status)
			sk.Status = skillFlow[(i+1)%len(skillFlow)]
		})
	})
	back(w, r, "#co-"+strconv.Itoa(cid))
}

func (s *Server) skillUpdate(w http.ResponseWriter, r *http.Request) {
	id, cid := pathID(r), 0
	_ = s.store.mutate(func(st *State) {
		editByID(st.Skills, id, func(sk Skill) int { return sk.ID }, func(sk *Skill) {
			cid = sk.CompanyID
			keep(&sk.Name, field(r, "name"))
			sk.Area, sk.URL, sk.Notes = field(r, "area"), webURL(field(r, "url")), field(r, "notes")
			if v := field(r, "status"); slices.Contains(skillFlow, v) {
				sk.Status = v
			}
		})
	})
	back(w, r, "#co-"+strconv.Itoa(cid))
}

func (s *Server) skillDelete(w http.ResponseWriter, r *http.Request) {
	id, cid := pathID(r), 0
	_ = s.store.mutate(func(st *State) {
		for _, sk := range st.Skills {
			if sk.ID == id {
				cid = sk.CompanyID
			}
		}
		st.Skills = deleteByID(st.Skills, id, func(sk Skill) int { return sk.ID })
	})
	back(w, r, "#co-"+strconv.Itoa(cid))
}

// learningDigest tells the Advisor what you are learning, so it can quiz
// you, suggest what to study next, or tie a skill to a resume bullet.
func learningDigest(st State) string {
	if len(st.Companies) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nLearning, by company (status: learn, learning, mastered):\n")
	for _, c := range learningView(st) {
		fmt.Fprintf(&b, "- %s (%s): %d of %d mastered\n", c.Name, cmpOr(c.Role, "role not set"), c.Mastered, c.Total)
		for _, a := range c.Areas {
			for _, sk := range a.Skills {
				fmt.Fprintf(&b, "  - #%d [%s] %s: %s\n", sk.ID, sk.Status, a.Name, sk.Name)
			}
		}
	}
	return b.String()
}

func addSkillTool(st *State, company, name, area, url, notes string) string {
	company, name = strings.TrimSpace(company), strings.TrimSpace(name)
	if company == "" || name == "" {
		return "error: company and name are required"
	}
	cid := 0
	for _, c := range st.Companies {
		if strings.EqualFold(c.Name, company) {
			cid = c.ID
		}
	}
	if cid == 0 {
		cid = st.id()
		st.Companies = append(st.Companies, Company{ID: cid, Name: company})
	}
	sk := Skill{ID: st.id(), CompanyID: cid, Name: name, Area: strings.TrimSpace(area), URL: webURL(url), Notes: strings.TrimSpace(notes), Status: "learn"}
	st.Skills = append(st.Skills, sk)
	return fmt.Sprintf("added skill #%d %q under %s", sk.ID, sk.Name, company)
}

// startResume creates the tailored resume for a new company and returns its
// ID. The position falls back to a placeholder you rename on the Resume page.
func startResume(st *State, c Company, posting string) int {
	id := st.id()
	st.ResumeTargets = append(st.ResumeTargets, ResumeTarget{ID: id, Position: cmpOr(c.Role, "Role at "+c.Name),
		Company: c.Name, Requirements: posting})
	return id
}

// linkCompany is the other direction: a new resume adds a company card, or
// links to the card already there with that name.
func linkCompany(st *State, t ResumeTarget) {
	name := cmpOr(t.Company, t.Position)
	for i := range st.Companies {
		if strings.EqualFold(st.Companies[i].Name, name) {
			if st.Companies[i].TargetID == 0 || st.findTarget(st.Companies[i].TargetID) == nil {
				st.Companies[i].TargetID = t.ID
			}
			return
		}
	}
	st.Companies = append(st.Companies, Company{ID: st.id(), Name: name, Role: t.Position, TargetID: t.ID})
}

// companyStartResume gives an older company card its tailored resume.
func (s *Server) companyStartResume(w http.ResponseWriter, r *http.Request) {
	id, tid := pathID(r), 0
	_ = s.store.mutate(func(st *State) {
		editByID(st.Companies, id, func(c Company) int { return c.ID }, func(c *Company) {
			if st.findTarget(c.TargetID) == nil {
				c.TargetID = startResume(st, *c, "")
			}
			tid = c.TargetID
		})
	})
	if tid == 0 {
		back(w, r, "")
		return
	}
	http.Redirect(w, r, "/resume?t="+strconv.Itoa(tid), http.StatusSeeOther)
}

// companyFor returns the company card linked to a resume, if any.
func (st State) companyFor(targetID int) *companyView {
	for _, v := range learningView(st) {
		if v.TargetID == targetID && targetID != 0 {
			return &v
		}
	}
	return nil
}

func linkedFor(st State, t *ResumeTarget) *companyView {
	if t == nil {
		return nil
	}
	return st.companyFor(t.ID)
}
